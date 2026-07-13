package observability

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildHTTPCompletionLogRendersOnlyContractFields(t *testing.T) {
	// HTTP completion log 是 Loki/Tempo 关联的稳定边界。测试故意给输入塞入原始
	// 请求和 provider 内容，确保实现只能从白名单字段构造 JSON，而不是序列化整个请求。
	tests := []struct {
		name          string
		input         HTTPCompletionLogInput
		want          map[string]any
		omittedFields []string
	}{
		{
			name: "infrastructure completion uses UTC and omits AI and smoke identities",
			// 即使边界层错误地带入了身份，infra 请求也绝不能借日志泄露 AI/smoke 语义。
			input: newHTTPCompletionLogInput(false, false, false),
			want: map[string]any{
				"timestamp":   "2026-07-13T01:02:03.456Z",
				"level":       "info",
				"message":     "http request completed",
				"request_id":  "req-t018",
				"trace_id":    "0123456789abcdef0123456789abcdef",
				"span_id":     "0123456789abcdef",
				"route":       "/api/v1/chat",
				"method":      "POST",
				"status":      float64(200),
				"duration_ms": float64(120),
			},
			omittedFields: []string{"error_class", "ai_trace_id", "smoke_run_id"},
		},
		{
			name:  "successful AI request keeps AI identity but omits smoke identity",
			input: newHTTPCompletionLogInput(false, true, false),
			want: map[string]any{
				"timestamp":   "2026-07-13T01:02:03.456Z",
				"level":       "info",
				"message":     "http request completed",
				"request_id":  "req-t018",
				"trace_id":    "0123456789abcdef0123456789abcdef",
				"span_id":     "0123456789abcdef",
				"route":       "/api/v1/chat",
				"method":      "POST",
				"status":      float64(200),
				"duration_ms": float64(120),
				"ai_trace_id": "ai-t018",
			},
			omittedFields: []string{"error_class", "smoke_run_id"},
		},
		{
			name:  "infrastructure smoke keeps smoke identity without AI identity",
			input: newHTTPCompletionLogInput(false, false, true),
			want: map[string]any{
				"timestamp":    "2026-07-13T01:02:03.456Z",
				"level":        "info",
				"message":      "http request completed",
				"request_id":   "req-t018",
				"trace_id":     "0123456789abcdef0123456789abcdef",
				"span_id":      "0123456789abcdef",
				"route":        "/api/v1/chat",
				"method":       "POST",
				"status":       float64(200),
				"duration_ms":  float64(120),
				"smoke_run_id": "smoke-t018",
			},
			omittedFields: []string{"error_class", "ai_trace_id"},
		},
		{
			name:  "failed AI smoke request keeps only stable classifications",
			input: newHTTPCompletionLogInput(true, true, true),
			want: map[string]any{
				"timestamp":    "2026-07-13T01:02:03.456Z",
				"level":        "error",
				"message":      "http request failed",
				"request_id":   "req-t018",
				"trace_id":     "0123456789abcdef0123456789abcdef",
				"span_id":      "0123456789abcdef",
				"route":        "/api/v1/chat",
				"method":       "POST",
				"status":       float64(502),
				"duration_ms":  float64(120),
				"error_class":  "upstream_unavailable",
				"ai_trace_id":  "ai-t018",
				"smoke_run_id": "smoke-t018",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := BuildHTTPCompletionLog(tt.input)
			if err != nil {
				t.Fatal("BuildHTTPCompletionLog() returned an unexpected error")
			}
			encoded, err := json.Marshal(entry)
			if err != nil {
				t.Fatal("HTTPCompletionLog could not be encoded as JSON")
			}
			if strings.ContainsAny(string(encoded), "\r\n") {
				t.Fatal("completion log JSON was not a single line")
			}

			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal("BuildHTTPCompletionLog() did not return valid JSON")
			}
			for _, field := range tt.omittedFields {
				if _, exists := got[field]; exists {
					t.Fatalf("conditional field %q was unexpectedly present", field)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatal("completion log fields did not exactly match the allowlist contract")
			}
			assertHTTPCompletionLogDoesNotContainSensitiveInput(t, string(encoded))
		})
	}
}

func TestBuildHTTPCompletionLogRejectsFailedRequestWithoutStableErrorClass(t *testing.T) {
	input := newHTTPCompletionLogInput(true, false, false)
	input.ErrorClass = ""

	_, err := BuildHTTPCompletionLog(input)
	if err == nil {
		t.Fatal("BuildHTTPCompletionLog() error = nil, want failed request without error class rejected")
	}
	assertHTTPCompletionLogDoesNotContainSensitiveInput(t, err.Error())
}

func newHTTPCompletionLogInput(isFailed, isAI, isSmoke bool) HTTPCompletionLogInput {
	input := HTTPCompletionLogInput{
		Timestamp:     time.Date(2026, time.July, 13, 9, 2, 3, 456000000, time.FixedZone("CST", 8*60*60)),
		RequestID:     "req-t018",
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "0123456789abcdef",
		RouteTemplate: "/api/v1/chat",
		Method:        "POST",
		StatusCode:    200,
		Duration:      120 * time.Millisecond,
		IsAIRequest:   isAI,
		IsSmokeRun:    isSmoke,
		AITraceID:     "ai-t018",
		SmokeRunID:    "smoke-t018",

		Authorization:      "Bearer synthetic-t018-authorization",
		APIKey:             "sk-t018-synthetic-key",
		RawQuery:           "message=synthetic-private-prompt",
		Prompt:             "synthetic-private-prompt",
		Output:             "synthetic-private-output",
		ToolArguments:      `{"email":"person-t018@example.test"}`,
		ProviderErrorBody:  "synthetic-provider-error-body",
		RecognizedPII:      "person-t018@example.test",
		EndpointCredential: "synthetic-t018-endpoint-credential",
	}
	if isFailed {
		input.StatusCode = 502
		input.ErrorClass = "upstream_unavailable"
	}
	return input
}

func assertHTTPCompletionLogDoesNotContainSensitiveInput(t *testing.T, rendered string) {
	t.Helper()
	for _, forbidden := range []string{
		"synthetic-t018-authorization",
		"sk-t018-synthetic-key",
		"synthetic-private-prompt",
		"synthetic-private-output",
		"person-t018@example.test",
		"synthetic-provider-error-body",
		"synthetic-t018-endpoint-credential",
		"Authorization",
		"api_key",
		"raw_query",
		"tool_arguments",
		"provider_error_body",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatal("completion log reflected a forbidden sensitive input")
		}
	}
}
