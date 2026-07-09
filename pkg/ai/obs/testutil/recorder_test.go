package testutil

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestRecorderRecordsTraceAndAllowsFieldAssertions(t *testing.T) {
	t.Parallel()

	recorder := NewRecorder()
	trace := obs.Trace{
		TraceID:           "trace-001",
		Feature:           "p0_smoke",
		Timestamp:         time.Unix(1, 0),
		Model:             "fake-model",
		PromptTemplateVer: "v1",
		PromptHash:        "abc123",
		InputTokens:       10,
		OutputTokens:      5,
		TotalLatencyMs:    42,
	}

	recorder.Record(context.Background(), trace)

	if got := recorder.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
	recorder.AssertCount(t, 1)
	recorder.AssertTrace(t, 0, func(t *testing.T, got obs.Trace) {
		t.Helper()

		if got.TraceID != trace.TraceID {
			t.Fatalf("TraceID = %q, want %q", got.TraceID, trace.TraceID)
		}
		if got.Model != trace.Model {
			t.Fatalf("Model = %q, want %q", got.Model, trace.Model)
		}
		if got.PromptHash != trace.PromptHash {
			t.Fatalf("PromptHash = %q, want %q", got.PromptHash, trace.PromptHash)
		}
	})
}

func TestRecorderReturnsTraceCopies(t *testing.T) {
	t.Parallel()

	recorder := NewRecorder()
	recorder.Record(context.Background(), obs.Trace{
		TraceID:   "trace-copy",
		Feature:   "copy_test",
		Timestamp: time.Unix(1, 0),
		TopScores: []float64{0.9, 0.8},
	})

	traces := recorder.Traces()
	traces[0].TraceID = "mutated"
	traces[0].TopScores[0] = 0

	recorder.AssertTrace(t, 0, func(t *testing.T, got obs.Trace) {
		t.Helper()

		if got.TraceID != "trace-copy" {
			t.Fatalf("TraceID = %q, want original copy", got.TraceID)
		}
		if got.TopScores[0] != 0.9 {
			t.Fatalf("TopScores[0] = %v, want 0.9", got.TopScores[0])
		}
	})
}

func TestRecorderSupportsConcurrentRecord(t *testing.T) {
	t.Parallel()

	recorder := NewRecorder()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			recorder.Record(context.Background(), obs.Trace{
				TraceID:   fmt.Sprintf("trace-%02d", i),
				Feature:   "concurrent_test",
				Timestamp: time.Unix(int64(i), 0),
			})
		}(i)
	}
	wg.Wait()

	recorder.AssertCount(t, 50)
}
