package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	infraSmokeLogRoute   = "/api/v1/observability/infra-smoke"
	infraSmokeLogMarker  = "run-t041"
	infraSmokeLogRequest = "req-t041"
)

func TestHTTPCompletionLoggingMiddlewareWritesInfraSmokeCompletion(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		body           string
		wantLevel      string
		wantMessage    string
		wantErrorClass string
	}{
		{name: "success", statusCode: http.StatusOK, body: "synthetic-t041-private-output", wantLevel: "info", wantMessage: "http request completed"},
		{name: "internal error", statusCode: http.StatusInternalServerError, body: "synthetic-t041-provider-error", wantLevel: "error", wantMessage: "http request failed", wantErrorClass: "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &httpCompletionLogWriterStub{}
			spanExporter := tracetest.NewInMemoryExporter()
			provider := trace.NewTracerProvider(trace.WithSyncer(spanExporter))
			t.Cleanup(func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Errorf("TracerProvider.Shutdown() error = %v", err)
				}
			})
			clock := newHTTPCompletionLogClock()
			middleware := NewHTTPCompletionLoggingMiddleware(HTTPLoggingDependencies{
				Tracer:           provider.Tracer("t041-http-log"),
				CompletionLogger: writer,
				Identify: func(*http.Request) HTTPRequestIdentity {
					return HTTPRequestIdentity{RequestID: infraSmokeLogRequest, RouteTemplate: infraSmokeLogRoute, IsSmokeRun: true, SmokeRunID: infraSmokeLogMarker}
				},
				Now: clock.Now,
			})
			handler := middleware(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil || string(body) != "synthetic-t041-private-prompt" {
					t.Fatalf("logging middleware consumed or changed business request body: body=%q err=%v", body, err)
				}
				response.WriteHeader(tt.statusCode)
				_, _ = response.Write([]byte(tt.body))
			}))
			request := httptest.NewRequest(http.MethodGet, infraSmokeLogRoute+"?token=synthetic-t041-query-secret", strings.NewReader("synthetic-t041-private-prompt"))
			request.Header.Set("Authorization", "Bearer synthetic-t041-authorization")
			request.Header.Set("X-API-Key", "sk-t041-synthetic-key")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)
			if response.Code != tt.statusCode || response.Body.String() != tt.body {
				t.Fatalf("middleware changed HTTP response: status=%d body=%q", response.Code, response.Body.String())
			}
			assertInfraSmokeHTTPCompletion(t, writer.entries, spanExporter.GetSpans(), tt.statusCode, tt.wantLevel, tt.wantMessage, tt.wantErrorClass)
		})
	}
}

func TestHTTPCompletionLoggingMiddlewareWritesAuthenticatedChatSmokeMarker(t *testing.T) {
	const marker = "run-t177-http-chat"
	const credential = "t177-forbidden-admission-secret"
	writer := &httpCompletionLogWriterStub{}
	spanExporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(spanExporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	middleware := NewHTTPCompletionLoggingMiddleware(HTTPLoggingDependencies{
		Tracer: provider.Tracer("t177-http-chat"), CompletionLogger: writer,
		Identify: func(*http.Request) HTTPRequestIdentity {
			return HTTPRequestIdentity{RequestID: "req-t177-http-chat", RouteTemplate: "/api/v1/chat", IsAIRequest: true, AITraceID: "ai-t177-http-chat", IsSmokeRun: true, SmokeRunID: marker}
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/chat", nil)
	request.Header.Set("X-Observability-Smoke-Authorization", credential)
	response := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })).ServeHTTP(response, request)

	if len(writer.entries) != 1 || writer.entries[0].SmokeRunID != marker || len(spanExporter.GetSpans()) != 1 {
		t.Fatalf("chat completion = logs:%#v spans:%d", writer.entries, len(spanExporter.GetSpans()))
	}
	attributes := attributesByKey(spanExporter.GetSpans()[0].Attributes)
	if got := attributes["longtermism.smoke.run_id"].AsString(); got != marker {
		t.Fatalf("HTTP root smoke marker = %q, want %q", got, marker)
	}
	encoded, err := json.Marshal(writer.entries[0])
	if err != nil {
		t.Fatalf("marshal completion log: %v", err)
	}
	if strings.Contains(string(encoded), credential) || strings.Contains(fmt.Sprintf("%+v", spanExporter.GetSpans()[0]), credential) {
		t.Fatal("chat completion telemetry leaked the admission secret")
	}
}

func TestHTTPCompletionLoggingMiddlewareDoesNotBlockResponsesWhenWriterFails(t *testing.T) {
	writer := &httpCompletionLogWriterStub{err: errors.New("synthetic-t041-log-file-unavailable")}
	diagnostics := &httpCompletionLogDiagnosticsStub{}
	middleware := NewHTTPCompletionLoggingMiddleware(HTTPLoggingDependencies{
		CompletionLogger: writer,
		Diagnostics:      diagnostics,
		Identify: func(*http.Request) HTTPRequestIdentity {
			return HTTPRequestIdentity{RequestID: infraSmokeLogRequest, RouteTemplate: infraSmokeLogRoute, IsSmokeRun: true, SmokeRunID: infraSmokeLogMarker}
		},
	})
	handler := middleware(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("business-response"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, infraSmokeLogRoute, nil))

	if response.Code != http.StatusOK || response.Body.String() != "business-response" || writer.calls != 1 {
		t.Fatalf("log writer failure changed business response: status=%d body=%q calls=%d", response.Code, response.Body.String(), writer.calls)
	}
	assertHTTPCompletionWriterFailureDiagnostic(t, diagnostics.failures)
}

func TestHTTPCompletionLoggingMiddlewareRejectsRawRouteFallback(t *testing.T) {
	writer := &httpCompletionLogWriterStub{}
	spanExporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(spanExporter))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("TracerProvider.Shutdown() error = %v", err)
		}
	})
	middleware := NewHTTPCompletionLoggingMiddleware(HTTPLoggingDependencies{
		Tracer:           provider.Tracer("t041-http-log"),
		CompletionLogger: writer,
		Identify: func(*http.Request) HTTPRequestIdentity {
			return HTTPRequestIdentity{RequestID: infraSmokeLogRequest, IsSmokeRun: true, SmokeRunID: infraSmokeLogMarker}
		},
	})
	handler := middleware(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, infraSmokeLogRoute+"/synthetic-t041-route-secret", nil))

	if response.Code != http.StatusOK || len(writer.entries) != 0 || len(spanExporter.GetSpans()) != 0 {
		t.Fatalf("raw route fallback changed response or wrote unsafe telemetry: status=%d entries=%d spans=%d", response.Code, len(writer.entries), len(spanExporter.GetSpans()))
	}
}

// HTTP 的首个 WriteHeader 才是实际发送给客户端的状态码。重复调用时若覆盖该值，
// 完成日志会与真实响应不一致，排障时会得到错误的失败分类。
func TestHTTPCompletionLoggingMiddlewareKeepsFirstResponseStatus(t *testing.T) {
	tests := []struct {
		name           string
		writeBodyFirst bool
		firstStatus    int
		secondStatus   int
		wantStatus     int
	}{
		{name: "first explicit status remains authoritative", firstStatus: http.StatusAccepted, secondStatus: http.StatusInternalServerError, wantStatus: http.StatusAccepted},
		{name: "implicit success remains authoritative", writeBodyFirst: true, secondStatus: http.StatusInternalServerError, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &httpCompletionLogWriterStub{}
			middleware := NewHTTPCompletionLoggingMiddleware(HTTPLoggingDependencies{
				CompletionLogger: writer,
				Identify: func(*http.Request) HTTPRequestIdentity {
					return HTTPRequestIdentity{RequestID: infraSmokeLogRequest, RouteTemplate: infraSmokeLogRoute}
				},
			})
			handler := middleware(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if tt.writeBodyFirst {
					_, _ = response.Write([]byte("implicit success"))
					response.WriteHeader(tt.secondStatus)
					return
				}
				response.WriteHeader(tt.firstStatus)
				response.WriteHeader(tt.secondStatus)
			}))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, infraSmokeLogRoute, nil))

			if response.Code != tt.wantStatus || len(writer.entries) != 1 || writer.entries[0].Status != tt.wantStatus {
				t.Fatalf("completion status = response:%d log:%#v, want authoritative status %d", response.Code, writer.entries, tt.wantStatus)
			}
		})
	}
}

// /api/v1/chat 将来会使用 SSE；完成日志 hook 必须保留 Flush，不能因为包装
// ResponseWriter 让业务 handler 的流式能力消失。
func TestHTTPCompletionLoggingMiddlewarePreservesFlusher(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "forwards flush to downstream writer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &httpCompletionLogWriterStub{}
			middleware := NewHTTPCompletionLoggingMiddleware(HTTPLoggingDependencies{
				CompletionLogger: writer,
				Identify: func(*http.Request) HTTPRequestIdentity {
					return HTTPRequestIdentity{RequestID: infraSmokeLogRequest, RouteTemplate: infraSmokeLogRoute}
				},
			})
			flushingResponse := &httpCompletionFlushingResponseWriter{ResponseWriter: httptest.NewRecorder()}
			handler := middleware(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				flusher, ok := response.(http.Flusher)
				if !ok {
					t.Fatal("completion logging middleware removed http.Flusher")
				}
				flusher.Flush()
			}))

			handler.ServeHTTP(flushingResponse, httptest.NewRequest(http.MethodGet, infraSmokeLogRoute, nil))

			if flushingResponse.flushCalls != 1 {
				t.Fatalf("downstream flush calls = %d, want 1", flushingResponse.flushCalls)
			}
		})
	}
}

// 这个断言要求 hook 自己创建真实 span 并在 HTTP 完成后记录其 native identity；
// 不能接受调用方传入任意 trace/span 字符串，否则日志到 Tempo 的关联不可信。
func assertInfraSmokeHTTPCompletion(t *testing.T, entries []HTTPCompletionLog, spans []tracetest.SpanStub, wantStatusCode int, wantLevel, wantMessage, wantErrorClass string) {
	t.Helper()
	if len(entries) != 1 || len(spans) != 1 {
		t.Fatalf("completion signals = logs:%d spans:%d, want one of each", len(entries), len(spans))
	}
	encoded, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatalf("marshal completion log: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode completion JSONL: %v", err)
	}
	spanContext := spans[0].SpanContext
	if string(fields["trace_id"]) != `"`+spanContext.TraceID().String()+`"` || string(fields["span_id"]) != `"`+spanContext.SpanID().String()+`"` {
		t.Fatalf("completion log identity = %s, want native SpanContext", encoded)
	}
	if string(fields["route"]) != `"`+infraSmokeLogRoute+`"` || string(fields["method"]) != `"GET"` || string(fields["request_id"]) != `"`+infraSmokeLogRequest+`"` || string(fields["smoke_run_id"]) != `"`+infraSmokeLogMarker+`"` {
		t.Fatalf("infra-smoke completion fields = %s, want trusted route/method/request/smoke identities", encoded)
	}
	if string(fields["timestamp"]) != `"2026-07-17T09:30:00Z"` || string(fields["status"]) != fmt.Sprintf("%d", wantStatusCode) || string(fields["duration_ms"]) != "125" {
		t.Fatalf("infra-smoke completion timing = %s, want fixed timestamp/status/duration", encoded)
	}
	if string(fields["level"]) != `"`+wantLevel+`"` || string(fields["message"]) != `"`+wantMessage+`"` || string(fields["error_class"]) != jsonStringOrEmpty(wantErrorClass) || fields["ai_trace_id"] != nil {
		t.Fatalf("infra-smoke completion classification = %s, want stable error class and no AI identity", encoded)
	}
	assertHTTPCompletionHookDoesNotLeakSensitiveInput(t, string(encoded))
	assertInfraSmokeLogSpan(t, spans[0], wantStatusCode)
}

func assertInfraSmokeLogSpan(t *testing.T, span tracetest.SpanStub, wantStatusCode int) {
	t.Helper()
	if span.Name != "HTTP GET "+infraSmokeLogRoute {
		t.Fatalf("HTTP completion span name = %q, want trusted infra-smoke route", span.Name)
	}
	attributes := attributesByKey(span.Attributes)
	for key, want := range map[string]string{
		"http.request.method":      "GET",
		"http.route":               infraSmokeLogRoute,
		"request.id":               infraSmokeLogRequest,
		"longtermism.smoke.run_id": infraSmokeLogMarker,
	} {
		if attributes[key].AsString() != want {
			t.Fatalf("HTTP completion span attribute %q = %q, want %q", key, attributes[key].AsString(), want)
		}
	}
	if attributes["http.response.status_code"].AsInt64() != int64(wantStatusCode) {
		t.Fatalf("HTTP completion span status = %d, want %d", attributes["http.response.status_code"].AsInt64(), wantStatusCode)
	}
	if len(attributes) != 5 {
		t.Fatalf("HTTP completion span attributes = %#v, want HTTP identity allowlist only", attributes)
	}
	assertHTTPCompletionHookDoesNotLeakSensitiveInput(t, fmt.Sprintf("%+v", span))
}

func assertHTTPCompletionWriterFailureDiagnostic(t *testing.T, failures []HTTPLoggingFailure) {
	t.Helper()
	if len(failures) != 1 || failures[0].Component != "http_completion_log" || failures[0].ErrorClass != "telemetry_export_failed" {
		t.Fatalf("writer failure diagnostics = %#v, want one stable low-sensitive failure", failures)
	}
	typeOfFailure := reflect.TypeFor[HTTPLoggingFailure]()
	if typeOfFailure.Kind() != reflect.Struct || typeOfFailure.NumField() != 2 {
		t.Fatalf("HTTPLoggingFailure must have only stable component/error class fields, got %v", typeOfFailure)
	}
	for index, want := range []string{"Component", "ErrorClass"} {
		field := typeOfFailure.Field(index)
		if field.Name != want || !field.IsExported() || field.Type.Kind() != reflect.String {
			t.Fatalf("HTTPLoggingFailure field %d = %#v, want exported string %q", index, field, want)
		}
	}
	encoded, err := json.Marshal(failures)
	if err != nil {
		t.Fatalf("marshal writer failure diagnostics: %v", err)
	}
	assertHTTPCompletionHookDoesNotLeakSensitiveInput(t, string(encoded))
}

func jsonStringOrEmpty(value string) string {
	if value == "" {
		return ""
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func attributesByKey(values []attribute.KeyValue) map[string]attribute.Value {
	result := make(map[string]attribute.Value, len(values))
	for _, value := range values {
		result[string(value.Key)] = value.Value
	}
	return result
}

func assertHTTPCompletionHookDoesNotLeakSensitiveInput(t *testing.T, rendered string) {
	t.Helper()
	for _, forbidden := range []string{
		"synthetic-t041-query-secret",
		"synthetic-t041-authorization",
		"sk-t041-synthetic-key",
		"synthetic-t041-private-prompt",
		"synthetic-t041-private-output",
		"synthetic-t041-provider-error",
		"synthetic-t041-log-file-unavailable",
		"synthetic-t041-route-secret",
		"Authorization",
		"api_key",
		"raw_query",
		"provider_error_body",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("completion hook leaked forbidden input %q", forbidden)
		}
	}
}

type httpCompletionLogWriterStub struct {
	entries []HTTPCompletionLog
	err     error
	calls   int
}

type httpCompletionFlushingResponseWriter struct {
	http.ResponseWriter
	flushCalls int
}

func (w *httpCompletionFlushingResponseWriter) Flush() {
	w.flushCalls++
}

func (w *httpCompletionLogWriterStub) Write(_ context.Context, entry HTTPCompletionLog) error {
	w.calls++
	w.entries = append(w.entries, entry)
	return w.err
}

type httpCompletionLogDiagnosticsStub struct {
	failures []HTTPLoggingFailure
}

func (d *httpCompletionLogDiagnosticsStub) RecordFailure(_ context.Context, failure HTTPLoggingFailure) {
	d.failures = append(d.failures, failure)
}

type httpCompletionLogClock struct {
	calls int
}

func newHTTPCompletionLogClock() *httpCompletionLogClock {
	return &httpCompletionLogClock{}
}

func (c *httpCompletionLogClock) Now() time.Time {
	timestamp := time.Date(2026, time.July, 17, 9, 30, 0, 0, time.UTC)
	if c.calls > 0 {
		timestamp = timestamp.Add(125 * time.Millisecond)
	}
	c.calls++
	return timestamp
}
