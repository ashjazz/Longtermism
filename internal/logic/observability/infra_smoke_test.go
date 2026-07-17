// Package observability contains pure application usecases for infrastructure observability.
package observability

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const infraSmokeRoute = "/api/v1/observability/infra-smoke"

func TestInfraSmokeUsecaseRecordsOnlyInfrastructureSignals(t *testing.T) {
	spanExporter := tracetest.NewInMemoryExporter()
	tracerProvider := trace.NewTracerProvider(trace.WithSyncer(spanExporter))
	t.Cleanup(func() {
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("TracerProvider.Shutdown() error = %v", err)
		}
	})

	metricReader := metric.NewManualReader()
	meterProvider := metric.NewMeterProvider(metric.WithReader(metricReader))
	t.Cleanup(func() {
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("MeterProvider.Shutdown() error = %v", err)
		}
	})
	metrics, err := appobservability.NewMetrics(
		meterProvider.Meter("github.com/ashjazz/Longtermism/internal/logic/observability"),
		appobservability.WithMetricLabelPolicy(appobservability.MetricLabelPolicy{AllowedRoutes: []string{infraSmokeRoute}}),
	)
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}

	logs := &inMemoryInfraSmokeLogWriter{}
	usecase := NewInfraSmokeUsecase(InfraSmokeUsecaseDependencies{
		Tracer:           tracerProvider.Tracer("t038-infra-smoke"),
		Metrics:          metrics,
		CompletionLogger: logs,
		Now:              func() time.Time { return time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC) },
	})

	result, err := usecase.Run(context.Background(), InfraSmokeInput{
		RequestID:  "req-t038-infra",
		SmokeRunID: "run-t038-infra",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != InfraSmokeStatusOK {
		t.Fatalf("Run() status = %q, want %q", result.Status, InfraSmokeStatusOK)
	}

	spans := spanExporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported span count = %d, want 1", len(spans))
	}
	assertInfraSmokeSpan(t, spans[0], "req-t038-infra", "run-t038-infra")
	assertInfraSmokeLog(t, logs.entries, "req-t038-infra", "run-t038-infra")
	assertInfraSmokeMetricDelta(t, metricReader)
}

func TestInfraSmokeUsecaseKeepsBusinessOKWhenTelemetryFails(t *testing.T) {
	telemetryFailure := errors.New("synthetic telemetry sink unavailable")
	metrics := &failingInfraSmokeMetrics{err: telemetryFailure}
	logs := &failingInfraSmokeLogWriter{err: telemetryFailure}
	diagnostics := &inMemoryInfraSmokeDiagnostics{}
	usecase := NewInfraSmokeUsecase(InfraSmokeUsecaseDependencies{
		Metrics:          metrics,
		CompletionLogger: logs,
		Diagnostics:      diagnostics,
		Now:              func() time.Time { return time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC) },
	})

	result, err := usecase.Run(context.Background(), InfraSmokeInput{
		RequestID:  "req-t038-failure",
		SmokeRunID: "run-t038-failure",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want telemetry failure isolated from the business result", err)
	}
	if result.Status != InfraSmokeStatusOK {
		t.Fatalf("Run() status = %q, want %q", result.Status, InfraSmokeStatusOK)
	}
	if metrics.calls != 1 || logs.calls != 1 {
		t.Fatalf("telemetry calls = metrics:%d logs:%d, want both sinks attempted once", metrics.calls, logs.calls)
	}
	assertInfraSmokeTelemetryDiagnostics(t, diagnostics.failures)
}

// 该断言把 infra-only 与 AI 平面的边界固定在 usecase 层：未来即使 chat 增加更多
// AI 属性，基础设施探针也不能借用或伪造任何 AI identity。
func assertInfraSmokeSpan(t *testing.T, span tracetest.SpanStub, requestID, smokeRunID string) {
	t.Helper()
	if span.Name != "HTTP GET "+infraSmokeRoute {
		t.Fatalf("span name = %q, want infra-smoke HTTP span", span.Name)
	}
	attributes := attributesByKey(span.Attributes)
	for key, want := range map[string]string{
		"request.id":               requestID,
		"longtermism.smoke.run_id": smokeRunID,
		"http.request.method":      "GET",
		"http.route":               infraSmokeRoute,
	} {
		if got := attributes[key].AsString(); got != want {
			t.Fatalf("span attribute %q = %q, want %q", key, got, want)
		}
	}
	if got := attributes["http.response.status_code"].AsInt64(); got != 200 {
		t.Fatalf("span attribute http.response.status_code = %d, want 200", got)
	}
	for key := range attributes {
		if key == "longtermism.observability.plane" || hasAIPrefix(key) {
			t.Fatalf("infra-only span leaked AI semantic attribute %q", key)
		}
	}
}

func assertInfraSmokeLog(t *testing.T, entries []appobservability.HTTPCompletionLog, requestID, smokeRunID string) {
	t.Helper()
	if len(entries) != 1 {
		t.Fatalf("completion log count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.RequestID != requestID || entry.SmokeRunID != smokeRunID {
		t.Fatalf("completion log identities = request:%q smoke:%q, want request:%q smoke:%q", entry.RequestID, entry.SmokeRunID, requestID, smokeRunID)
	}
	if entry.AITraceID != "" {
		t.Fatalf("infra-only completion log emitted ai_trace_id = %q", entry.AITraceID)
	}
}

func assertInfraSmokeMetricDelta(t *testing.T, reader *metric.ManualReader) {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("ManualReader.Collect() error = %v", err)
	}

	seenRequestCount := false
	seenRequestDuration := false
	for _, scope := range collected.ScopeMetrics {
		for _, collectedMetric := range scope.Metrics {
			switch collectedMetric.Name {
			case "longtermism.http.server.request.count":
				data, ok := collectedMetric.Data.(metricdata.Sum[int64])
				if !ok || len(data.DataPoints) != 1 || data.DataPoints[0].Value != 1 {
					t.Fatalf("HTTP request metric = %#v, want one counter delta", collectedMetric.Data)
				}
				assertInfraSmokeMetricAttributes(t, data.DataPoints[0].Attributes.ToSlice())
				seenRequestCount = true
			case "longtermism.http.server.request.duration":
				data, ok := collectedMetric.Data.(metricdata.Histogram[float64])
				if !ok || len(data.DataPoints) != 1 || data.DataPoints[0].Count != 1 {
					t.Fatalf("HTTP duration metric = %#v, want one histogram delta", collectedMetric.Data)
				}
				assertInfraSmokeMetricAttributes(t, data.DataPoints[0].Attributes.ToSlice())
				seenRequestDuration = true
			}
		}
	}
	if !seenRequestCount || !seenRequestDuration {
		t.Fatalf("infra-smoke metric deltas = count:%t duration:%t, want both", seenRequestCount, seenRequestDuration)
	}
}

func assertInfraSmokeMetricAttributes(t *testing.T, values []attribute.KeyValue) {
	t.Helper()
	attributes := attributesByKey(values)
	if len(attributes) != 3 || attributes["http.route"].AsString() != infraSmokeRoute || attributes["http.request.method"].AsString() != "GET" || attributes["http.response.status_class"].AsString() != "2xx" {
		t.Fatalf("HTTP metric attributes = %#v, want only route/method/status class", attributes)
	}
	for key := range attributes {
		if key == "smoke_run_id" || key == "request.id" || key == "trace.id" || key == "span.id" || hasAIPrefix(key) {
			t.Fatalf("HTTP metric used forbidden identity label %q", key)
		}
	}
}

func attributesByKey(values []attribute.KeyValue) map[string]attribute.Value {
	result := make(map[string]attribute.Value, len(values))
	for _, value := range values {
		result[string(value.Key)] = value.Value
	}
	return result
}

func hasAIPrefix(key string) bool {
	for _, prefix := range []string{"longtermism.ai.", "gen_ai.", "ai."} {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// 诊断只保留稳定组件和错误类别。它既证明失败没有被静默吞掉，也避免把 exporter
// 原始错误、request ID 或 run marker 重新变成一条高敏日志通道。
func assertInfraSmokeTelemetryDiagnostics(t *testing.T, failures []InfraSmokeTelemetryFailure) {
	t.Helper()
	assertInfraSmokeDiagnosticShape(t)
	if len(failures) != 2 {
		t.Fatalf("telemetry diagnostic count = %d, want 2", len(failures))
	}
	want := map[string]string{
		"metrics":        "telemetry_export_failed",
		"completion_log": "telemetry_export_failed",
	}
	seen := make(map[string]int, len(want))
	for _, failure := range failures {
		if errorClass, exists := want[failure.Component]; !exists || failure.ErrorClass != errorClass {
			t.Fatalf("telemetry diagnostic = %#v, want stable component and error class", failure)
		}
		seen[failure.Component]++
	}
	for component := range want {
		if seen[component] != 1 {
			t.Fatalf("telemetry diagnostic count for %q = %d, want 1", component, seen[component])
		}
	}
}

func assertInfraSmokeDiagnosticShape(t *testing.T) {
	t.Helper()
	typeOfFailure := reflect.TypeFor[InfraSmokeTelemetryFailure]()
	if typeOfFailure.Kind() != reflect.Struct || typeOfFailure.NumField() != 2 {
		t.Fatalf("InfraSmokeTelemetryFailure must be a two-field low-sensitive diagnostic DTO, got %v", typeOfFailure)
	}
	for index, want := range []string{"Component", "ErrorClass"} {
		if field := typeOfFailure.Field(index); field.Name != want || !field.IsExported() || field.Type.Kind() != reflect.String {
			t.Fatalf("InfraSmokeTelemetryFailure field %d = %#v, want exported string %q", index, field, want)
		}
	}
}

type inMemoryInfraSmokeLogWriter struct {
	entries []appobservability.HTTPCompletionLog
}

func (w *inMemoryInfraSmokeLogWriter) Write(_ context.Context, entry appobservability.HTTPCompletionLog) error {
	w.entries = append(w.entries, entry)
	return nil
}

type failingInfraSmokeMetrics struct {
	calls int
	err   error
}

func (m *failingInfraSmokeMetrics) RecordHTTP(_ context.Context, _ appobservability.HTTPMetric) error {
	m.calls++
	return m.err
}

type failingInfraSmokeLogWriter struct {
	calls int
	err   error
}

func (w *failingInfraSmokeLogWriter) Write(_ context.Context, _ appobservability.HTTPCompletionLog) error {
	w.calls++
	return w.err
}

type inMemoryInfraSmokeDiagnostics struct {
	failures []InfraSmokeTelemetryFailure
}

func (d *inMemoryInfraSmokeDiagnostics) RecordTelemetryFailure(_ context.Context, failure InfraSmokeTelemetryFailure) {
	d.failures = append(d.failures, failure)
}
