// Package observability contains pure application usecases for infrastructure observability.
package observability

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	logicchat "github.com/ashjazz/Longtermism/internal/logic/chat"
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

// The smoke probe is exceptional: its Prometheus assertion runs immediately after the request.
// ForceFlush makes the just-recorded counter eligible for the Collector before that query starts.
func TestInfraSmokeUsecaseFlushesMetricsAndRecordsFlushFailure(t *testing.T) {
	flusher := &failingInfraSmokeFlusher{err: errors.New("synthetic metric flush failure")}
	diagnostics := &inMemoryInfraSmokeDiagnostics{}
	usecase := NewInfraSmokeUsecase(InfraSmokeUsecaseDependencies{
		MetricFlusher: flusher,
		Diagnostics:   diagnostics,
	})

	result, err := usecase.Run(context.Background(), InfraSmokeInput{RequestID: "req-t066-flush", SmokeRunID: "run-t066-flush"})
	if err != nil || result.Status != InfraSmokeStatusOK {
		t.Fatalf("Run() = (%#v, %v), want successful business result despite flush failure", result, err)
	}
	if flusher.calls != 1 {
		t.Fatalf("metric flush calls = %d, want one", flusher.calls)
	}
	if len(diagnostics.failures) != 1 || diagnostics.failures[0] != (InfraSmokeTelemetryFailure{Component: "metrics_flush", ErrorClass: infraSmokeTelemetryFailureClass}) {
		t.Fatalf("metric flush diagnostics = %#v, want one low-sensitive flush failure", diagnostics.failures)
	}
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

type failingInfraSmokeFlusher struct {
	calls int
	err   error
}

func (f *failingInfraSmokeFlusher) Flush(_ context.Context) error {
	f.calls++
	return f.err
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

// ---------------------------------------------------------------------------
// T199（RED）：AI-negative marker-count 事实源 usecase 契约。
//
// 事实源必须是应用自己拥有的真实 AI 平面发射记录，而不是硬编码 0、把 Collector
// 冒充可查询存储或从日志字段猜测。usecase 在把任何输入交给事实源之前先完成
// marker/window 校验：disabled/remote/unauthenticated/replay 由传输边界拒绝，
// invalid query 必须在本层零事实读取地拒绝。
// ---------------------------------------------------------------------------

func aiPlaneMarkerCountNow() time.Time {
	return time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
}

func aiPlaneMarkerCountErrorClass(err error) string {
	var classed interface{ Class() string }
	if errors.As(err, &classed) {
		return classed.Class()
	}
	return ""
}

// 拒绝路径必须在事实源之前完成：任何畸形 marker/window 都以 stable
// invalid_query 类失败，且注入的计数事实源调用次数为 0。
func TestAIPlaneMarkerCountUsecaseRejectsInvalidQueriesBeforeFactSource(t *testing.T) {
	now := aiPlaneMarkerCountNow()
	validMarker := "run-t199-ai-negative"
	tests := []struct {
		name      string
		marker    string
		startedAt time.Time
		deadline  time.Time
	}{
		{name: "missing marker", startedAt: now.Add(-time.Second), deadline: now.Add(time.Second)},
		{name: "short marker", marker: "short", startedAt: now.Add(-time.Second), deadline: now.Add(time.Second)},
		{name: "oversized marker", marker: strings.Repeat("a", 129), startedAt: now.Add(-time.Second), deadline: now.Add(time.Second)},
		{name: "marker with unsafe characters", marker: "run marker!@#", startedAt: now.Add(-time.Second), deadline: now.Add(time.Second)},
		{name: "zero started at", marker: validMarker, deadline: now.Add(time.Second)},
		{name: "zero deadline", marker: validMarker, startedAt: now.Add(-time.Second)},
		{name: "inverted window", marker: validMarker, startedAt: now.Add(time.Second), deadline: now.Add(-time.Second)},
		{name: "empty window", marker: validMarker, startedAt: now, deadline: now},
		{name: "window exceeds one minute", marker: validMarker, startedAt: now.Add(-time.Second), deadline: now.Add(time.Minute)},
		{name: "stale window start", marker: validMarker, startedAt: now.Add(-time.Minute - time.Nanosecond), deadline: now},
		{name: "future window deadline", marker: validMarker, startedAt: now, deadline: now.Add(time.Minute + time.Nanosecond)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &recordingAIPlaneFactSource{}
			usecase := NewAIPlaneMarkerCountUsecase(AIPlaneMarkerCountUsecaseDependencies{
				Source: source, Now: func() time.Time { return now },
			})
			count, err := usecase.Count(context.Background(), AIPlaneMarkerCountInput{
				Marker: tt.marker, StartedAt: tt.startedAt, Deadline: tt.deadline,
			})
			if err == nil || aiPlaneMarkerCountErrorClass(err) != AIPlaneMarkerCountInvalidQueryClass {
				t.Fatalf("Count() = (%d, %v), want invalid_query rejection", count, err)
			}
			if source.calls != 0 {
				t.Fatalf("fact source reads = %d, want zero reads before validation", source.calls)
			}
		})
	}
}

// 成功路径必须把 marker/window 原样交给真实事实源，并以内置上限约束扫描范围：
// 结果只来自事实源，usecase 不自造 0 也不放宽扫描。
func TestAIPlaneMarkerCountUsecaseQueriesTheRealFactSourceWithBoundedScan(t *testing.T) {
	now := aiPlaneMarkerCountNow()
	tests := []struct {
		name  string
		count int
	}{
		{name: "real negative evidence is zero", count: 0},
		{name: "real positive evidence is preserved", count: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &recordingAIPlaneFactSource{count: tt.count}
			usecase := NewAIPlaneMarkerCountUsecase(AIPlaneMarkerCountUsecaseDependencies{
				Source: source, Now: func() time.Time { return now },
			})
			startedAt := now.Add(-30 * time.Second)
			deadline := now.Add(30 * time.Second)
			count, err := usecase.Count(context.Background(), AIPlaneMarkerCountInput{
				Marker: "run-t199-real", StartedAt: startedAt, Deadline: deadline,
			})
			if err != nil || count != tt.count {
				t.Fatalf("Count() = (%d, %v), want (%d, nil)", count, err, tt.count)
			}
			if source.calls != 1 || source.marker != "run-t199-real" || !source.startedAt.Equal(startedAt) || !source.deadline.Equal(deadline) {
				t.Fatalf("fact source received = %#v, want one verbatim marker+window query", source)
			}
			if source.limit != maximumAIPlaneFactSourceLimit {
				t.Fatalf("fact source scan limit = %d, want bounded constant %d", source.limit, maximumAIPlaneFactSourceLimit)
			}
		})
	}
}

// 事实源失败绝不能变成 count=0：错误必须原样浮出，调用方只能把成功的有界查询
// 结果当作证据。同时拒绝违反事实源契约的返回值（负数、超过扫描上限）。
func TestAIPlaneMarkerCountUsecaseNeverFabricatesZeroFromSourceFailures(t *testing.T) {
	now := aiPlaneMarkerCountNow()
	validInput := AIPlaneMarkerCountInput{
		Marker: "run-t199-failure", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Second),
	}

	t.Run("source failure surfaces instead of a zero count", func(t *testing.T) {
		sourceFailure := errors.New("synthetic ai plane fact source unavailable")
		source := &recordingAIPlaneFactSource{err: sourceFailure}
		usecase := NewAIPlaneMarkerCountUsecase(AIPlaneMarkerCountUsecaseDependencies{
			Source: source, Now: func() time.Time { return now },
		})
		count, err := usecase.Count(context.Background(), validInput)
		if err == nil || !errors.Is(err, sourceFailure) || count != 0 {
			t.Fatalf("Count() = (%d, %v), want the source failure surfaced with no fabricated count", count, err)
		}
	})

	t.Run("negative source count is a contract violation", func(t *testing.T) {
		source := &recordingAIPlaneFactSource{count: -1}
		usecase := NewAIPlaneMarkerCountUsecase(AIPlaneMarkerCountUsecaseDependencies{
			Source: source, Now: func() time.Time { return now },
		})
		count, err := usecase.Count(context.Background(), validInput)
		if err == nil || aiPlaneMarkerCountErrorClass(err) != AIPlaneMarkerCountQueryFailedClass || count != 0 {
			t.Fatalf("Count() = (%d, %v), want query_failed rejection", count, err)
		}
	})

	t.Run("count above the scan limit is a contract violation", func(t *testing.T) {
		source := &recordingAIPlaneFactSource{count: maximumAIPlaneFactSourceLimit + 1}
		usecase := NewAIPlaneMarkerCountUsecase(AIPlaneMarkerCountUsecaseDependencies{
			Source: source, Now: func() time.Time { return now },
		})
		count, err := usecase.Count(context.Background(), validInput)
		if err == nil || aiPlaneMarkerCountErrorClass(err) != AIPlaneMarkerCountQueryFailedClass || count != 0 {
			t.Fatalf("Count() = (%d, %v), want query_failed rejection", count, err)
		}
	})
}

// 没有真实事实源时 usecase 必须 fail-closed：空 source 不能退化为固定 0。
func TestAIPlaneMarkerCountUsecaseRequiresARealFactSource(t *testing.T) {
	now := aiPlaneMarkerCountNow()
	usecase := NewAIPlaneMarkerCountUsecase(AIPlaneMarkerCountUsecaseDependencies{
		Now: func() time.Time { return now },
	})
	count, err := usecase.Count(context.Background(), AIPlaneMarkerCountInput{
		Marker: "run-t199-source", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Second),
	})
	if err == nil || aiPlaneMarkerCountErrorClass(err) != AIPlaneMarkerCountQueryFailedClass || count != 0 {
		t.Fatalf("Count() = (%d, %v), want query_failed rejection without a fact source", count, err)
	}
}

type recordingAIPlaneFactSource struct {
	calls     int
	marker    string
	startedAt time.Time
	deadline  time.Time
	limit     int
	count     int
	err       error
}

func (s *recordingAIPlaneFactSource) CountAIPlaneFacts(_ context.Context, marker string, startedAt, deadline time.Time, limit int) (int, error) {
	s.calls++
	s.marker, s.startedAt, s.deadline, s.limit = marker, startedAt, deadline, limit
	return s.count, s.err
}

// ---------------------------------------------------------------------------
// AIPlaneEmissionRegistry 是生产事实源的实现：真实、只读、有界。以下测试证明
// 计数只来自登记事实（精确 marker + 闭合窗口 + 扫描上限）、TTL 驱逐与每 marker
// 洪水上限保证内存有界，以及 chat 用例的 recorder 端口由同一实例结构性满足。
// ---------------------------------------------------------------------------

var _ logicchat.AIPlaneFactRecorder = (*AIPlaneEmissionRegistry)(nil)

func TestAIPlaneEmissionRegistryCountsOnlyExactWindowedFacts(t *testing.T) {
	registry := NewAIPlaneEmissionRegistry(0, 0, nil)
	now := time.Now()
	registry.RecordAIPlaneFact("run-registry-marker", now.Add(-10*time.Second))
	// 窗口外但仍未过 TTL 的事实不得计入，其它 marker 的事实也不得计入。
	registry.RecordAIPlaneFact("run-registry-marker", now.Add(-90*time.Second))
	registry.RecordAIPlaneFact("run-other-marker", now.Add(-10*time.Second))

	startedAt := now.Add(-30 * time.Second)
	deadline := now.Add(30 * time.Second)
	count, err := registry.CountAIPlaneFacts(context.Background(), "run-registry-marker", startedAt, deadline, maximumAIPlaneFactSourceLimit)
	if err != nil || count != 1 {
		t.Fatalf("CountAIPlaneFacts() = (%d, %v), want exactly one in-window fact for the exact marker", count, err)
	}
}

func TestAIPlaneEmissionRegistryBoundsTheScanByLimit(t *testing.T) {
	registry := NewAIPlaneEmissionRegistry(0, 0, nil)
	now := time.Now()
	for index := 0; index < 4; index++ {
		registry.RecordAIPlaneFact("run-registry-limit", now.Add(-time.Duration(index)*time.Second))
	}
	count, err := registry.CountAIPlaneFacts(context.Background(), "run-registry-limit", now.Add(-time.Minute), now.Add(time.Minute), 2)
	if err != nil || count != 2 {
		t.Fatalf("CountAIPlaneFacts() = (%d, %v), want the scan limit capped at 2", count, err)
	}
}

func TestAIPlaneEmissionRegistryEvictsExpiredFacts(t *testing.T) {
	// 写路径与读路径共用注入时钟：过 TTL 的事实无论从哪一侧触发驱逐都被一致清除。
	now := aiPlaneMarkerCountNow()
	registry := NewAIPlaneEmissionRegistry(time.Second, 0, func() time.Time { return now })
	registry.RecordAIPlaneFact("run-registry-ttl", now.Add(-time.Minute))
	count, err := registry.CountAIPlaneFacts(context.Background(), "run-registry-ttl", now.Add(-time.Minute), now, maximumAIPlaneFactSourceLimit)
	if err != nil || count != 0 {
		t.Fatalf("CountAIPlaneFacts() = (%d, %v), want expired facts evicted", count, err)
	}
}

func TestAIPlaneEmissionRegistryBoundsFloodedMarkers(t *testing.T) {
	registry := NewAIPlaneEmissionRegistry(0, 2, nil)
	now := time.Now()
	for index := 0; index < 4; index++ {
		registry.RecordAIPlaneFact("run-registry-flood", now.Add(-time.Duration(index)*time.Second))
	}
	count, err := registry.CountAIPlaneFacts(context.Background(), "run-registry-flood", now.Add(-time.Minute), now.Add(time.Minute), maximumAIPlaneFactSourceLimit)
	if err != nil || count != 2 {
		t.Fatalf("CountAIPlaneFacts() = (%d, %v), want per-marker facts capped at 2", count, err)
	}
}

func TestAIPlaneEmissionRegistryRejectsMalformedAndNilInput(t *testing.T) {
	registry := NewAIPlaneEmissionRegistry(0, 0, nil)
	now := aiPlaneMarkerCountNow()
	registry.RecordAIPlaneFact("bad marker", now)
	registry.RecordAIPlaneFact("run-registry-empty", time.Time{})
	count, err := registry.CountAIPlaneFacts(context.Background(), "bad marker", now.Add(-time.Minute), now.Add(time.Minute), maximumAIPlaneFactSourceLimit)
	if err != nil || count != 0 {
		t.Fatalf("CountAIPlaneFacts() = (%d, %v), want malformed records silently absent", count, err)
	}
	var nilRegistry *AIPlaneEmissionRegistry
	if _, err := nilRegistry.CountAIPlaneFacts(context.Background(), "run-registry-nil", now.Add(-time.Minute), now.Add(time.Minute), maximumAIPlaneFactSourceLimit); err == nil || aiPlaneMarkerCountErrorClass(err) != AIPlaneMarkerCountQueryFailedClass {
		t.Fatalf("nil registry error = %v, want stable query_failed", err)
	}
}
