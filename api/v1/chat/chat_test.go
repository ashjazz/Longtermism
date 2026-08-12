package chat

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gogf/gf/v2/util/gvalid"
)

const (
	wantChatSmokeRunIDHeader         = "X-Observability-Smoke-Run-ID"
	wantChatSmokeAuthorizationHeader = "X-Observability-Smoke-Authorization"
)

func TestChatRequestDTOContract(t *testing.T) {
	requestType := reflect.TypeFor[ChatReq]()
	meta, ok := requestType.FieldByName("Meta")
	if !ok || meta.Tag.Get("path") != "/chat" || meta.Tag.Get("method") != "post" {
		t.Fatal("ChatReq must retain GoFrame POST /api/v1/chat route metadata")
	}
	message, ok := requestType.FieldByName("Message")
	if !ok || message.Tag.Get("json") != "message" || message.Tag.Get("v") != "required" {
		t.Fatal("ChatReq must expose message as its only JSON request field and declare the required rule")
	}
	if requestType.NumField() != 2 {
		t.Fatal("ChatReq must not expose server-owned provider, model, URL, credential, or debug fields")
	}
}

func TestChatRequestDTOUsesGoFrameRequiredRule(t *testing.T) {
	err := gvalid.New().Data(ChatReq{}).Run(context.Background())
	if err == nil {
		t.Fatal("ChatReq message must be rejected by its GoFrame required validation tag")
	}
}

func TestChatResponseDTOContract(t *testing.T) {
	score := 0.95
	tests := []struct {
		name     string
		response any
		wantNull bool
	}{
		{
			name: "success has stable public fields",
			response: ChatSuccessEnvelope{
				Code: 0, Message: "OK",
				Data: ChatData{Content: "completed", Model: "actual-model", FinishReason: FinishReasonStop, Usage: UsageSummary{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
				Meta: ChatSuccessMeta{RequestID: "req-api", AITraceID: "ai-api", EvalSummary: &EvalSummary{Status: EvalStatusPassed, Evaluator: "completion_contract_v1", Score: &score, ReasonClass: "within_policy"}},
			},
		},
		{name: "pre-AI error has null data", response: ChatPreAIErrorEnvelope{Code: 400, Message: "invalid chat request", Data: nil, Meta: ChatPreAIErrorMeta{RequestID: "req-pre-ai"}}, wantNull: true},
		{name: "post-AI error has null data and correlation", response: ChatPostAIErrorEnvelope{Code: 502, Message: "chat upstream unavailable", Data: nil, Meta: ChatPostAIErrorMeta{RequestID: "req-post-ai", AITraceID: "ai-post-ai"}}, wantNull: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatalf("marshal chat DTO: %v", err)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(payload, &envelope); err != nil {
				t.Fatalf("unmarshal chat DTO: %v", err)
			}
			if len(envelope) != 4 || envelope["code"] == nil || envelope["message"] == nil || envelope["data"] == nil || envelope["meta"] == nil {
				t.Fatalf("chat DTO envelope = %s, want code/message/data/meta", payload)
			}
			if tt.wantNull && string(envelope["data"]) != "null" {
				t.Fatalf("error data = %s, want null", envelope["data"])
			}
		})
	}
}

// Smoke admission is expressed exclusively through protected headers. Keeping it out of ChatReq
// prevents ordinary API clients from persisting a privileged marker in the public JSON model.
func TestChatSmokeContractKeepsPublicDTOsUnprivileged(t *testing.T) {
	requestType := reflect.TypeFor[ChatReq]()
	for _, forbidden := range []string{"SmokeRunID", "SmokeAuthorization", "ServiceTraceID", "SpanID"} {
		if _, ok := requestType.FieldByName(forbidden); ok {
			t.Fatalf("ChatReq must not expose privileged field %q", forbidden)
		}
	}

	payload, err := json.Marshal(ChatSuccessMeta{RequestID: "req-t177", AITraceID: "ai-t177"})
	if err != nil {
		t.Fatalf("marshal public chat metadata: %v", err)
	}
	for _, forbidden := range []string{"smoke_run_id", "service_trace_id", "span_id", "authorization"} {
		if strings.Contains(strings.ToLower(string(payload)), forbidden) {
			t.Fatalf("public chat metadata leaked %q: %s", forbidden, payload)
		}
	}

	// These constants are owned by the HTTP contract rather than the request DTO. Their absence is
	// the intentional RED signal until T182 implements the protected admission boundary.
	if ChatSmokeRunIDHeader != wantChatSmokeRunIDHeader || ChatSmokeAuthorizationHeader != wantChatSmokeAuthorizationHeader {
		t.Fatal("chat smoke header constants do not match the public contract")
	}
}
