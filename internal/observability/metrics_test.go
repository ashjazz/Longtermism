package observability

import (
	"context"
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
	metrics, err := NewMetrics(provider.Meter("github.com/ashjazz/Longtermism/internal/observability"))
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
		counter, ok := aggregation.(metricdata.Sum[int64])
		if !ok || !counter.IsMonotonic {
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
