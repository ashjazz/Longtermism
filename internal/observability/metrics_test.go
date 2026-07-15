package observability

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricsRecordRequiredInstrumentsWithOnlyLowCardinalityAttributes(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	metrics, err := NewMetrics(provider.Meter("github.com/ashjazz/Longtermism/internal/observability"), WithMetricLabelPolicy(testMetricLabelPolicy()))
	if err != nil {
		t.Fatal("NewMetrics() returned an unexpected error")
	}

	ctx := context.Background()
	// 每种输入都带上故意的高基数身份和原始内容；指标端口只可消费合同列出的
	// 低基数维度，防止一次 chat 或 smoke 造成新的 Prometheus time series。
	if err := metrics.RecordHTTP(ctx, HTTPMetric{RouteTemplate: "/api/v1/chat", RawRoute: "/api/v1/chat?message=synthetic-private-prompt", Method: "POST", StatusCode: 502, Duration: 120 * time.Millisecond, RequestID: "req-t017", TraceID: "trace-t017", SpanID: "span-t017", SmokeRunID: "smoke-t017"}); err != nil {
		t.Fatal("RecordHTTP() returned an unexpected error")
	}
	if err := metrics.RecordLLM(ctx, LLMMetric{Provider: "openai-compatible", RequestedModel: "gpt-test", ActualModel: "gpt-test-actual", Outcome: "failed", Duration: 800 * time.Millisecond, InputTokens: 10, OutputTokens: 5, Cost: 0.01, Currency: "USD", EstimateStatus: "estimated", AITraceID: "ai-t017", SessionID: "session-t017", TraceID: "trace-t017", SpanID: "span-t017", PromptHash: "sha256:synthetic"}); err != nil {
		t.Fatal("RecordLLM() returned an unexpected error")
	}
	if err := metrics.RecordEval(ctx, EvalMetric{Evaluator: "deterministic", Status: "passed", MetricName: "answer_quality", Score: 0.9, RequestID: "req-t017", AITraceID: "ai-t017", TraceID: "trace-t017", SpanID: "span-t017", PromptHash: "sha256:synthetic"}); err != nil {
		t.Fatal("RecordEval() returned an unexpected error")
	}
	if err := metrics.RecordScoreProjection(ctx, ScoreProjectionMetric{Backend: "langfuse", Status: "sent", RequestID: "req-t017", AITraceID: "ai-t017", TraceID: "trace-t017", SpanID: "span-t017", SmokeRunID: "smoke-t017"}); err != nil {
		t.Fatal("RecordScoreProjection() returned an unexpected error")
	}
	if err := metrics.RecordScoreQueue(ctx, ScoreQueueMetric{Backend: "langfuse", Depth: 3, RequestID: "req-t017", TraceID: "trace-t017", SpanID: "span-t017", SessionID: "session-t017"}); err != nil {
		t.Fatal("RecordScoreQueue() returned an unexpected error")
	}

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal("ManualReader.Collect() returned an unexpected error")
	}

	requestAttributes := metricAttributes("http.route", "/api/v1/chat", "http.request.method", "POST", "http.response.status_class", "5xx")
	llmRequestAttributes := metricAttributes("gen_ai.provider.name", "openai-compatible", "gen_ai.request.model", "gpt-test", "outcome", "failed")
	evalAttributes := metricAttributes("evaluator", "deterministic", "status", "passed", "metric.name", "answer_quality")
	want := map[string]metricExpectation{
		"longtermism.http.server.request.count":    {kind: metricKindCounter, expectedAttributeSets: []map[string]string{requestAttributes}},
		"longtermism.http.server.request.duration": {kind: metricKindHistogram, expectedAttributeSets: []map[string]string{requestAttributes}},
		"longtermism.llm.request.count":            {kind: metricKindCounter, expectedAttributeSets: []map[string]string{llmRequestAttributes}},
		"longtermism.llm.duration":                 {kind: metricKindHistogram, expectedAttributeSets: []map[string]string{llmRequestAttributes}},
		"longtermism.llm.tokens": {kind: metricKindCounter, expectedAttributeSets: []map[string]string{
			metricAttributes("gen_ai.provider.name", "openai-compatible", "gen_ai.response.model", "gpt-test-actual", "gen_ai.token.type", "input"),
			metricAttributes("gen_ai.provider.name", "openai-compatible", "gen_ai.response.model", "gpt-test-actual", "gen_ai.token.type", "output"),
		}},
		"longtermism.llm.cost":           {kind: metricKindCounter, expectedAttributeSets: []map[string]string{metricAttributes("gen_ai.provider.name", "openai-compatible", "gen_ai.response.model", "gpt-test-actual", "currency", "USD", "estimate.status", "estimated")}},
		"longtermism.eval.result":        {kind: metricKindCounter, expectedAttributeSets: []map[string]string{evalAttributes}},
		"longtermism.eval.score":         {kind: metricKindHistogram, expectedAttributeSets: []map[string]string{evalAttributes}},
		"longtermism.score.projection":   {kind: metricKindCounter, expectedAttributeSets: []map[string]string{metricAttributes("backend", "langfuse", "status", "sent")}},
		"longtermism.score.worker.queue": {kind: metricKindGauge, expectedAttributeSets: []map[string]string{metricAttributes("backend", "langfuse")}},
	}

	seen := make(map[string]struct{}, len(want))
	for _, scopeMetrics := range collected.ScopeMetrics {
		for _, collectedMetric := range scopeMetrics.Metrics {
			expectation, required := want[collectedMetric.Name]
			if !required {
				t.Fatal("scoped meter emitted an unknown metric instrument")
			}
			seen[collectedMetric.Name] = struct{}{}
			assertMetricAggregationKind(t, collectedMetric.Data, expectation.kind)
			assertMetricDataPointAttributes(t, collectedMetric.Data, expectation.expectedAttributeSets)
		}
	}
	if len(seen) != len(want) {
		t.Fatal("required first-wave metric instruments were not all collected")
	}
}

func TestMetricsCoarsensUnknownLabels(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	metrics, err := NewMetrics(provider.Meter("t029-label-policy"), WithMetricLabelPolicy(testMetricLabelPolicy()))
	if err != nil {
		t.Fatal("NewMetrics() returned an unexpected error")
	}

	// 模拟错误边界把原始路由、模型别名和敏感文本交给指标端口。它们必须被压缩为
	// 有界 other，而不是作为标签值进入 Prometheus 时序。
	for _, record := range []func() error{
		func() error {
			return metrics.RecordHTTP(context.Background(), HTTPMetric{RouteTemplate: "/api/v1/chat?token=synthetic-private", Method: "TRACE", StatusCode: 200})
		},
		func() error {
			return metrics.RecordLLM(context.Background(), LLMMetric{Provider: "Bearer synthetic-private", RequestedModel: "user-input-synthetic-private", ActualModel: "provider-error-synthetic-private", Outcome: "provider-error-synthetic-private"})
		},
		func() error {
			return metrics.RecordEval(context.Background(), EvalMetric{Evaluator: "user-synthetic-private", Status: "user-synthetic-private", MetricName: "user-synthetic-private"})
		},
		func() error {
			return metrics.RecordScoreProjection(context.Background(), ScoreProjectionMetric{Backend: "user-synthetic-private", Status: "user-synthetic-private"})
		},
		func() error {
			return metrics.RecordScoreQueue(context.Background(), ScoreQueueMetric{Backend: "user-synthetic-private"})
		},
	} {
		if err := record(); err != nil {
			t.Fatalf("recording unknown labels returned error = %v", err)
		}
	}

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal("ManualReader.Collect() returned an unexpected error")
	}

	otherCount := 0
	for _, scopeMetrics := range collected.ScopeMetrics {
		for _, collectedMetric := range scopeMetrics.Metrics {
			for _, attributes := range metricAttributeSets(collectedMetric.Data) {
				for _, value := range attributes {
					if strings.Contains(value, "synthetic-private") || strings.Contains(value, "Bearer") {
						t.Fatalf("metric label leaked an unbounded or sensitive input: %q", value)
					}
					if value == metricOtherLabelValue {
						otherCount++
					}
				}
			}
		}
	}
	if otherCount == 0 {
		t.Fatal("unknown labels were not coarsened to other")
	}
}

func TestMetricsRejectsInvalidMeasurements(t *testing.T) {
	tests := []struct {
		name   string
		record func(*Metrics) error
	}{
		{name: "negative HTTP duration", record: func(metrics *Metrics) error {
			return metrics.RecordHTTP(context.Background(), HTTPMetric{Duration: -time.Second})
		}},
		{name: "negative LLM tokens", record: func(metrics *Metrics) error {
			return metrics.RecordLLM(context.Background(), LLMMetric{InputTokens: -1})
		}},
		{name: "negative LLM cost", record: func(metrics *Metrics) error { return metrics.RecordLLM(context.Background(), LLMMetric{Cost: -0.1}) }},
		{name: "NaN LLM cost", record: func(metrics *Metrics) error {
			return metrics.RecordLLM(context.Background(), LLMMetric{Cost: math.NaN()})
		}},
		{name: "infinite LLM cost", record: func(metrics *Metrics) error {
			return metrics.RecordLLM(context.Background(), LLMMetric{Cost: math.Inf(1)})
		}},
		{name: "negative eval score", record: func(metrics *Metrics) error { return metrics.RecordEval(context.Background(), EvalMetric{Score: -0.1}) }},
		{name: "infinite eval score", record: func(metrics *Metrics) error {
			return metrics.RecordEval(context.Background(), EvalMetric{Score: math.Inf(1)})
		}},
		{name: "negative queue depth", record: func(metrics *Metrics) error {
			return metrics.RecordScoreQueue(context.Background(), ScoreQueueMetric{Depth: -1})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := metric.NewManualReader()
			provider := metric.NewMeterProvider(metric.WithReader(reader))
			metrics, err := NewMetrics(provider.Meter("t029-invalid-measurement"), WithMetricLabelPolicy(testMetricLabelPolicy()))
			if err != nil {
				t.Fatal("NewMetrics() returned an unexpected error")
			}

			if err := test.record(metrics); !errors.Is(err, ErrInvalidMetricValue) {
				t.Fatalf("record() error = %v, want ErrInvalidMetricValue", err)
			}

			var collected metricdata.ResourceMetrics
			if err := reader.Collect(context.Background(), &collected); err != nil {
				t.Fatal("ManualReader.Collect() returned an unexpected error")
			}
			for _, scopeMetrics := range collected.ScopeMetrics {
				for _, collectedMetric := range scopeMetrics.Metrics {
					if got := len(metricAttributeSets(collectedMetric.Data)); got != 0 {
						t.Fatalf("invalid input emitted %d partial metric data points", got)
					}
				}
			}
		})
	}
}

func testMetricLabelPolicy() MetricLabelPolicy {
	return MetricLabelPolicy{
		AllowedRoutes:      []string{"/api/v1/chat"},
		AllowedModels:      []string{"gpt-test", "gpt-test-actual"},
		AllowedMetricNames: []string{"answer_quality"},
	}
}

func TestStatusClass(t *testing.T) {
	// 状态码只被投影为有限集合，避免把完整状态或任意输入变成新的指标序列。
	tests := []struct {
		name       string
		statusCode int
		want       string
	}{
		{name: "informational", statusCode: 100, want: "1xx"},
		{name: "success", statusCode: 200, want: "2xx"},
		{name: "redirect", statusCode: 302, want: "3xx"},
		{name: "client error", statusCode: 404, want: "4xx"},
		{name: "server error", statusCode: 502, want: "5xx"},
		{name: "out of HTTP range", statusCode: 0, want: "unknown"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := statusClass(test.statusCode); got != test.want {
				t.Fatalf("statusClass(%d) = %q, want %q", test.statusCode, got, test.want)
			}
		})
	}
}

type metricKind string

const (
	metricKindCounter   metricKind = "counter"
	metricKindHistogram metricKind = "histogram"
	metricKindGauge     metricKind = "gauge"
)

type metricExpectation struct {
	kind                  metricKind
	expectedAttributeSets []map[string]string
}

func assertMetricAggregationKind(t *testing.T, aggregation metricdata.Aggregation, want metricKind) {
	t.Helper()
	switch want {
	case metricKindCounter:
		// 成本是小数金额；强制它使用 int64 counter 会截断真实事实。首批契约只要求
		// 单调 counter，因此同时接受整数计数与浮点金额两种合法的 OTLP 聚合。
		switch counter := aggregation.(type) {
		case metricdata.Sum[int64]:
			if !counter.IsMonotonic {
				t.Fatal("metric did not use the expected monotonic counter aggregation")
			}
		case metricdata.Sum[float64]:
			if !counter.IsMonotonic {
				t.Fatal("metric did not use the expected monotonic counter aggregation")
			}
		default:
			t.Fatal("metric did not use the expected counter aggregation")
		}
	case metricKindHistogram:
		if _, ok := aggregation.(metricdata.Histogram[float64]); !ok {
			t.Fatal("metric did not use the expected histogram aggregation")
		}
	case metricKindGauge:
		if _, ok := aggregation.(metricdata.Gauge[int64]); !ok {
			t.Fatal("metric did not use the expected gauge aggregation")
		}
	}
}

func assertMetricDataPointAttributes(t *testing.T, aggregation metricdata.Aggregation, expectedSets []map[string]string) {
	t.Helper()
	sets := metricAttributeSets(aggregation)
	if len(sets) == 0 {
		t.Fatal("metric contained no data points")
	}
	for _, attributes := range sets {
		if !matchesExpectedMetricAttributeSet(attributes, expectedSets) {
			t.Fatal("metric attribute values did not match the low-cardinality contract")
		}
		for _, forbiddenKey := range []string{"request_id", "trace_id", "span_id", "ai_trace_id", "session_id", "user_id", "raw_route", "prompt_hash", "smoke_run_id"} {
			if _, exists := attributes[forbiddenKey]; exists {
				t.Fatal("metric attributes included a forbidden high-cardinality key")
			}
		}
		for _, forbiddenFragment := range []string{"req-t017", "trace-t017", "span-t017", "ai-t017", "session-t017", "smoke-t017", "sha256:synthetic", "synthetic-private-prompt"} {
			for _, value := range attributes {
				if strings.Contains(value, forbiddenFragment) {
					t.Fatal("metric attribute value contained a forbidden high-cardinality or sensitive marker")
				}
			}
		}
	}
	for _, expected := range expectedSets {
		if !matchesExpectedMetricAttributeSet(expected, sets) {
			t.Fatal("metric omitted an expected low-cardinality attribute set")
		}
	}
}

func matchesExpectedMetricAttributeSet(got map[string]string, wantSets []map[string]string) bool {
	for _, want := range wantSets {
		if reflect.DeepEqual(got, want) {
			return true
		}
	}
	return false
}

func metricAttributes(keyValuePairs ...string) map[string]string {
	attributes := make(map[string]string, len(keyValuePairs)/2)
	for index := 0; index < len(keyValuePairs); index += 2 {
		attributes[keyValuePairs[index]] = keyValuePairs[index+1]
	}
	return attributes
}

func metricAttributeSets(aggregation metricdata.Aggregation) []map[string]string {
	var sets []map[string]string
	appendSet := func(slice []metricdata.DataPoint[int64]) {
		for _, point := range slice {
			sets = append(sets, metricAttributeSet(point.Attributes.ToSlice()))
		}
	}
	switch data := aggregation.(type) {
	case metricdata.Sum[int64]:
		appendSet(data.DataPoints)
	case metricdata.Sum[float64]:
		for _, point := range data.DataPoints {
			sets = append(sets, metricAttributeSet(point.Attributes.ToSlice()))
		}
	case metricdata.Gauge[int64]:
		appendSet(data.DataPoints)
	case metricdata.Histogram[float64]:
		for _, point := range data.DataPoints {
			sets = append(sets, metricAttributeSet(point.Attributes.ToSlice()))
		}
	}
	return sets
}

func metricAttributeSet(attributes []attribute.KeyValue) map[string]string {
	result := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		result[string(attribute.Key)] = attribute.Value.AsString()
	}
	return result
}
