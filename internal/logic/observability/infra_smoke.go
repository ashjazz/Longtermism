// Package observability contains pure application usecases for infrastructure observability.
package observability

import (
	"context"
	"time"

	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	traceapi "go.opentelemetry.io/otel/trace"
)

const (
	InfraSmokeStatusOK              = "ok"
	infraSmokeRouteTemplate         = "/api/v1/observability/infra-smoke"
	infraSmokeTelemetryFailureClass = "telemetry_export_failed"
)

// InfraSmokeInput carries only low-sensitive correlation facts. The controller owns HTTP
// validation; this usecase must not inspect headers, query parameters, or request bodies.
type InfraSmokeInput struct {
	RequestID  string
	SmokeRunID string
}

type InfraSmokeResult struct {
	Status string
}

// InfraSmokeRunner lets the HTTP controller depend on this usecase's narrow behavior rather
// than its concrete observability dependencies. It keeps the controller free of OTel details.
type InfraSmokeRunner interface {
	Run(context.Context, InfraSmokeInput) (InfraSmokeResult, error)
}

// HTTPMetrics is the narrow application port required by the smoke usecase. It intentionally
// omits backend details so the usecase cannot query or configure observability platforms.
type HTTPMetrics interface {
	RecordHTTP(context.Context, appobservability.HTTPMetric) error
}

// HTTPCompletionLogWriter writes the already-redacted completion-log projection.
type HTTPCompletionLogWriter interface {
	Write(context.Context, appobservability.HTTPCompletionLog) error
}

// InfraSmokeTelemetryFailure is deliberately a low-sensitive diagnostic. Exporter errors can
// contain endpoints or credentials, so diagnostics retain only a stable component and class.
type InfraSmokeTelemetryFailure struct {
	Component  string
	ErrorClass string
}

type InfraSmokeTelemetryDiagnostics interface {
	RecordTelemetryFailure(context.Context, InfraSmokeTelemetryFailure)
}

type InfraSmokeUsecaseDependencies struct {
	Tracer           traceapi.Tracer
	Metrics          HTTPMetrics
	MetricFlusher    interface{ Flush(context.Context) error }
	CompletionLogger HTTPCompletionLogWriter
	Diagnostics      InfraSmokeTelemetryDiagnostics
	Now              func() time.Time
}

type InfraSmokeUsecase struct {
	tracer           traceapi.Tracer
	metrics          HTTPMetrics
	metricFlusher    interface{ Flush(context.Context) error }
	completionLogger HTTPCompletionLogWriter
	diagnostics      InfraSmokeTelemetryDiagnostics
	now              func() time.Time
}

func NewInfraSmokeUsecase(dependencies InfraSmokeUsecaseDependencies) *InfraSmokeUsecase {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	return &InfraSmokeUsecase{
		tracer:           dependencies.Tracer,
		metrics:          dependencies.Metrics,
		metricFlusher:    dependencies.MetricFlusher,
		completionLogger: dependencies.CompletionLogger,
		diagnostics:      dependencies.Diagnostics,
		now:              now,
	}
}

// Run emits exactly infrastructure-plane facts for a successful smoke probe. Observability
// failure must never make the probe's business result ambiguous: callers still receive `ok`,
// while a separate low-sensitive diagnostic preserves the delivery failure for operations.
func (u *InfraSmokeUsecase) Run(ctx context.Context, input InfraSmokeInput) (InfraSmokeResult, error) {
	startedAt := u.now()
	var traceID, spanID string
	if u.tracer != nil {
		tracedContext, span := u.tracer.Start(ctx, "HTTP GET "+infraSmokeRouteTemplate)
		ctx = tracedContext
		span.SetAttributes(infraSmokeSpanAttributes(input)...)
		spanContext := span.SpanContext()
		traceID = spanContext.TraceID().String()
		spanID = spanContext.SpanID().String()
		defer span.End()
	}

	completedAt := u.now()
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}

	u.recordMetric(ctx, input, duration, traceID, spanID)
	u.flushMetrics(ctx)
	u.recordCompletionLog(ctx, input, startedAt, duration, traceID, spanID)
	return InfraSmokeResult{Status: InfraSmokeStatusOK}, nil
}

// flushMetrics bounds the smoke-only export latency. Regular application metrics retain their
// periodic batching; this explicit probe must make its one counter visible before its bounded
// Prometheus query window closes.
func (u *InfraSmokeUsecase) flushMetrics(ctx context.Context) {
	if u.metricFlusher == nil {
		return
	}
	if err := u.metricFlusher.Flush(ctx); err != nil {
		u.recordFailure(ctx, "metrics_flush")
	}
}

func infraSmokeSpanAttributes(input InfraSmokeInput) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("request.id", input.RequestID),
		attribute.String("longtermism.smoke.run_id", input.SmokeRunID),
		attribute.String("http.request.method", "GET"),
		attribute.String("http.route", infraSmokeRouteTemplate),
		attribute.Int("http.response.status_code", 200),
	}
}

func (u *InfraSmokeUsecase) recordMetric(ctx context.Context, input InfraSmokeInput, duration time.Duration, traceID, spanID string) {
	if u.metrics == nil {
		return
	}
	if err := u.metrics.RecordHTTP(ctx, appobservability.HTTPMetric{
		RouteTemplate: infraSmokeRouteTemplate,
		Method:        "GET",
		StatusCode:    200,
		Duration:      duration,
		RequestID:     input.RequestID,
		TraceID:       traceID,
		SpanID:        spanID,
		SmokeRunID:    input.SmokeRunID,
	}); err != nil {
		u.recordFailure(ctx, "metrics")
	}
}

func (u *InfraSmokeUsecase) recordCompletionLog(ctx context.Context, input InfraSmokeInput, timestamp time.Time, duration time.Duration, traceID, spanID string) {
	if u.completionLogger == nil {
		return
	}
	entry, err := appobservability.BuildHTTPCompletionLog(appobservability.HTTPCompletionLogInput{
		Timestamp:     timestamp,
		RequestID:     input.RequestID,
		TraceID:       traceID,
		SpanID:        spanID,
		RouteTemplate: infraSmokeRouteTemplate,
		Method:        "GET",
		StatusCode:    200,
		Duration:      duration,
		IsSmokeRun:    true,
		SmokeRunID:    input.SmokeRunID,
	})
	if err == nil {
		err = u.completionLogger.Write(ctx, entry)
	}
	if err != nil {
		u.recordFailure(ctx, "completion_log")
	}
}

func (u *InfraSmokeUsecase) recordFailure(ctx context.Context, component string) {
	if u.diagnostics == nil {
		return
	}
	u.diagnostics.RecordTelemetryFailure(ctx, InfraSmokeTelemetryFailure{
		Component:  component,
		ErrorClass: infraSmokeTelemetryFailureClass,
	})
}
