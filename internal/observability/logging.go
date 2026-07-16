package observability

import (
	"errors"
	"time"
)

var (
	errMissingStableHTTPErrorClass = errors.New("failed HTTP completion log requires stable error class")
	errUntrustedHTTPRoute          = errors.New("HTTP completion log route is not a trusted template")
	errUntrustedHTTPErrorClass     = errors.New("HTTP completion log error class is not stable")
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
