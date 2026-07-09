package testutil

import (
	"context"
	"testing"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

func TestStaticDatasetLoadReturnsSamples(t *testing.T) {
	t.Parallel()

	dataset := NewStaticDataset([]aieval.Sample{
		{
			ID:          "sample-001",
			Query:       "什么是 P0 最小闭环？",
			GroundTruth: "prompt -> llm -> obs -> eval",
			RelevantCtx: []string{"P0 目标是先打通最小工程闭环"},
			Difficulty:  "simple",
			Category:    "p0",
			Meta:        map[string]any{"source": "test"},
		},
	})

	samples, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("Load() returned %d samples, want 1", len(samples))
	}
	if samples[0].ID != "sample-001" {
		t.Fatalf("sample ID = %q, want sample-001", samples[0].ID)
	}
	if samples[0].RelevantCtx[0] != "P0 目标是先打通最小工程闭环" {
		t.Fatalf("RelevantCtx[0] = %q", samples[0].RelevantCtx[0])
	}
}

func TestStaticDatasetLoadReturnsImmutableSampleCopies(t *testing.T) {
	t.Parallel()

	dataset := NewStaticDataset([]aieval.Sample{
		{
			ID:          "sample-copy",
			Query:       "如何验证不可变副本？",
			RelevantCtx: []string{"original context"},
			Meta: map[string]any{
				"key":    "original",
				"nested": map[string]any{"value": "original"},
				"labels": []any{"original"},
			},
		},
	})

	first, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	first[0].ID = "mutated"
	first[0].RelevantCtx[0] = "mutated context"
	first[0].Meta["key"] = "mutated"
	first[0].Meta["nested"].(map[string]any)["value"] = "mutated"
	first[0].Meta["labels"].([]any)[0] = "mutated"

	second, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if second[0].ID != "sample-copy" {
		t.Fatalf("ID = %q, want original ID", second[0].ID)
	}
	if second[0].RelevantCtx[0] != "original context" {
		t.Fatalf("RelevantCtx[0] = %q, want original context", second[0].RelevantCtx[0])
	}
	if second[0].Meta["key"] != "original" {
		t.Fatalf("Meta[key] = %q, want original", second[0].Meta["key"])
	}
	if second[0].Meta["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("Meta[nested][value] = %q, want original", second[0].Meta["nested"].(map[string]any)["value"])
	}
	if second[0].Meta["labels"].([]any)[0] != "original" {
		t.Fatalf("Meta[labels][0] = %q, want original", second[0].Meta["labels"].([]any)[0])
	}
}

func TestStaticDatasetLoadHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	dataset := NewStaticDataset([]aieval.Sample{{ID: "sample-cancel", Query: "cancel"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := dataset.Load(ctx)
	if err == nil {
		t.Fatal("Load() error = nil, want context cancellation error")
	}
}
