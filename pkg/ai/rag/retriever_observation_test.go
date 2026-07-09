package rag

import (
	"context"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"github.com/ashjazz/Longtermism/pkg/ai/obs/testutil"
	"github.com/ashjazz/Longtermism/pkg/ai/vectordb"
)

func TestBasicRetrieverRecordsRetrievalObservation(t *testing.T) {
	identity := obs.NewCorrelationIdentity(
		"req-rag-observation-001",
		obs.WithServiceSpan("svc-trace-rag-observation-001", "span-rag-observation-001"),
		obs.WithAITraceID("ai-trace-rag-observation-001"),
	)
	recorder := testutil.NewRecorder()
	retriever := NewBasicRetriever(BasicRetrieverConfig{
		Embedder: &stubEmbedder{
			vectors: [][]float32{{1, 0, 0}},
		},
		Store: &spyStore{
			searchHits: []vectordb.Hit{
				{
					ID:    "chunk-high-score",
					Score: 0.92,
					Metadata: map[string]any{
						"content":   "high score context",
						"parent_id": "doc-a",
					},
				},
				{
					ID:    "chunk-second-score",
					Score: 0.81,
					Metadata: map[string]any{
						"content":   "second score context",
						"parent_id": "doc-b",
					},
				},
			},
		},
		Tracer:  recorder,
		Feature: "rag_qa",
		Now: scriptedNow(
			time.Date(2026, time.July, 6, 10, 0, 0, 0, time.UTC),
			time.Date(2026, time.July, 6, 10, 0, 0, 37*int(time.Millisecond), time.UTC),
		),
	})
	ctx := obs.ContextWithCorrelationIdentity(context.Background(), identity)

	chunks, err := retriever.Retrieve(ctx, "如何观察 RAG 检索质量？", 2, nil)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks length = %d, want 2", len(chunks))
	}

	recorder.AssertCount(t, 1)
	recorder.AssertTrace(t, 0, func(t *testing.T, trace obs.Trace) {
		t.Helper()

		assertRetrieverTraceIdentity(t, trace, identity)
		assertRetrieverTraceOutcome(t, trace, "success", "", 2)
		assertRetrieverTraceScores(t, trace, []float64{0.92, 0.81})
		if trace.RetrievalLatencyMs != 37 {
			t.Fatalf("RetrievalLatencyMs = %d, want 37", trace.RetrievalLatencyMs)
		}
	})
}

func TestBasicRetrieverRecordsRetrievalMissObservation(t *testing.T) {
	identity := obs.NewCorrelationIdentity(
		"req-rag-miss-001",
		obs.WithServiceSpan("svc-trace-rag-miss-001", "span-rag-miss-001"),
		obs.WithAITraceID("ai-trace-rag-miss-001"),
	)
	recorder := testutil.NewRecorder()
	retriever := NewBasicRetriever(BasicRetrieverConfig{
		Embedder: &stubEmbedder{
			vectors: [][]float32{{1, 0}},
		},
		Store:   &spyStore{},
		Tracer:  recorder,
		Feature: "rag_qa",
		Now: scriptedNow(
			time.Date(2026, time.July, 6, 10, 1, 0, 0, time.UTC),
			time.Date(2026, time.July, 6, 10, 1, 0, 12*int(time.Millisecond), time.UTC),
		),
	})
	ctx := obs.ContextWithCorrelationIdentity(context.Background(), identity)

	chunks, err := retriever.Retrieve(ctx, "没有命中的检索问题", 3, nil)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks length = %d, want 0", len(chunks))
	}

	recorder.AssertCount(t, 1)
	recorder.AssertTrace(t, 0, func(t *testing.T, trace obs.Trace) {
		t.Helper()

		assertRetrieverTraceIdentity(t, trace, identity)
		assertRetrieverTraceOutcome(t, trace, "failure", string(obs.FailureRetrievalMiss), 0)
		if trace.RetrievalLatencyMs != 12 {
			t.Fatalf("RetrievalLatencyMs = %d, want 12", trace.RetrievalLatencyMs)
		}
		if len(trace.TopScores) != 0 {
			t.Fatalf("TopScores length = %d, want 0", len(trace.TopScores))
		}
		if trace.RetrievalSummary.Status != "miss" {
			t.Fatalf("RetrievalSummary.Status = %q, want miss", trace.RetrievalSummary.Status)
		}
	})
}

func assertRetrieverTraceIdentity(t *testing.T, trace obs.Trace, identity obs.CorrelationIdentity) {
	t.Helper()

	if trace.TraceID != identity.AITraceID {
		t.Fatalf("TraceID = %q, want ai_trace_id %q", trace.TraceID, identity.AITraceID)
	}
	if trace.RequestID != identity.RequestID {
		t.Fatalf("RequestID = %q, want %q", trace.RequestID, identity.RequestID)
	}
	if trace.ServiceTraceID != identity.ServiceTraceID {
		t.Fatalf("ServiceTraceID = %q, want %q", trace.ServiceTraceID, identity.ServiceTraceID)
	}
	if trace.SpanID != identity.SpanID {
		t.Fatalf("SpanID = %q, want %q", trace.SpanID, identity.SpanID)
	}
	if trace.ObservationType != obs.ObservationTypeRetriever {
		t.Fatalf("ObservationType = %q, want %q", trace.ObservationType, obs.ObservationTypeRetriever)
	}
}

func assertRetrieverTraceOutcome(t *testing.T, trace obs.Trace, wantOutcome, wantFailure string, wantCount int) {
	t.Helper()

	if trace.Feature != "rag_qa" {
		t.Fatalf("Feature = %q, want rag_qa", trace.Feature)
	}
	if trace.OutcomeStatus != wantOutcome {
		t.Fatalf("OutcomeStatus = %q, want %q", trace.OutcomeStatus, wantOutcome)
	}
	if trace.FailureStatus != wantFailure {
		t.Fatalf("FailureStatus = %q, want %q", trace.FailureStatus, wantFailure)
	}
	if trace.ChunksRetrieved != wantCount {
		t.Fatalf("ChunksRetrieved = %d, want %d", trace.ChunksRetrieved, wantCount)
	}
	if trace.RetrievalSummary.Count != wantCount {
		t.Fatalf("RetrievalSummary.Count = %d, want %d", trace.RetrievalSummary.Count, wantCount)
	}
	if trace.RetrievalSummary.ErrorClass != wantFailure {
		t.Fatalf("RetrievalSummary.ErrorClass = %q, want %q", trace.RetrievalSummary.ErrorClass, wantFailure)
	}
}

func assertRetrieverTraceScores(t *testing.T, trace obs.Trace, want []float64) {
	t.Helper()

	if len(trace.TopScores) != len(want) {
		t.Fatalf("TopScores length = %d, want %d: %#v", len(trace.TopScores), len(want), trace.TopScores)
	}
	for index, wantScore := range want {
		if trace.TopScores[index] != wantScore {
			t.Fatalf("TopScores[%d] = %v, want %v", index, trace.TopScores[index], wantScore)
		}
	}
	if trace.RetrievalSummary.Score == nil {
		t.Fatalf("RetrievalSummary.Score = nil, want top score summary")
	}
	if *trace.RetrievalSummary.Score != want[0] {
		t.Fatalf("RetrievalSummary.Score = %v, want %v", *trace.RetrievalSummary.Score, want[0])
	}
	if trace.RetrievalSummary.Status != "success" {
		t.Fatalf("RetrievalSummary.Status = %q, want success", trace.RetrievalSummary.Status)
	}
}

func scriptedNow(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if len(times) == 0 {
			return time.Time{}
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		now := times[index]
		index++
		return now
	}
}
