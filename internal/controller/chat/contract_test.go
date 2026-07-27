package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/ashjazz/Longtermism/api/v1/chat"
)

func TestDecodeAndValidateChatRequestOwnsHTTPBoundary(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{name: "accepts one valid object", body: []byte(`{"message":"hello"}`)},
		{name: "rejects caller-selected provider", body: []byte(`{"message":"hello","provider":"caller-controlled"}`), wantErr: true},
		{name: "rejects trailing JSON", body: []byte(`{"message":"hello"}{}`), wantErr: true},
		{name: "rejects invalid UTF-8", body: []byte{'{', '"', 'm', '"', ':', '"', 0xff, '"', '}'}, wantErr: true},
		{name: "rejects blank message", body: []byte(`{"message":" \t\n"}`), wantErr: true},
		{name: "rejects overlong UTF-8 message", body: []byte(`{"message":"` + strings.Repeat("你", 10923) + `"}`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := DecodeAndValidateChatRequest(context.Background(), tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DecodeAndValidateChatRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && request.Message != "hello" {
				t.Fatalf("decoded message = %q, want hello", request.Message)
			}
		})
	}
}

func TestChatResponseFactoriesKeepPublicEnvelopeSafe(t *testing.T) {
	summary := v1.EvalSummary{Status: v1.EvalStatusPassed, Evaluator: "contract_check", ReasonClass: "within_policy"}
	success, err := NewChatSuccessEnvelope("req-controller", "ai-controller", v1.ChatData{Content: "completed", Model: "actual-model", FinishReason: v1.FinishReasonStop}, &summary, true)
	if err != nil {
		t.Fatalf("NewChatSuccessEnvelope() error = %v", err)
	}
	preAIError, err := NewPreAIChatErrorEnvelope(400, "invalid chat request", "req-pre-ai")
	if err != nil {
		t.Fatalf("NewPreAIChatErrorEnvelope() error = %v", err)
	}
	postAIError, err := NewPostAIChatErrorEnvelope(502, "chat upstream unavailable", "req-post-ai", "ai-post-ai")
	if err != nil {
		t.Fatalf("NewPostAIChatErrorEnvelope() error = %v", err)
	}

	for _, response := range []any{success, preAIError, postAIError} {
		payload, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			t.Fatalf("marshal controller response: %v", marshalErr)
		}
		if strings.Contains(string(payload), "provider") || strings.Contains(string(payload), "credential") {
			t.Fatalf("controller response exposed a forbidden transport detail: %s", payload)
		}
	}
	if _, err := NewPreAIChatErrorEnvelope(502, "wrong branch", "req-pre-ai"); err == nil {
		t.Fatal("pre-AI error factory must reject post-AI status codes")
	}
	if _, err := NewPostAIChatErrorEnvelope(400, "wrong branch", "req-post-ai", "ai-post-ai"); err == nil {
		t.Fatal("post-AI error factory must reject pre-AI status codes")
	}
	providerRateLimit, err := NewPostAIChatErrorEnvelope(429, "chat rate limited", "req-provider-rate-limit", "ai-provider-rate-limit")
	if err != nil || providerRateLimit.Meta.AITraceID != "ai-provider-rate-limit" {
		t.Fatalf("provider rate-limit error must retain started AI identity, envelope=%#v error=%v", providerRateLimit, err)
	}
	if _, err := NewPreAIChatErrorEnvelope(400, "missing identity", ""); err == nil {
		t.Fatal("pre-AI error factory must require request identity")
	}
	if _, err := NewPostAIChatErrorEnvelope(502, "missing identity", "req-post-ai", ""); err == nil {
		t.Fatal("post-AI error factory must require AI identity")
	}
}

func TestChatSuccessFactoryAppliesDebugSummaryPolicyAndSnapshotsInput(t *testing.T) {
	score := 0.95
	summary := &v1.EvalSummary{Status: v1.EvalStatusPassed, Evaluator: "contract_check", Score: &score, ReasonClass: "within_policy"}
	envelope, err := NewChatSuccessEnvelope("req-snapshot", "ai-snapshot", v1.ChatData{}, summary, true)
	if err != nil {
		t.Fatalf("NewChatSuccessEnvelope() error = %v", err)
	}

	// The evaluator may reuse its object after returning. A public response must retain the
	// validated snapshot rather than aliasing mutable score/reason fields.
	summary.ReasonClass = "api_key_sk_live_secret"
	*summary.Score = 2
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal success envelope: %v", err)
	}
	if strings.Contains(string(payload), "secret") || strings.Contains(string(payload), `"score":2`) {
		t.Fatalf("success envelope must retain validated summary snapshot: %s", payload)
	}

	withoutDebug, err := NewChatSuccessEnvelope("req-no-debug", "ai-no-debug", v1.ChatData{}, summary, false)
	if err != nil {
		t.Fatalf("NewChatSuccessEnvelope(debug=false) error = %v", err)
	}
	if withoutDebug.Meta.EvalSummary != nil {
		t.Fatal("debug-disabled success must omit eval summary")
	}
	if _, err := NewChatSuccessEnvelope("", "ai-missing-request", v1.ChatData{}, nil, false); err == nil {
		t.Fatal("success factory must require request identity")
	}
	if _, err := NewChatSuccessEnvelope("req-missing-ai", "", v1.ChatData{}, nil, false); err == nil {
		t.Fatal("success factory must require AI identity")
	}
	if _, err := NewChatSuccessEnvelope("req-invalid-summary", "ai-invalid-summary", v1.ChatData{}, &v1.EvalSummary{Status: v1.EvalStatusPassed, ReasonClass: "api_key_sk_live_secret"}, true); err == nil {
		t.Fatal("success factory must reject non-allowlisted debug classifications")
	}
	if _, err := NewChatSuccessEnvelope("req-invalid-score", "ai-invalid-score", v1.ChatData{}, &v1.EvalSummary{Status: v1.EvalStatusPassed, Score: ptrScore(1.1)}, true); err == nil {
		t.Fatal("success factory must reject out-of-range debug scores")
	}
	if _, err := NewChatSuccessEnvelope("req-invalid-status", "ai-invalid-status", v1.ChatData{}, &v1.EvalSummary{Status: "unknown"}, true); err == nil {
		t.Fatal("success factory must reject unknown debug statuses")
	}
}

func ptrScore(value float64) *float64 { return &value }
