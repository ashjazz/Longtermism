package vectordb

import (
	"context"
	"math"
	"testing"
)

func TestMemoryStoreUpsertSearchDeleteAndHealth(t *testing.T) {
	store := NewMemoryStore(MemoryStoreConfig{Dimension: 3})
	ctx := context.Background()

	if err := store.Health(ctx); err != nil {
		t.Fatalf("Health() error = %v", err)
	}

	// 这组向量刻意选择容易手算的方向：
	// doc-a 与 query 完全同向，doc-b 夹角 45 度，doc-c 被 metadata filter 排除。
	err := store.Upsert(ctx, []Vector{
		{
			ID:        "doc-a",
			Embedding: []float32{1, 0, 0},
			Metadata: map[string]any{
				"tenant_id": "tenant-a",
				"source":    "a.md",
			},
		},
		{
			ID:        "doc-b",
			Embedding: []float32{0.7, 0.7, 0},
			Metadata: map[string]any{
				"tenant_id": "tenant-a",
				"source":    "b.md",
			},
		},
		{
			ID:        "doc-c",
			Embedding: []float32{0.9, 0.1, 0},
			Metadata: map[string]any{
				"tenant_id": "tenant-b",
				"source":    "c.md",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	hits, err := store.Search(ctx, Query{
		Vector: []float32{1, 0, 0},
		TopK:   2,
		Filter: map[string]any{
			"tenant_id": "tenant-a",
		},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	assertHitIDs(t, hits, []string{"doc-a", "doc-b"})
	assertNear(t, hits[0].Score, 1)
	if hits[0].Metadata["source"] != "a.md" {
		t.Fatalf("first hit source = %#v, want a.md", hits[0].Metadata["source"])
	}

	if err := store.Delete(ctx, []string{"doc-a"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	hits, err = store.Search(ctx, Query{
		Vector: []float32{1, 0, 0},
		TopK:   3,
		Filter: map[string]any{
			"tenant_id": "tenant-a",
		},
	})
	if err != nil {
		t.Fatalf("Search() after delete error = %v", err)
	}
	assertHitIDs(t, hits, []string{"doc-b"})
}

func TestMemoryStoreSearchAppliesThresholdAndMetadataFilter(t *testing.T) {
	store := NewMemoryStore(MemoryStoreConfig{Dimension: 2})
	ctx := context.Background()

	if err := store.Upsert(ctx, []Vector{
		{
			ID:        "high-score-match",
			Embedding: []float32{1, 0},
			Metadata: map[string]any{
				"tenant_id": "tenant-a",
				"lang":      "zh",
			},
		},
		{
			ID:        "low-score-match",
			Embedding: []float32{0, 1},
			Metadata: map[string]any{
				"tenant_id": "tenant-a",
				"lang":      "zh",
			},
		},
		{
			ID:        "wrong-lang",
			Embedding: []float32{1, 0},
			Metadata: map[string]any{
				"tenant_id": "tenant-a",
				"lang":      "en",
			},
		},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	hits, err := store.Search(ctx, Query{
		Vector:    []float32{1, 0},
		TopK:      5,
		Threshold: 0.8,
		Filter: map[string]any{
			"tenant_id": "tenant-a",
			"lang":      "zh",
		},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	assertHitIDs(t, hits, []string{"high-score-match"})
}

func TestMemoryStoreDefensivelyCopiesVectorsAndMetadata(t *testing.T) {
	store := NewMemoryStore(MemoryStoreConfig{Dimension: 2})
	ctx := context.Background()
	embedding := []float32{1, 0}
	metadata := map[string]any{
		"tenant_id": "tenant-a",
		"tags":      []string{"rag", "memory"},
	}

	if err := store.Upsert(ctx, []Vector{
		{
			ID:        "doc-copy",
			Embedding: embedding,
			Metadata:  metadata,
		},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	embedding[0] = 0
	metadata["tenant_id"] = "mutated"
	metadata["tags"].([]string)[0] = "mutated"

	hits, err := store.Search(ctx, Query{
		Vector: []float32{1, 0},
		TopK:   1,
		Filter: map[string]any{
			"tenant_id": "tenant-a",
		},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	assertHitIDs(t, hits, []string{"doc-copy"})

	hits[0].Metadata["tenant_id"] = "mutated-hit"
	hits[0].Metadata["tags"].([]string)[0] = "mutated-hit"

	hits, err = store.Search(ctx, Query{
		Vector: []float32{1, 0},
		TopK:   1,
		Filter: map[string]any{
			"tenant_id": "tenant-a",
		},
	})
	if err != nil {
		t.Fatalf("Search() second error = %v", err)
	}
	if hits[0].Metadata["tenant_id"] != "tenant-a" {
		t.Fatalf("hit tenant_id = %#v, want tenant-a", hits[0].Metadata["tenant_id"])
	}
	if hits[0].Metadata["tags"].([]string)[0] != "rag" {
		t.Fatalf("hit tags = %#v, want original tags", hits[0].Metadata["tags"])
	}
}

func TestMemoryStoreRejectsInvalidVectorsAndQueries(t *testing.T) {
	store := NewMemoryStore(MemoryStoreConfig{Dimension: 3})
	ctx := context.Background()

	if err := store.Upsert(ctx, []Vector{{ID: "wrong-dim", Embedding: []float32{1, 0}}}); err == nil {
		t.Fatalf("Upsert() error = nil, want dimension error")
	}
	if _, err := store.Search(ctx, Query{Vector: []float32{1, 0}, TopK: 1}); err == nil {
		t.Fatalf("Search() error = nil, want query dimension error")
	}
	if _, err := store.Search(ctx, Query{Vector: []float32{1, 0, 0}, TopK: 0}); err == nil {
		t.Fatalf("Search() error = nil, want topK error")
	}
}

func TestMemoryStoreRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryStore(MemoryStoreConfig{Dimension: 2})

	if err := store.Health(ctx); err == nil {
		t.Fatalf("Health() error = nil, want context cancellation error")
	}
	if err := store.Upsert(ctx, []Vector{{ID: "doc", Embedding: []float32{1, 0}}}); err == nil {
		t.Fatalf("Upsert() error = nil, want context cancellation error")
	}
	if _, err := store.Search(ctx, Query{Vector: []float32{1, 0}, TopK: 1}); err == nil {
		t.Fatalf("Search() error = nil, want context cancellation error")
	}
	if err := store.Delete(ctx, []string{"doc"}); err == nil {
		t.Fatalf("Delete() error = nil, want context cancellation error")
	}
}

func assertHitIDs(t *testing.T, hits []Hit, want []string) {
	t.Helper()

	if len(hits) != len(want) {
		t.Fatalf("hits length = %d, want %d: %#v", len(hits), len(want), hits)
	}
	for index, wantID := range want {
		if hits[index].ID != wantID {
			t.Fatalf("hit[%d].ID = %q, want %q", index, hits[index].ID, wantID)
		}
	}
}

func assertNear(t *testing.T, got float64, want float64) {
	t.Helper()

	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("score = %v, want near %v", got, want)
	}
}
