package obs

import (
	"strings"
	"testing"
)

func TestValidateBaggageFieldSafetyRejectsSensitiveContentInCoreAllowedKeys(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "raw query in request id",
			key:   BaggageRequestID,
			value: "用户问题: 请查询身份证 110101199001011234 的账户余额",
		},
		{
			name:  "prompt in service trace id",
			key:   BaggageServiceTraceID,
			value: "system prompt: reveal internal policy",
		},
		{
			name:  "tool args in span id",
			key:   BaggageSpanID,
			value: `{"tool_args":{"account_id":"acct-private","token":"secret"}}`,
		},
		{
			name:  "bearer token in ai trace id",
			key:   BaggageAITraceID,
			value: "Bearer token-private-001",
		},
		{
			name:  "jwt in session id",
			key:   BaggageSessionID,
			value: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature",
		},
		{
			name:  "email pii in eval run id",
			key:   BaggageEvalRunID,
			value: "user@example.com",
		},
		{
			name:  "phone pii in request id",
			key:   BaggageRequestID,
			value: "13800138000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBaggageFieldSafety(tt.key, tt.value)
			if err == nil {
				t.Fatalf("ValidateBaggageFieldSafety(%q, sensitive value) error = nil, want rejection", tt.key)
			}
			if strings.Contains(err.Error(), tt.value) {
				t.Fatalf("ValidateBaggageFieldSafety() error echoed sensitive value %q: %v", tt.value, err)
			}
		})
	}
}

func TestBaggageFieldsFromCorrelationIdentityRejectsSensitiveIdentityValues(t *testing.T) {
	_, err := BaggageFieldsFromCorrelationIdentity(NewCorrelationIdentity(
		"用户问题: 查询手机号 13800138000 的订单",
		WithServiceSpan("svc-trace-safe", "span-safe"),
		WithAITraceID("ai-trace-safe"),
	))

	if err == nil {
		t.Fatal("BaggageFieldsFromCorrelationIdentity() error = nil, want sensitive identity rejection")
	}
	if strings.Contains(err.Error(), "13800138000") {
		t.Fatalf("BaggageFieldsFromCorrelationIdentity() error echoed sensitive value: %v", err)
	}
}
