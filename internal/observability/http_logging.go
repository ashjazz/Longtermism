// Package observability contains application-facing observability adapters.
package observability

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	traceapi "go.opentelemetry.io/otel/trace"
)

const httpCompletionTelemetryFailureClass = "telemetry_export_failed"

// HTTPRequestIdentity is the low-sensitive request context projected into completion signals.
// The HTTP boundary must derive RouteTemplate from router metadata, never from a raw URL path.
type HTTPRequestIdentity struct {
	RequestID     string
	RouteTemplate string
	IsAIRequest   bool
	AITraceID     string
	IsSmokeRun    bool
	SmokeRunID    string
}

// HTTPCompletionLogWriter 是受控 completion fact 出口。生产 composition root 注入 OTLP
// writer；JSONL 仅作为显式启用的本地诊断副本，middleware 本身始终保持 backend-free。
type HTTPCompletionLogWriter interface {
	Write(context.Context, HTTPCompletionLog) error
}

// HTTPLoggingFailure preserves delivery failure evidence without carrying an exporter error,
// request identity, route, prompt, provider response, or credential into another log channel.
type HTTPLoggingFailure struct {
	Component  string
	ErrorClass string
}

type HTTPLoggingDiagnostics interface {
	RecordFailure(context.Context, HTTPLoggingFailure)
}

type HTTPLoggingDependencies struct {
	Tracer           traceapi.Tracer
	CompletionLogger HTTPCompletionLogWriter
	Diagnostics      HTTPLoggingDiagnostics
	Identify         func(*http.Request) HTTPRequestIdentity
	Now              func() time.Time
}

// NewHTTPCompletionLoggingMiddleware records a request only after its business handler has
// completed. It never buffers or reads request/response bodies; sink failures are isolated
// from the caller-visible HTTP payload. The concrete local JSONL sink is wired in T055 and
// emitted asynchronously by the OTel Logs SDK, so this hook never calls or waits for Loki.
func NewHTTPCompletionLoggingMiddleware(dependencies HTTPLoggingDependencies) func(http.Handler) http.Handler {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	identify := dependencies.Identify
	if identify == nil {
		identify = func(*http.Request) HTTPRequestIdentity { return HTTPRequestIdentity{} }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			identity := identify(request)
			if !isTrustedHTTPCompletionIdentity(identity) {
				next.ServeHTTP(response, request)
				return
			}

			// method 是客户端可控的 HTTP token；收敛成有限集合，避免将异常方法
			// 变成 trace/log 的高基数字段或 span name 的自由文本。
			method := canonicalHTTPMethod(request.Method)
			startedAt := now()
			contextForRequest := request.Context()
			var span traceapi.Span
			if dependencies.Tracer != nil {
				contextForRequest, span = dependencies.Tracer.Start(contextForRequest, "HTTP "+method+" "+identity.RouteTemplate)
				request = request.WithContext(contextForRequest)
			}
			statusWriter := &httpCompletionStatusWriter{ResponseWriter: response, statusCode: http.StatusOK}
			next.ServeHTTP(statusWriter, request)

			completedAt := now()
			duration := completedAt.Sub(startedAt)
			if duration < 0 {
				duration = 0
			}
			if span != nil {
				span.SetAttributes(httpCompletionSpanAttributes(identity, method, statusWriter.statusCode)...)
				span.End()
			}
			writeHTTPCompletion(contextForRequest, dependencies, identity, method, statusWriter.statusCode, duration, startedAt, span)
		})
	}
}

func isTrustedHTTPCompletionIdentity(identity HTTPRequestIdentity) bool {
	return containsString(trustedHTTPRouteTemplates, identity.RouteTemplate)
}

func canonicalHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func httpCompletionSpanAttributes(identity HTTPRequestIdentity, method string, statusCode int) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("http.request.method", method),
		attribute.String("http.route", identity.RouteTemplate),
		attribute.String("request.id", identity.RequestID),
		attribute.String("longtermism.smoke.run_id", identity.SmokeRunID),
		attribute.Int("http.response.status_code", statusCode),
	}
}

func writeHTTPCompletion(ctx context.Context, dependencies HTTPLoggingDependencies, identity HTTPRequestIdentity, method string, statusCode int, duration time.Duration, timestamp time.Time, span traceapi.Span) {
	if dependencies.CompletionLogger == nil {
		return
	}
	traceID, spanID := "", ""
	if span != nil {
		spanContext := span.SpanContext()
		traceID = spanContext.TraceID().String()
		spanID = spanContext.SpanID().String()
	}
	entry, err := BuildHTTPCompletionLog(HTTPCompletionLogInput{
		Timestamp:     timestamp,
		RequestID:     identity.RequestID,
		TraceID:       traceID,
		SpanID:        spanID,
		RouteTemplate: identity.RouteTemplate,
		Method:        method,
		StatusCode:    statusCode,
		Duration:      duration,
		ErrorClass:    httpCompletionErrorClass(statusCode),
		IsAIRequest:   identity.IsAIRequest,
		AITraceID:     identity.AITraceID,
		IsSmokeRun:    identity.IsSmokeRun,
		SmokeRunID:    identity.SmokeRunID,
	})
	if err == nil {
		err = dependencies.CompletionLogger.Write(ctx, entry)
	}
	if err != nil && dependencies.Diagnostics != nil {
		dependencies.Diagnostics.RecordFailure(ctx, HTTPLoggingFailure{
			Component:  "http_completion_log",
			ErrorClass: httpCompletionTelemetryFailureClass,
		})
	}
}

func httpCompletionErrorClass(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "request_validation_failed"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusBadGateway:
		return "upstream_unavailable"
	case http.StatusGatewayTimeout:
		return "upstream_timeout"
	default:
		if statusCode >= http.StatusBadRequest {
			return "internal_error"
		}
		return ""
	}
}

type httpCompletionStatusWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (w *httpCompletionStatusWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	// net/http 只认第一次 WriteHeader。这里保持同一规则，才能确保记录的
	// status/error_class 与客户端实际收到的响应一致。
	w.wroteHeader = true
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *httpCompletionStatusWriter) Write(data []byte) (int, error) {
	// 第一次 Write 会由 net/http 隐式提交 200。即使业务代码随后误调用
	// WriteHeader(500)，也不能让完成日志与已发送的成功响应发生分叉。
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(data)
}

// Flush preserves streaming handlers such as the future SSE chat endpoint. Calling Flush on
// a non-streaming writer remains a safe no-op, matching Go's optional ResponseWriter pattern.
func (w *httpCompletionStatusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
