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

func TestBuildHTTPCompletionLogRejectsUntrustedRouteAndErrorClass(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HTTPCompletionLogInput)
	}{
		{
			name: "raw route query",
			mutate: func(input *HTTPCompletionLogInput) {
				input.RouteTemplate = "/api/v1/chat?token=synthetic-t030-route-secret"
			},
		},
		{
			name: "raw route path value",
			mutate: func(input *HTTPCompletionLogInput) {
				input.RouteTemplate = "/api/v1/chat/synthetic-t030-route-secret"
			},
		},
		{
			name: "provider response as error class",
			mutate: func(input *HTTPCompletionLogInput) {
				input.ErrorClass = "synthetic-t030-provider-secret"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := newHTTPCompletionLogInput(true, false, false)
			test.mutate(&input)

			_, err := BuildHTTPCompletionLog(input)
			if err == nil {
				t.Fatal("BuildHTTPCompletionLog() error = nil, want untrusted log field rejected")
			}
			assertHTTPCompletionLogDoesNotContainSensitiveInput(t, err.Error())
			if strings.Contains(err.Error(), "synthetic-t030") {
				t.Fatal("completion log validation error reflected sensitive input")
			}
		})
	}
}

// OTLP logs 与本地 JSONL 复用同一个低敏事实模型，但不能直接序列化整个领域对象。
// 这个契约先固定 SDK 无关的 record 投影；T173 再负责把它交给 OTel LoggerProvider。
func TestBuildHTTPCompletionOTLPRecordUsesExactAllowlist(t *testing.T) {
	tests := []struct {
		name       string
		input      HTTPCompletionLogInput
		severity   string
		body       string
		attributes map[string]any
	}{
		{
			name:  "ordinary completion excludes conditional identities",
			input: newHTTPCompletionLogInput(false, false, false), severity: "INFO", body: "http request completed",
			attributes: map[string]any{
				"request_id": "req-t018", "trace_id": "0123456789abcdef0123456789abcdef", "span_id": "0123456789abcdef",
				"route": "/api/v1/chat", "method": "POST", "status": int64(200), "duration_ms": int64(120),
			},
		},
		{
			name:  "failed AI smoke completion retains stable structured metadata",
			input: newHTTPCompletionLogInput(true, true, true), severity: "ERROR", body: "http request failed",
			attributes: map[string]any{
				"request_id": "req-t018", "trace_id": "0123456789abcdef0123456789abcdef", "span_id": "0123456789abcdef",
				"route": "/api/v1/chat", "method": "POST", "status": int64(502), "duration_ms": int64(120),
				"error_class": "upstream_unavailable", "ai_trace_id": "ai-t018", "smoke_run_id": "smoke-t018",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := BuildHTTPCompletionLog(tt.input)
			if err != nil {
				t.Fatalf("BuildHTTPCompletionLog() error = %v", err)
			}
			record, err := BuildHTTPCompletionOTLPRecord(entry)
			if err != nil {
				t.Fatalf("BuildHTTPCompletionOTLPRecord() error = %v", err)
			}
			if !record.Timestamp.Equal(tt.input.Timestamp.UTC()) || record.Severity != tt.severity || record.Body != tt.body {
				t.Fatalf("OTLP record envelope = %#v, want UTC timestamp, severity %q and stable body %q", record, tt.severity, tt.body)
			}
			recordType := reflect.TypeOf(record)
			if recordType.Kind() != reflect.Struct || recordType.NumField() != 4 {
				t.Fatalf("HTTPCompletionOTLPRecord fields = %v, want timestamp/severity/body/attributes only", recordType)
			}
			for index, name := range []string{"Timestamp", "Severity", "Body", "Attributes"} {
				if field := recordType.Field(index); field.Name != name || !field.IsExported() {
					t.Fatalf("HTTPCompletionOTLPRecord field %d = %#v, want exported %s", index, field, name)
				}
			}
			if !reflect.DeepEqual(record.Attributes, tt.attributes) {
				t.Fatalf("OTLP record attributes = %#v, want exact low-sensitive allowlist %#v", record.Attributes, tt.attributes)
			}
			if _, duplicated := record.Attributes["message"]; duplicated || strings.Contains(record.Body, "smoke-t018") {
				t.Fatal("OTLP body duplicated structured metadata")
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("marshal OTLP-safe record: %v", err)
			}
			assertHTTPCompletionLogDoesNotContainSensitiveInput(t, string(encoded))

			// 返回值必须拥有自己的 attribute map，避免 exporter 或测试调用方修改后污染后续记录。
			record.Attributes["route"] = "mutated"
			fresh, err := BuildHTTPCompletionOTLPRecord(entry)
			if err != nil || fresh.Attributes["route"] != "/api/v1/chat" {
				t.Fatal("OTLP record projection retained mutable attribute state")
			}
		})
	}
}

// Allowlist 也必须约束值。否则上游身份边界一旦失守，攻击者仍可借合法属性名把
// credential、换行或自由文本送入 OTLP/Loki，绕过“字段名安全”的表面保护。
func TestBuildHTTPCompletionOTLPRecordRejectsUnsafeIdentityValuesWithoutEcho(t *testing.T) {
	identityFields := []struct {
		name string
		set  func(*HTTPCompletionLog, string)
	}{
		{name: "request_id", set: func(entry *HTTPCompletionLog, value string) { entry.RequestID = value }},
		{name: "ai_trace_id", set: func(entry *HTTPCompletionLog, value string) { entry.AITraceID = value }},
		{name: "smoke_run_id", set: func(entry *HTTPCompletionLog, value string) { entry.SmokeRunID = value }},
	}
	unsafeValues := []struct{ name, value string }{
		{name: "control characters", value: "identity\nsecret"},
		{name: "credential text", value: "Bearer-synthetic-t170-secret"},
		{name: "free text", value: "identity synthetic-t170-secret"},
	}
	for _, field := range identityFields {
		for _, unsafe := range unsafeValues {
			// request_id 的唯一事实边界是 transport opaque-ID grammar；不能在日志出口
			// 因业务上合法的单词子串而丢弃已经接纳的 completion fact。
			if field.name == "request_id" && unsafe.name == "credential text" {
				continue
			}
			t.Run(field.name+" rejects "+unsafe.name, func(t *testing.T) {
				entry, err := BuildHTTPCompletionLog(newHTTPCompletionLogInput(false, true, true))
				if err != nil {
					t.Fatalf("BuildHTTPCompletionLog() error = %v", err)
				}
				field.set(&entry, unsafe.value)
				assertUnsafeOTLPRecordRejected(t, entry)
			})
		}
	}

	tests := []struct {
		name   string
		mutate func(*HTTPCompletionLog)
	}{
		{name: "trace identity must be fixed hex", mutate: func(entry *HTTPCompletionLog) { entry.TraceID = "synthetic-t170-secret" }},
		{name: "span identity must be fixed hex", mutate: func(entry *HTTPCompletionLog) { entry.SpanID = "not-otel-hex" }},
		{name: "stable body cannot be replaced", mutate: func(entry *HTTPCompletionLog) { entry.Message = "synthetic-t170-secret" }},
		{name: "severity cannot be replaced", mutate: func(entry *HTTPCompletionLog) { entry.Level = "synthetic-t170-secret" }},
		{name: "method cannot be replaced", mutate: func(entry *HTTPCompletionLog) { entry.Method = "synthetic-t170-secret" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := BuildHTTPCompletionLog(newHTTPCompletionLogInput(false, true, true))
			if err != nil {
				t.Fatalf("BuildHTTPCompletionLog() error = %v", err)
			}
			tt.mutate(&entry)
			assertUnsafeOTLPRecordRejected(t, entry)
		})
	}
}

func TestBuildHTTPCompletionOTLPRecordAcceptsTransportValidRequestIDs(t *testing.T) {
	for _, requestID := range []string{"a", "req.1", "token-abc", "authorization-1", "req-secret-1"} {
		t.Run(requestID, func(t *testing.T) {
			entry, err := BuildHTTPCompletionLog(newHTTPCompletionLogInput(false, false, false))
			if err != nil {
				t.Fatalf("BuildHTTPCompletionLog() error = %v", err)
			}
			entry.RequestID = requestID
			if _, err := BuildHTTPCompletionOTLPRecord(entry); err != nil {
				t.Fatalf("transport-valid request ID rejected at OTLP boundary: %v", err)
			}
		})
	}
}

func assertUnsafeOTLPRecordRejected(t *testing.T, entry HTTPCompletionLog) {
	t.Helper()
	if _, err := BuildHTTPCompletionOTLPRecord(entry); err == nil {
		t.Fatal("BuildHTTPCompletionOTLPRecord() accepted unsafe record data")
	} else if strings.Contains(err.Error(), "synthetic-t170-secret") || strings.ContainsAny(err.Error(), "\r\n") {
		t.Fatal("OTLP record validation reflected rejected data")
	}
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
