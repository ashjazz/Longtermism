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

// The core mapper must apply the shared routing marker to explicit semantic observations. This
// keeps route names from becoming an unsafe substitute for domain facts.
func TestMapTraceToSpanSnapshotMapsExplicitAIPlaneForSemanticObservations(t *testing.T) {
	tests := []struct {
		name            string
		observationType ObservationType
	}{
		{name: "generation", observationType: ObservationTypeGeneration},
		{name: "evaluator", observationType: ObservationTypeEvaluator},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := MapTraceToSpanSnapshot(newMapperTrace(tt.observationType))
			if err != nil {
				t.Fatalf("MapTraceToSpanSnapshot() error = %v", err)
			}
			assertMappedSpanAttributes(t, snapshot, map[string]string{
				"longtermism.observability.plane": "ai",
				"longtermism.ai.trace_id":         "ai-trace-mapper-001",
			})
		})
	}
}

func TestMapTraceToSpanSnapshotMapsExplicitGenerationFactsToGenAIAttributes(t *testing.T) {
	trace := newMapperTrace(
		ObservationTypeGeneration,
		func(trace Trace) Trace {
			trace.ProviderName = "openai-compatible"
			trace.RequestedModel = "server-requested-model"
			return trace
		},
		WithModel("provider-actual-model"),
		WithUsage(120, 48, 7),
		WithCacheUsage(5, 3),
		WithTemperature(0.2),
	)

	snapshot, err := MapTraceToSpanSnapshot(trace)
	if err != nil {
		t.Fatalf("MapTraceToSpanSnapshot() error = %v", err)
	}
	assertMappedSpanAttributes(t, snapshot, map[string]string{
		"gen_ai.provider.name":                 "openai-compatible",
		"gen_ai.request.model":                 "server-requested-model",
		"gen_ai.response.model":                "provider-actual-model",
		"gen_ai.usage.input_tokens":            "120",
		"gen_ai.usage.output_tokens":           "48",
		"gen_ai.usage.reasoning.output_tokens": "7",
		"gen_ai.request.temperature":           "0.2",
		// The current OTel version has no stable cache-token span attribute. Preserve the
		// explicit cache facts under the existing project namespace instead of inventing one.
		"ai.usage.cache_read_tokens":  "5",
		"ai.usage.cache_write_tokens": "3",
	})
}

func TestMapTraceToSpanSnapshotDoesNotLeakSensitiveGenerationFactsThroughGenAIAttributes(t *testing.T) {
	trace := newMapperTrace(
		ObservationTypeGeneration,
		func(trace Trace) Trace {
			trace.ProviderName = privacyContractExternalResponse
			trace.RequestedModel = privacyContractAPIKey
			trace.Model = privacyContractPrompt
			return trace
		},
	)

	snapshot, err := MapTraceToSpanSnapshot(trace)
	if err != nil {
		t.Fatalf("MapTraceToSpanSnapshot() error = %v", err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal(snapshot) error = %v", err)
	}
	for _, forbidden := range []string{privacyContractExternalResponse, privacyContractAPIKey, privacyContractPrompt} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatal("generation GenAI mapping must use the existing sensitive-value boundary")
		}
	}
	for _, key := range []string{"gen_ai.provider.name", "gen_ai.request.model", "gen_ai.response.model"} {
		if _, exists := snapshot.Attributes[key]; exists {
			t.Fatalf("sensitive generation fact must not be exported through %q", key)
		}
	}
}

// Routing uses an explicit role, never a span-name or route heuristic. The same allowlist can be
// used later by the chat bridge and real OTel adapter without marking infrastructure children.
func TestMapSpanRoutingAttributesMarksOnlyExplicitAIPlaneRoles(t *testing.T) {
	identity := NewCorrelationIdentity("req-routing-001", WithServiceSpan("service-trace-routing-001", "span-routing-001"), WithAITraceID("ai-trace-routing-001"))
	tests := []struct {
		name       string
		role       SpanRoutingRole
		wantMarker bool
		wantErr    bool
	}{
		{name: "chat root", role: SpanRoutingRoleAIChatRoot, wantMarker: true},
		{name: "chat bridge", role: SpanRoutingRoleAIChatBridge, wantMarker: true},
		{name: "generation", role: SpanRoutingRoleAIGeneration, wantMarker: true},
		{name: "evaluator", role: SpanRoutingRoleAIEvaluator, wantMarker: true},
		{name: "ordinary HTTP child", role: SpanRoutingRoleHTTPChild},
		{name: "database child", role: SpanRoutingRoleDatabaseChild},
		{name: "redis child", role: SpanRoutingRoleRedisChild},
		{name: "empty role is rejected", wantErr: true},
		{name: "unknown role is rejected", role: SpanRoutingRole("queue_child"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attributes, err := MapSpanRoutingAttributes(SpanRoutingInput{Role: tt.role, Identity: identity, Feature: "chat"})
			if tt.wantErr {
				if err == nil || len(attributes) != 0 {
					t.Fatalf("MapSpanRoutingAttributes() = (%#v, %v), want rejected role without routing attributes", attributes, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("MapSpanRoutingAttributes() error = %v", err)
			}
			marker, hasMarker := attributes["longtermism.observability.plane"]
			if tt.wantMarker {
				if !hasMarker || marker != "ai" || attributes["longtermism.ai.trace_id"] != "ai-trace-routing-001" {
					t.Fatalf("AI routing attributes = %#v, want explicit plane marker and domain AI trace ID", attributes)
				}
				return
			}
			if hasMarker || attributes["longtermism.ai.trace_id"] != "" {
				t.Fatalf("ordinary infrastructure child attributes = %#v, must not carry AI routing facts", attributes)
			}
		})
	}
}

// A mapper can add standard names only for facts its Trace actually carries. In particular it
// must not infer a chat operation from Feature, model facts from its span name, or finish reasons
// from a generic outcome; those are different facts and later adapters must provide them explicitly.
func TestMapTraceToSpanSnapshotDoesNotInventAbsentGenAISemantics(t *testing.T) {
	tests := []struct {
		name            string
		observationType ObservationType
		feature         string
	}{
		{name: "generation with route-like feature", observationType: ObservationTypeGeneration, feature: "HTTP POST /api/v1/chat"},
		{name: "evaluator with chat-like feature", observationType: ObservationTypeEvaluator, feature: "chat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := newMapperTrace(tt.observationType, withoutGenAIFacts(tt.feature))

			snapshot, err := MapTraceToSpanSnapshot(trace)
			if err != nil {
				t.Fatalf("MapTraceToSpanSnapshot() error = %v", err)
			}
			for _, key := range []string{
				"gen_ai.operation.name",
				"gen_ai.provider.name",
				"gen_ai.request.model",
				"gen_ai.response.model",
				"gen_ai.response.finish_reasons",
				"gen_ai.usage.input_tokens",
				"gen_ai.usage.output_tokens",
				"gen_ai.usage.reasoning.output_tokens",
				"gen_ai.server.time_to_first_token",
			} {
				if _, ok := snapshot.Attributes[key]; ok {
					t.Fatalf("Attributes must not guess %q from a route-like feature or absent fact", key)
				}
			}
		})
	}
}

func withoutGenAIFacts(feature string) TraceOption {
	return func(trace Trace) Trace {
		trace.Feature = feature
		trace.Model = ""
		trace.ProviderName = ""
		trace.RequestedModel = ""
		trace.InputTokens = 0
		trace.OutputTokens = 0
		trace.ReasoningTokens = 0
		trace.CacheReadTokens = 0
		trace.CacheWriteTokens = 0
		trace.TTFTMs = 0
		return trace
	}
}

func TestMapTraceToSpanSnapshotRefusesRouteLikeFeatureWithoutSemanticType(t *testing.T) {
	for _, feature := range []string{"HTTP POST /api/v1/chat", "redis GET", "HTTP client openai-compatible"} {
		t.Run(feature, func(t *testing.T) {
			trace := NewTrace("not-an-ai-trace", feature, time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC))
			if _, err := MapTraceToSpanSnapshot(trace); err == nil {
				t.Fatal("mapper must not infer an AI marker from a route-like feature without explicit observation type")
			}
		})
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
