package obs

import "testing"

func TestBaggageFieldsFromCorrelationIdentityAllowsOnlyLowSensitivityFields(t *testing.T) {
	identity := NewCorrelationIdentity(
		"req-baggage",
		WithServiceSpan("svc-trace-baggage", "span-baggage"),
		WithAITraceID("ai-trace-baggage"),
		WithSessionID("session-baggage"),
		WithEvalRunID("eval-run-baggage"),
	)

	fields, err := BaggageFieldsFromCorrelationIdentity(identity)
	if err != nil {
		t.Fatalf("BaggageFieldsFromCorrelationIdentity() error = %v, want nil", err)
	}

	want := map[string]string{
		"request_id":       "req-baggage",
		"service_trace_id": "svc-trace-baggage",
		"span_id":          "span-baggage",
		"ai_trace_id":      "ai-trace-baggage",
		"session_id":       "session-baggage",
		"eval_run_id":      "eval-run-baggage",
	}
	assertStringMapsEqual(t, fields, want)

	fields["request_id"] = "mutated"
	fieldsAgain, err := BaggageFieldsFromCorrelationIdentity(identity)
	if err != nil {
		t.Fatalf("BaggageFieldsFromCorrelationIdentity() second error = %v, want nil", err)
	}
	if fieldsAgain["request_id"] != "req-baggage" {
		t.Fatalf("second baggage request_id = %q, want defensive copy req-baggage", fieldsAgain["request_id"])
	}
}

func TestValidateBaggageFieldRejectsForbiddenKeys(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "raw query", key: "raw_query", value: "where is my order"},
		{name: "prompt content", key: "prompt_content", value: "system prompt"},
		{name: "tool args", key: "tool_args", value: `{"account_id":"acct-001"}`},
		{name: "api key", key: "api_key", value: "sk-private"},
		{name: "authorization", key: "authorization", value: "Bearer secret"},
		{name: "external response", key: "external_response", value: `{"private":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateBaggageField(tt.key, tt.value); err == nil {
				t.Fatalf("ValidateBaggageField(%q, %q) error = nil, want rejection", tt.key, tt.value)
			}
		})
	}
}

func TestValidateBaggageFieldRejectsSensitiveValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "bearer token", key: "request_id", value: "Bearer token-private-001"},
		{name: "openai style secret", key: "ai_trace_id", value: "sk-private-001"},
		{name: "password pair", key: "session_id", value: "password=private"},
		{name: "jwt value", key: "eval_run_id", value: "eyJhbGciOiJIUzI1NiJ9.private.signature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateBaggageField(tt.key, tt.value); err == nil {
				t.Fatalf("ValidateBaggageField(%q, %q) error = nil, want sensitive value rejection", tt.key, tt.value)
			}
		})
	}
}

func TestValidateBaggageFieldAllowsCorrelationFields(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "request id", key: "request_id", value: "req-safe"},
		{name: "service trace id", key: "service_trace_id", value: "svc-trace-safe"},
		{name: "span id", key: "span_id", value: "span-safe"},
		{name: "ai trace id", key: "ai_trace_id", value: "ai-trace-safe"},
		{name: "session id", key: "session_id", value: "session-safe"},
		{name: "eval run id", key: "eval_run_id", value: "eval-run-safe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateBaggageField(tt.key, tt.value); err != nil {
				t.Fatalf("ValidateBaggageField(%q, %q) error = %v, want nil", tt.key, tt.value, err)
			}
		})
	}
}

func assertStringMapsEqual(t *testing.T, got, want map[string]string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("map length = %d, want %d; got %#v", len(got), len(want), got)
	}
	for key, wantValue := range want {
		if gotValue := got[key]; gotValue != wantValue {
			t.Fatalf("map[%q] = %q, want %q; got %#v", key, gotValue, wantValue, got)
		}
	}
}
