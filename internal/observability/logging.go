package observability

import (
	"errors"
	"strings"
	"time"

	traceapi "go.opentelemetry.io/otel/trace"
)

var (
	errMissingStableHTTPErrorClass = errors.New("failed HTTP completion log requires stable error class")
	errUntrustedHTTPRoute          = errors.New("HTTP completion log route is not a trusted template")
	errUntrustedHTTPErrorClass     = errors.New("HTTP completion log error class is not stable")
	errInvalidHTTPCompletionOTLP   = errors.New("HTTP completion OTLP record is invalid")
)

var (
	trustedHTTPRouteTemplates = stringSet(
		"/api/v1/chat",
		"/api/v1/health/ping",
		"/api/v1/observability/infra-smoke",
	)
	trustedHTTPErrorClasses = stringSet(
		"upstream_unavailable",
		"upstream_timeout",
		"rate_limited",
		"request_validation_failed",
		"internal_error",
	)
)

// HTTPCompletionLogInput separates the wide HTTP boundary from the narrow log contract.
// Sensitive fields exist so callers can hand over their full boundary object without tempting
// later code to serialize it. BuildHTTPCompletionLog deliberately never reads those fields.
type HTTPCompletionLogInput struct {
	Timestamp     time.Time
	RequestID     string
	TraceID       string
	SpanID        string
	RouteTemplate string
	Method        string
	StatusCode    int
	Duration      time.Duration
	ErrorClass    string
	IsAIRequest   bool
	IsSmokeRun    bool
	AITraceID     string
	SmokeRunID    string

	Authorization      string
	APIKey             string
	RawQuery           string
	Prompt             string
	Output             string
	ToolArguments      string
	ProviderErrorBody  string
	RecognizedPII      string
	EndpointCredential string
}

// HTTPCompletionLog is the JSONL payload sent through GoFrame glog and later collected by
// filelog. Its fields are an explicit allowlist: traces and logs need correlation facts, but
// must not become a second storage path for prompts, provider bodies, or credentials.
type HTTPCompletionLog struct {
	Timestamp  string `json:"timestamp"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	RequestID  string `json:"request_id"`
	TraceID    string `json:"trace_id"`
	SpanID     string `json:"span_id"`
	Route      string `json:"route"`
	Method     string `json:"method"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`

	ErrorClass string `json:"error_class,omitempty"`
	AITraceID  string `json:"ai_trace_id,omitempty"`
	SmokeRunID string `json:"smoke_run_id,omitempty"`
}

// HTTPCompletionOTLPRecord 是 SDK 无关、可安全交给 OTel Logger 的不可变投影。
// 保留这个窄值对象可以让 privacy contract 不依赖实验期 Logs SDK 的具体 Record 形状。
type HTTPCompletionOTLPRecord struct {
	Timestamp  time.Time
	Severity   string
	Body       string
	Attributes map[string]any
}

// BuildHTTPCompletionOTLPRecord 在 exporter 边界再次验证导出的可变结构体。调用方即使
// 篡改已构造的 HTTPCompletionLog，也不能借合法字段名把自由文本或 credential 送入 Loki。
func BuildHTTPCompletionOTLPRecord(entry HTTPCompletionLog) (HTTPCompletionOTLPRecord, error) {
	timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil || !validCompletionEnvelope(entry) || !validCompletionIdentities(entry) {
		return HTTPCompletionOTLPRecord{}, errInvalidHTTPCompletionOTLP
	}
	attributes := map[string]any{
		"request_id": entry.RequestID, "trace_id": entry.TraceID, "span_id": entry.SpanID,
		"route": entry.Route, "method": entry.Method, "status": int64(entry.Status), "duration_ms": entry.DurationMS,
	}
	if entry.ErrorClass != "" {
		attributes["error_class"] = entry.ErrorClass
	}
	if entry.AITraceID != "" {
		attributes["ai_trace_id"] = entry.AITraceID
	}
	if entry.SmokeRunID != "" {
		attributes["smoke_run_id"] = entry.SmokeRunID
	}
	return HTTPCompletionOTLPRecord{Timestamp: timestamp.UTC(), Severity: strings.ToUpper(entry.Level), Body: entry.Message, Attributes: attributes}, nil
}

func validCompletionEnvelope(entry HTTPCompletionLog) bool {
	failed := entry.Status >= 400
	if entry.Status < 100 || entry.Status > 599 || entry.DurationMS < 0 || !containsString(trustedHTTPRouteTemplates, entry.Route) {
		return false
	}
	if entry.Method != canonicalHTTPMethod(entry.Method) || entry.Level != completionLogLevel(failed) || entry.Message != completionLogMessage(failed) {
		return false
	}
	if failed {
		return containsString(trustedHTTPErrorClasses, entry.ErrorClass)
	}
	return entry.ErrorClass == ""
}

func validCompletionIdentities(entry HTTPCompletionLog) bool {
	if entry.TraceID == "" && entry.SpanID == "" {
		return safeCompletionRequestID(entry.RequestID) && (entry.AITraceID == "" || safeCompletionIdentity(entry.AITraceID)) && (entry.SmokeRunID == "" || safeCompletionIdentity(entry.SmokeRunID))
	}
	traceID, traceErr := traceapi.TraceIDFromHex(entry.TraceID)
	spanID, spanErr := traceapi.SpanIDFromHex(entry.SpanID)
	if traceErr != nil || spanErr != nil || !traceID.IsValid() || !spanID.IsValid() || !safeCompletionRequestID(entry.RequestID) {
		return false
	}
	return (entry.AITraceID == "" || safeCompletionIdentity(entry.AITraceID)) && (entry.SmokeRunID == "" || safeCompletionIdentity(entry.SmokeRunID))
}

func safeCompletionIdentity(value string) bool {
	if !safeCompletionRequestID(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"authorization", "bearer", "credential", "payload", "secret"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

// request_id 由 HTTP transport 创建或校验；OTLP 投影必须接受与该边界完全相同的
// opaque-ID 语言，不能在出口悄悄丢弃已经合法接纳的请求事实。
func safeCompletionRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !completionIdentityAlphanumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if completionIdentityAlphanumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func completionIdentityAlphanumeric(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

// BuildHTTPCompletionLog projects one completed HTTP request into GoFrame's structured JSON
// fields. Actual glog writing stays at the HTTP hook (T053), so this pure constructor remains
// deterministic and can be tested without touching global logger configuration.
func BuildHTTPCompletionLog(input HTTPCompletionLogInput) (HTTPCompletionLog, error) {
	if !containsString(trustedHTTPRouteTemplates, input.RouteTemplate) {
		return HTTPCompletionLog{}, errUntrustedHTTPRoute
	}
	isFailed := input.StatusCode >= 400
	if isFailed && input.ErrorClass == "" {
		return HTTPCompletionLog{}, errMissingStableHTTPErrorClass
	}
	if isFailed && !containsString(trustedHTTPErrorClasses, input.ErrorClass) {
		return HTTPCompletionLog{}, errUntrustedHTTPErrorClass
	}

	entry := HTTPCompletionLog{
		Timestamp:  input.Timestamp.UTC().Format(time.RFC3339Nano),
		Level:      completionLogLevel(isFailed),
		Message:    completionLogMessage(isFailed),
		RequestID:  input.RequestID,
		TraceID:    input.TraceID,
		SpanID:     input.SpanID,
		Route:      input.RouteTemplate,
		Method:     input.Method,
		Status:     input.StatusCode,
		DurationMS: input.Duration.Milliseconds(),
	}
	if isFailed {
		entry.ErrorClass = input.ErrorClass
	}
	if input.IsAIRequest {
		entry.AITraceID = input.AITraceID
	}
	if input.IsSmokeRun {
		entry.SmokeRunID = input.SmokeRunID
	}
	return entry, nil
}

func completionLogLevel(isFailed bool) string {
	if isFailed {
		return "error"
	}
	return "info"
}

func completionLogMessage(isFailed bool) string {
	if isFailed {
		return "http request failed"
	}
	return "http request completed"
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func containsString(values map[string]struct{}, value string) bool {
	_, exists := values[value]
	return exists
}
