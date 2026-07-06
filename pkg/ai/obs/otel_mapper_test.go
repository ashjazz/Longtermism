package obs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMapTraceToSpanSnapshotCoversObservationTypes(t *testing.T) {
	tests := []struct {
		name            string
		observationType ObservationType
		trace           Trace
		wantSpanName    string
		wantAttributes  map[string]string
		wantSummaries   []string
	}{
		{
			name:            "generation maps model prompt usage and latency summaries",
			observationType: ObservationTypeGeneration,
			trace: newMapperTrace(
				ObservationTypeGeneration,
				WithModel("gpt-mapper"),
				WithPrompt("prompt-v3", "sha256:prompt"),
				WithUsage(120, 48, 7),
				WithLatency(42, 360),
				WithSafeSummaries(
					NewSafeSummary(WithSummaryHash("sha256:query"), WithSummaryLength(32), WithSummaryCategory("zh-CN")),
					NewSafeSummary(WithSummaryHash("sha256:prompt"), WithSummaryLength(280), WithSummaryStatus("rendered")),
					SafeSummary{},
					SafeSummary{},
				),
			),
			wantSpanName: "ai.generation",
			wantAttributes: map[string]string{
				"ai.feature":                 "rag_qa",
				"ai.model":                   "gpt-mapper",
				"ai.prompt.template_version": "prompt-v3",
				"ai.usage.input_tokens":      "120",
				"ai.usage.output_tokens":     "48",
				"ai.latency.ttft_ms":         "42",
				"ai.latency.total_ms":        "360",
				"ai.outcome":                 "success",
			},
			wantSummaries: []string{"query", "prompt"},
		},
		{
			name:            "retriever maps retrieval count score and miss status",
			observationType: ObservationTypeRetriever,
			trace: newMapperTrace(
				ObservationTypeRetriever,
				WithRetrieval(0, "sha256:rewritten-query", []float64{}, 18),
				WithSafeSummaries(
					NewSafeSummary(WithSummaryHash("sha256:query"), WithSummaryLength(32)),
					SafeSummary{},
					NewSafeSummary(WithSummaryCount(0), WithSummaryStatus("miss"), WithSummaryErrorClass(string(FailureRetrievalMiss))),
					SafeSummary{},
				),
				WithOutcome("failure"),
			),
			wantSpanName: "ai.retriever",
			wantAttributes: map[string]string{
				"ai.retrieval.chunks":             "0",
				"ai.retrieval.query_rewrite_hash": "sha256:rewritten-query",
				"ai.latency.retrieval_ms":         "18",
				"ai.outcome":                      "failure",
			},
			wantSummaries: []string{"query", "retrieval"},
		},
		{
			name:            "tool maps safe tool summary without arguments",
			observationType: ObservationTypeTool,
			trace: newMapperTrace(
				ObservationTypeTool,
				WithSafeSummaries(
					NewSafeSummary(WithSummaryHash("sha256:query"), WithSummaryLength(32)),
					SafeSummary{},
					SafeSummary{},
					NewSafeSummary(WithSummaryCategory("weather.lookup"), WithSummaryStatus("tool_error"), WithSummaryErrorClass("tool_error")),
				),
				WithOutcome("failure"),
			),
			wantSpanName: "ai.tool",
			wantAttributes: map[string]string{
				"ai.outcome": "failure",
			},
			wantSummaries: []string{"query", "tool"},
		},
		{
			name:            "agent maps control-plane termination state",
			observationType: ObservationTypeAgent,
			trace: newMapperTrace(
				ObservationTypeAgent,
				WithSafeSummaries(
					SafeSummary{},
					SafeSummary{},
					SafeSummary{},
					NewSafeSummary(WithSummaryCategory("agent.step"), WithSummaryStatus("terminated"), WithSummaryErrorClass(string(FailureBudgetExceeded))),
				),
				WithOutcome("terminated"),
			),
			wantSpanName: "ai.agent",
			wantAttributes: map[string]string{
				"ai.outcome": "terminated",
			},
			wantSummaries: []string{"tool"},
		},
		{
			name:            "evaluator maps evaluation score as safe summary",
			observationType: ObservationTypeEvaluator,
			trace: newMapperTrace(
				ObservationTypeEvaluator,
				WithSafeSummaries(
					SafeSummary{},
					SafeSummary{},
					NewSafeSummary(WithSummaryCategory("answer_relevance"), WithSummaryScore(0.82), WithSummaryStatus("passed")),
					SafeSummary{},
				),
			),
			wantSpanName: "ai.evaluator",
			wantAttributes: map[string]string{
				"ai.outcome": "success",
			},
			wantSummaries: []string{"retrieval"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := MapTraceToSpanSnapshot(tt.trace)
			if err != nil {
				t.Fatalf("MapTraceToSpanSnapshot() error = %v", err)
			}

			assertMappedSpanIdentity(t, snapshot, tt.wantSpanName, tt.observationType)
			assertMappedSpanAttributes(t, snapshot, tt.wantAttributes)
			assertMappedSpanSummaries(t, snapshot, tt.wantSummaries)
			assertMappedSpanPrivacy(t, snapshot)
		})
	}
}

func TestMapTraceToSpanSnapshotRejectsUnknownObservationType(t *testing.T) {
	trace := newMapperTrace(ObservationType("unknown"))

	_, err := MapTraceToSpanSnapshot(trace)
	if err == nil {
		t.Fatal("MapTraceToSpanSnapshot() error = nil, want unknown observation type error")
	}
}

func newMapperTrace(observationType ObservationType, options ...TraceOption) Trace {
	identity := NewCorrelationIdentity(
		"req-mapper-001",
		WithServiceSpan("svc-trace-mapper-001", "span-mapper-parent"),
		WithAITraceID("ai-trace-mapper-001"),
		WithSessionID("session-mapper-001"),
	)

	allOptions := []TraceOption{
		WithCorrelationIdentity(identity),
		WithObservationType(observationType),
		WithQuery("sha256:query", "zh-CN", 32),
		WithOutcome("success"),
	}
	allOptions = append(allOptions, options...)

	return NewTrace(
		"ai-trace-mapper-001",
		"rag_qa",
		time.Date(2026, time.July, 3, 11, 0, 0, 0, time.UTC),
		allOptions...,
	)
}

func assertMappedSpanIdentity(t *testing.T, snapshot TraceSpanSnapshot, wantName string, wantObservationType ObservationType) {
	t.Helper()

	for field, gotWant := range map[string][2]string{
		"Name":            {snapshot.Name, wantName},
		"RequestID":       {snapshot.RequestID, "req-mapper-001"},
		"ServiceTraceID":  {snapshot.ServiceTraceID, "svc-trace-mapper-001"},
		"ParentSpanID":    {snapshot.ParentSpanID, "span-mapper-parent"},
		"AITraceID":       {snapshot.AITraceID, "ai-trace-mapper-001"},
		"ObservationType": {snapshot.ObservationType.String(), wantObservationType.String()},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %q, want %q", field, gotWant[0], gotWant[1])
		}
	}
	if snapshot.SpanID == "" {
		t.Fatalf("SpanID is empty, want mapper-generated span identity")
	}
}

func assertMappedSpanAttributes(t *testing.T, snapshot TraceSpanSnapshot, want map[string]string) {
	t.Helper()

	for key, wantValue := range want {
		if snapshot.Attributes[key] != wantValue {
			t.Fatalf("Attributes[%q] = %q, want %q; all attributes = %#v", key, snapshot.Attributes[key], wantValue, snapshot.Attributes)
		}
	}
}

func assertMappedSpanSummaries(t *testing.T, snapshot TraceSpanSnapshot, wantKeys []string) {
	t.Helper()

	for _, key := range wantKeys {
		if _, ok := snapshot.Summaries[key]; !ok {
			t.Fatalf("Summaries missing %q in %#v", key, snapshot.Summaries)
		}
	}
}

func assertMappedSpanPrivacy(t *testing.T, snapshot TraceSpanSnapshot) {
	t.Helper()

	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal(snapshot) error = %v", err)
	}

	for _, forbidden := range []string{
		"raw_query",
		"prompt_content",
		"tool_args",
		"tool_arguments",
		"api_key",
		"password",
		"Bearer ",
		"token-private",
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("mapped span snapshot leaked forbidden content %q: %s", forbidden, string(payload))
		}
	}
}
