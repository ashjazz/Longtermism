package testutil

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestMemorySpanSinkRecordsSnapshotsInOrder(t *testing.T) {
	t.Parallel()

	sink := NewMemorySpanSink()
	sink.Record(SpanSnapshot{
		Name:            "http.request",
		RequestID:       "req-001",
		ServiceTraceID:  "svc-trace-001",
		SpanID:          "span-001",
		ObservationType: obs.ObservationTypeGeneration,
		Attributes: map[string]string{
			"http.method": "POST",
			"http.route":  "/api/v1/chat",
		},
		Summaries: map[string]obs.SafeSummary{
			"query": obs.NewSafeSummary(obs.WithSummaryHash("sha256:query"), obs.WithSummaryLength(42)),
		},
	})
	sink.Record(SpanSnapshot{
		Name:           "ai.generation",
		RequestID:      "req-001",
		ServiceTraceID: "svc-trace-001",
		SpanID:         "span-002",
		ParentSpanID:   "span-001",
		Attributes: map[string]string{
			"model": "fake-model",
		},
	})

	snapshots := sink.Snapshots()
	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(snapshots))
	}
	if snapshots[0].Name != "http.request" || snapshots[1].Name != "ai.generation" {
		t.Fatalf("snapshot order = (%q, %q), want http.request then ai.generation", snapshots[0].Name, snapshots[1].Name)
	}
	if snapshots[1].ParentSpanID != "span-001" {
		t.Fatalf("ParentSpanID = %q, want span-001", snapshots[1].ParentSpanID)
	}
	if snapshots[0].Summaries["query"].Hash != "sha256:query" {
		t.Fatalf("query summary = %#v, want query hash", snapshots[0].Summaries["query"])
	}
}

func TestMemorySpanSinkReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()

	sink := NewMemorySpanSink()
	sink.Record(SpanSnapshot{
		Name:      "safe.copy",
		RequestID: "req-copy",
		Attributes: map[string]string{
			"outcome": "success",
		},
		Summaries: map[string]obs.SafeSummary{
			"query": obs.NewSafeSummary(obs.WithSummaryHash("sha256:query"), obs.WithSummaryScore(0.9)),
		},
	})

	snapshots := sink.Snapshots()
	snapshots[0].Name = "mutated"
	snapshots[0].Attributes["outcome"] = "mutated"
	*snapshots[0].Summaries["query"].Score = 0.1

	snapshotsAgain := sink.Snapshots()
	if snapshotsAgain[0].Name != "safe.copy" {
		t.Fatalf("Name = %q, want safe.copy", snapshotsAgain[0].Name)
	}
	if snapshotsAgain[0].Attributes["outcome"] != "success" {
		t.Fatalf("attribute outcome = %q, want success", snapshotsAgain[0].Attributes["outcome"])
	}
	if snapshotsAgain[0].Summaries["query"].Score == nil || *snapshotsAgain[0].Summaries["query"].Score != 0.9 {
		t.Fatalf("summary score = %#v, want 0.9", snapshotsAgain[0].Summaries["query"].Score)
	}
}

func TestMemorySpanSinkSupportsConcurrentRecord(t *testing.T) {
	t.Parallel()

	sink := NewMemorySpanSink()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			sink.Record(SpanSnapshot{
				Name:      fmt.Sprintf("span-%02d", i),
				RequestID: "req-concurrent",
				SpanID:    fmt.Sprintf("span-id-%02d", i),
			})
		}(i)
	}
	wg.Wait()

	if got := len(sink.Snapshots()); got != 50 {
		t.Fatalf("snapshot count = %d, want 50", got)
	}
}

func TestMemorySpanSinkRawPayloadDoesNotExposeSensitiveContent(t *testing.T) {
	t.Parallel()

	const rawQuery = "请查询身份证号 110101199001011234 的账户余额"
	sink := NewMemorySpanSink()
	sink.Record(SpanSnapshot{
		Name:      "privacy.span",
		RequestID: "req-privacy",
		Attributes: map[string]string{
			"query_hash": "sha256:query",
			"status":     "blocked",
		},
		Summaries: map[string]obs.SafeSummary{
			"query": obs.NewSafeSummary(
				obs.WithSummaryHash("sha256:query"),
				obs.WithSummaryLength(len([]rune(rawQuery))),
				obs.WithSummaryCategory("contains_pii"),
			),
		},
	})

	rawPayload := sink.RawPayload()
	for _, forbidden := range []string{
		rawQuery,
		"110101199001011234",
		"raw_query",
		"prompt_content",
		"tool_args",
		"api_key",
	} {
		if strings.Contains(rawPayload, forbidden) {
			t.Fatalf("RawPayload leaked forbidden content %q: %s", forbidden, rawPayload)
		}
	}
}
