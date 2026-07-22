// Package chat fixes the public contract for the first non-streaming chat request.
//
// These tests intentionally stop at the DTO boundary. Controller, limiter and provider behavior
// are covered by their own Phase 4 tasks, so this contract cannot accidentally invent an AI
// identity before the usecase has actually started.
package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gogf/gf/v2/util/gvalid"
)

const maxChatMessageBytes = 32 * 1024

func TestChatRequestContract(t *testing.T) {
	requestType := reflect.TypeFor[ChatReq]()
	meta, ok := requestType.FieldByName("Meta")
	if !ok {
		t.Fatal("ChatReq must declare GoFrame route metadata")
	}
	if meta.Tag.Get("path") != "/chat" || meta.Tag.Get("method") != "post" {
		t.Fatal("ChatReq route metadata must describe POST /api/v1/chat")
	}

	message, ok := requestType.FieldByName("Message")
	if !ok || message.Tag.Get("json") != "message" {
		t.Fatal("ChatReq must expose message as its only JSON request field")
	}
	assertChatRequestDoesNotExposeServerConfiguration(t, requestType)

	tests := []struct {
		name    string
		message string
		wantErr bool
	}{
		{name: "accepts a normal message", message: "请用一句话介绍 Longtermism。"},
		{name: "accepts exactly 32 KiB", message: strings.Repeat("a", maxChatMessageBytes)},
		{name: "accepts exactly 32 KiB of valid multibyte UTF-8", message: strings.Repeat("你", 10922) + "aa"},
		{name: "rejects missing message", message: "", wantErr: true},
		{name: "rejects whitespace only message", message: " \t\n　", wantErr: true},
		{name: "rejects a message over 32 KiB", message: strings.Repeat("a", maxChatMessageBytes+1), wantErr: true},
		{name: "rejects valid multibyte UTF-8 over 32 KiB", message: strings.Repeat("你", 10922) + "aaa", wantErr: true},
		{name: "rejects invalid UTF-8", message: string([]byte{0xff}), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChatMessage(tt.message)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateChatMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err := gvalid.New().Data(ChatReq{Message: tt.message}).Run(context.Background()); (err != nil) != tt.wantErr {
				t.Fatalf("ChatReq validation error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	// additionalProperties:false must be enforced at the transport boundary. A plain json.Unmarshal
	// would silently ignore these fields and let callers attempt to select server-owned settings.
	for _, field := range []string{"debug", "provider", "model", "base_url", "api_key", "unexpected"} {
		t.Run("rejects extra field "+field, func(t *testing.T) {
			body := []byte(`{"message":"hello","` + field + `":"caller-controlled"}`)
			if _, err := DecodeChatRequest(body); err == nil {
				t.Fatalf("DecodeChatRequest() accepted forbidden field %q", field)
			}
		})
	}
}

func TestChatSuccessEnvelopeContract(t *testing.T) {
	tests := []struct {
		name            string
		debugEnabled    bool
		summary         *EvalSummary
		usage           UsageSummary
		wantEvalSummary bool
	}{
		{
			name: "omits eval summary when debug is disabled even when evaluation completed",
			summary: &EvalSummary{
				Status: EvalStatusPassed,
			},
			usage: UsageSummary{InputTokens: 12, OutputTokens: 8, TotalTokens: 20},
		},
		{
			name:         "includes a bounded low sensitivity eval summary when debug is enabled",
			debugEnabled: true,
			summary: &EvalSummary{
				Status:      EvalStatusPassed,
				Evaluator:   "contract-check",
				Score:       ptrFloat64(0.95),
				ReasonClass: "within_policy",
			},
			usage: UsageSummary{
				InputTokens:      12,
				OutputTokens:     8,
				ReasoningTokens:  3,
				CacheReadTokens:  2,
				CacheWriteTokens: 1,
				TotalTokens:      26,
			},
			wantEvalSummary: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := NewChatMeta("req-chat-success", "ai-chat-success", tt.summary, tt.debugEnabled)
			if err != nil {
				t.Fatalf("NewChatMeta() error = %v", err)
			}
			envelope := ChatSuccessEnvelope{
				Code:    0,
				Message: "OK",
				Data: ChatData{
					Content:      "Longtermism is an observability-first AI Agent Harness.",
					Model:        "configured-model",
					FinishReason: FinishReasonStop,
					Usage:        tt.usage,
				},
				Meta: meta,
			}
			assertChatSuccessEnvelope(t, envelope, tt.wantEvalSummary)
		})
	}

	assertChatMetaHasRequestIDJSONKey(t, ChatMeta{})
}

func TestChatEvalSummaryContract(t *testing.T) {
	tests := []struct {
		name    string
		summary EvalSummary
		wantErr bool
	}{
		{name: "accepts a low sensitivity summary", summary: EvalSummary{Status: EvalStatusWarning, Evaluator: "contract-check", ReasonClass: "low_confidence"}},
		{name: "rejects an oversized summary", summary: EvalSummary{Status: EvalStatusFailed, ReasonClass: strings.Repeat("a", 1025)}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateEvalSummary(tt.summary); (err != nil) != tt.wantErr {
				t.Fatalf("ValidateEvalSummary() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChatErrorEnvelopeContract(t *testing.T) {
	tests := []struct {
		name          string
		code          int
		meta          ChatMeta
		wantAITraceID bool
	}{
		{name: "invalid input stops before AI usecase", code: http.StatusBadRequest, meta: ChatMeta{RequestID: "req-chat-400"}},
		{name: "local rate limit stops before AI usecase", code: http.StatusTooManyRequests, meta: ChatMeta{RequestID: "req-chat-429"}},
		{name: "upstream failure keeps identity after AI usecase starts", code: http.StatusBadGateway, meta: ChatMeta{RequestID: "req-chat-502", AITraceID: "ai-chat-502"}, wantAITraceID: true},
		{name: "upstream timeout keeps identity after AI usecase starts", code: http.StatusGatewayTimeout, meta: ChatMeta{RequestID: "req-chat-504", AITraceID: "ai-chat-504"}, wantAITraceID: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := ChatErrorEnvelope{
				Code:    tt.code,
				Message: "chat request failed",
				Data:    nil,
				Meta:    tt.meta,
			}
			assertChatErrorEnvelope(t, envelope, tt.code, tt.wantAITraceID)
		})
	}
}

func assertChatRequestDoesNotExposeServerConfiguration(t *testing.T, requestType reflect.Type) {
	t.Helper()
	for index := range requestType.NumField() {
		field := requestType.Field(index)
		if field.Name == "Meta" || field.Name == "Message" {
			continue
		}
		t.Fatalf("ChatReq must not expose server-owned field %q", field.Name)
	}
}

func assertChatSuccessEnvelope(t *testing.T, envelope ChatSuccessEnvelope, wantEvalSummary bool) {
	t.Helper()
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal chat success envelope: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode chat success envelope: %v", err)
	}
	if len(raw) != 4 || string(raw["code"]) != "0" || raw["message"] == nil || raw["data"] == nil || raw["meta"] == nil {
		t.Fatalf("chat success envelope = %s, want code/message/data/meta with code 0", payload)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw["data"], &data); err != nil {
		t.Fatalf("decode chat data: %v", err)
	}
	for _, field := range []string{"content", "model", "finish_reason", "usage"} {
		if data[field] == nil {
			t.Fatalf("chat data must contain %q: %s", field, raw["data"])
		}
	}
	if len(data) != 4 {
		t.Fatalf("chat data must not expose extra fields: %s", raw["data"])
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(data["usage"], &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if usage[field] == nil {
			t.Fatalf("usage must contain %q: %s", field, data["usage"])
		}
	}
	allowedUsageFields := map[string]struct{}{
		"input_tokens": {}, "output_tokens": {}, "reasoning_tokens": {},
		"cache_read_tokens": {}, "cache_write_tokens": {}, "total_tokens": {},
	}
	for field := range usage {
		if _, ok := allowedUsageFields[field]; !ok {
			t.Fatalf("usage must not expose extra field %q: %s", field, data["usage"])
		}
	}
	assertChatMeta(t, raw["meta"], true, wantEvalSummary)
}

func assertChatErrorEnvelope(t *testing.T, envelope ChatErrorEnvelope, wantCode int, wantAITraceID bool) {
	t.Helper()
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal chat error envelope: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode chat error envelope: %v", err)
	}
	if len(raw) != 4 || string(raw["code"]) != strconv.Itoa(wantCode) || raw["message"] == nil || string(raw["data"]) != "null" || raw["meta"] == nil {
		t.Fatalf("chat error envelope = %s, want code/message/data:null/meta", payload)
	}
	assertChatMeta(t, raw["meta"], wantAITraceID, false)
}

func assertChatMeta(t *testing.T, payload json.RawMessage, wantAITraceID, wantEvalSummary bool) {
	t.Helper()
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(payload, &meta); err != nil {
		t.Fatalf("decode chat meta: %v", err)
	}
	if meta["request_id"] == nil {
		t.Fatalf("chat metadata must always contain request_id: %s", payload)
	}
	if (meta["ai_trace_id"] != nil) != wantAITraceID {
		t.Fatalf("ai_trace_id presence = %v, want %v: %s", meta["ai_trace_id"] != nil, wantAITraceID, payload)
	}
	if (meta["eval_summary"] != nil) != wantEvalSummary {
		t.Fatalf("eval_summary presence = %v, want %v: %s", meta["eval_summary"] != nil, wantEvalSummary, payload)
	}
	if meta["smoke_run_id"] != nil {
		t.Fatalf("chat metadata must not contain infra-only smoke_run_id: %s", payload)
	}
	if len(meta) != 1+boolToInt(wantAITraceID)+boolToInt(wantEvalSummary) {
		t.Fatalf("chat metadata must not expose extra fields: %s", payload)
	}
}

func assertChatMetaHasRequestIDJSONKey(t *testing.T, meta ChatMeta) {
	t.Helper()
	payload, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal zero-value chat meta: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode zero-value chat meta: %v", err)
	}
	if raw["request_id"] == nil {
		t.Fatal("request_id must not be omitted when its value is empty")
	}
}

func ptrFloat64(value float64) *float64 {
	return &value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
