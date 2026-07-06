package obs

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOTelTracerContract(t *testing.T) {
	RunTracerContract(t, func(t *testing.T) (Tracer, TracerContractSink) {
		t.Helper()

		sink := newOTelContractSink()
		return NewOTelTracer(sink), sink
	})
}

func TestOTelTracerDropsTraceWithoutObservationType(t *testing.T) {
	sink := newOTelContractSink()
	tracer := NewOTelTracer(sink)

	tracer.Record(context.Background(), NewTrace(
		"trace-missing-observation-type",
		"agent_run",
		mustParseTime(t, "2026-07-05T10:00:00Z"),
		WithOutcome("success"),
	))

	if records := sink.Records(t); len(records) != 0 {
		t.Fatalf("record count = %d, want 0 when observation type is missing", len(records))
	}
}

type otelContractSink struct {
	mu        sync.Mutex
	snapshots []TraceSpanSnapshot
}

func newOTelContractSink() *otelContractSink {
	return &otelContractSink{}
}

func (s *otelContractSink) RecordTraceSpan(_ context.Context, snapshot TraceSpanSnapshot) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, cloneTraceSpanSnapshot(snapshot))
}

func (s *otelContractSink) Records(t *testing.T) []TracerContractRecord {
	t.Helper()

	snapshots := s.snapshotsCopy()
	records := make([]TracerContractRecord, 0, len(snapshots))
	for _, snapshot := range snapshots {
		records = append(records, otelSnapshotToContractRecord(snapshot))
	}
	return records
}

func (s *otelContractSink) RawPayload(t *testing.T) string {
	t.Helper()

	payload, err := json.Marshal(s.snapshotsCopy())
	if err != nil {
		t.Fatalf("marshal otel snapshots: %v", err)
	}
	return string(payload)
}

func (s *otelContractSink) snapshotsCopy() []TraceSpanSnapshot {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cloned := make([]TraceSpanSnapshot, len(s.snapshots))
	for index, snapshot := range s.snapshots {
		cloned[index] = cloneTraceSpanSnapshot(snapshot)
	}
	return cloned
}

func otelSnapshotToContractRecord(snapshot TraceSpanSnapshot) TracerContractRecord {
	attrs := snapshot.Attributes
	return TracerContractRecord{
		TraceID:               attrs["ai.trace_id"],
		TenantID:              attrs["ai.tenant_id"],
		UserID:                attrs["ai.user_id"],
		SessionID:             attrs["ai.session_id"],
		Feature:               attrs["ai.feature"],
		Timestamp:             attrs["ai.timestamp"],
		RequestID:             snapshot.RequestID,
		ServiceTraceID:        snapshot.ServiceTraceID,
		SpanID:                snapshot.ParentSpanID,
		ObservationType:       snapshot.ObservationType,
		FailureStatus:         attrs["ai.failure_status"],
		QueryHash:             attrs["ai.query.hash"],
		QueryLang:             attrs["ai.query.lang"],
		QueryLen:              intAttribute(attrs, "ai.query.length"),
		Model:                 attrs["ai.model"],
		PromptTemplateVersion: attrs["ai.prompt.template_version"],
		PromptHash:            attrs["ai.prompt.hash"],
		InputTokens:           intAttribute(attrs, "ai.usage.input_tokens"),
		OutputTokens:          intAttribute(attrs, "ai.usage.output_tokens"),
		ReasoningTokens:       intAttribute(attrs, "ai.usage.reasoning_tokens"),
		CacheReadTokens:       intAttribute(attrs, "ai.usage.cache_read_tokens"),
		CacheWriteTokens:      intAttribute(attrs, "ai.usage.cache_write_tokens"),
		Temperature:           floatAttribute(attrs, "ai.temperature"),
		TTFTMs:                int64Attribute(attrs, "ai.latency.ttft_ms"),
		TotalLatencyMs:        int64Attribute(attrs, "ai.latency.total_ms"),
		ChunksRetrieved:       intAttribute(attrs, "ai.retrieval.chunks"),
		QueryRewrittenHash:    attrs["ai.retrieval.query_rewrite_hash"],
		TopScores:             floatSliceAttribute(attrs, "ai.retrieval.top_scores"),
		RetrievalLatencyMs:    int64Attribute(attrs, "ai.latency.retrieval_ms"),
		QuerySummary:          snapshot.Summaries["query"],
		PromptSummary:         snapshot.Summaries["prompt"],
		RetrievalSummary:      snapshot.Summaries["retrieval"],
		ToolSummary:           snapshot.Summaries["tool"],
		CostUSD:               floatAttribute(attrs, "ai.cost_usd"),
		OutcomeStatus:         attrs["ai.outcome"],
		UserRating:            intPointerAttribute(attrs, "ai.feedback.user_rating"),
		AutoEvalScore:         floatPointerAttribute(attrs, "ai.feedback.auto_eval_score"),
	}
}

func intAttribute(attrs map[string]string, key string) int {
	value, _ := strconv.Atoi(attrs[key])
	return value
}

func int64Attribute(attrs map[string]string, key string) int64 {
	value, _ := strconv.ParseInt(attrs[key], 10, 64)
	return value
}

func floatAttribute(attrs map[string]string, key string) float64 {
	value, _ := strconv.ParseFloat(attrs[key], 64)
	return value
}

func floatSliceAttribute(attrs map[string]string, key string) []float64 {
	raw := strings.TrimSpace(attrs[key])
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]float64, len(parts))
	for index, part := range parts {
		values[index], _ = strconv.ParseFloat(part, 64)
	}
	return values
}

func intPointerAttribute(attrs map[string]string, key string) *int {
	raw := strings.TrimSpace(attrs[key])
	if raw == "" {
		return nil
	}

	value, _ := strconv.Atoi(raw)
	return &value
}

func floatPointerAttribute(attrs map[string]string, key string) *float64 {
	raw := strings.TrimSpace(attrs[key])
	if raw == "" {
		return nil
	}

	value, _ := strconv.ParseFloat(raw, 64)
	return &value
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}
