package vectordb

import (
	"context"
	"errors"
	"math"
	"testing"
)

// StoreFactory 为契约测试创建一个全新的 Store。
//
// 每个契约子测试都必须拿到独立 store，避免测试之间共享向量数据。
// 未来 pgvector、Milvus 或其它 adapter 接入时，只需要提供同样的 factory，
// 就能复用这套 Store 行为契约。
type StoreFactory func(t *testing.T) Store

func TestMemoryStoreContract(t *testing.T) {
	RunStoreContract(t, func(t *testing.T) Store {
		t.Helper()
		return NewMemoryStore(MemoryStoreConfig{Dimension: 3})
	})
}

// RunStoreContract 验证所有 Store 实现必须保持的用户可见语义。
//
// 这不是 MemoryStore 的私有单测，而是向量库 adapter 边界的公共验收：
// 后续无论底层是 pgvector、Milvus 还是本地 memory fake，RAG 上层都应看到一致的
// Upsert/Search/Delete/Health、metadata filter、threshold、context 和 defensive copy 行为。
func RunStoreContract(t *testing.T, newStore StoreFactory) {
	t.Helper()

	t.Run("health reports ready store", func(t *testing.T) {
		store := newStore(t)

		if err := store.Health(context.Background()); err != nil {
			t.Fatalf("Health() error = %v", err)
		}
	})

	t.Run("upsert search and delete preserve visible semantics", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		if err := store.Upsert(ctx, []Vector{
			{
				ID:        "doc-a",
				Embedding: []float32{1, 0, 0},
				Metadata:  map[string]any{"tenant_id": "tenant-a", "source": "a.md"},
			},
			{
				ID:        "doc-b",
				Embedding: []float32{0.7, 0.7, 0},
				Metadata:  map[string]any{"tenant_id": "tenant-a", "source": "b.md"},
			},
			{
				ID:        "doc-c",
				Embedding: []float32{0.9, 0.1, 0},
				Metadata:  map[string]any{"tenant_id": "tenant-b", "source": "c.md"},
			},
		}); err != nil {
			t.Fatalf("Upsert() error = %v", err)
		}

		hits, err := store.Search(ctx, Query{
			Vector: []float32{1, 0, 0},
			TopK:   2,
			Filter: map[string]any{"tenant_id": "tenant-a"},
		})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		assertContractHitIDs(t, hits, []string{"doc-a", "doc-b"})
		assertContractScoreNear(t, hits[0].Score, 1)
		if hits[0].Metadata["source"] != "a.md" {
			t.Fatalf("first hit source = %#v, want a.md", hits[0].Metadata["source"])
		}

		if err := store.Delete(ctx, []string{"doc-a", "missing-doc"}); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		hits, err = store.Search(ctx, Query{
			Vector: []float32{1, 0, 0},
			TopK:   3,
			Filter: map[string]any{"tenant_id": "tenant-a"},
		})
		if err != nil {
			t.Fatalf("Search() after delete error = %v", err)
		}
		assertContractHitIDs(t, hits, []string{"doc-b"})
	})

	t.Run("search applies metadata filter threshold and topK", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		if err := store.Upsert(ctx, []Vector{
			{
				ID:        "best",
				Embedding: []float32{1, 0, 0},
				Metadata:  map[string]any{"tenant_id": "tenant-a", "lang": "zh"},
			},
			{
				ID:        "second",
				Embedding: []float32{0.9, 0.1, 0},
				Metadata:  map[string]any{"tenant_id": "tenant-a", "lang": "zh"},
			},
			{
				ID:        "below-threshold",
				Embedding: []float32{0, 1, 0},
				Metadata:  map[string]any{"tenant_id": "tenant-a", "lang": "zh"},
			},
			{
				ID:        "wrong-lang",
				Embedding: []float32{1, 0, 0},
				Metadata:  map[string]any{"tenant_id": "tenant-a", "lang": "en"},
			},
		}); err != nil {
			t.Fatalf("Upsert() error = %v", err)
		}

		hits, err := store.Search(ctx, Query{
			Vector:    []float32{1, 0, 0},
			TopK:      1,
			Threshold: 0.8,
			Filter:    map[string]any{"tenant_id": "tenant-a", "lang": "zh"},
		})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		assertContractHitIDs(t, hits, []string{"best"})
	})

	t.Run("upsert overwrites existing id", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		if err := store.Upsert(ctx, []Vector{{
			ID:        "doc-updated",
			Embedding: []float32{1, 0, 0},
			Metadata:  map[string]any{"version": "old"},
		}}); err != nil {
			t.Fatalf("first Upsert() error = %v", err)
		}
		if err := store.Upsert(ctx, []Vector{{
			ID:        "doc-updated",
			Embedding: []float32{0, 1, 0},
			Metadata:  map[string]any{"version": "new"},
		}}); err != nil {
			t.Fatalf("second Upsert() error = %v", err)
		}

		hits, err := store.Search(ctx, Query{Vector: []float32{0, 1, 0}, TopK: 1})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		assertContractHitIDs(t, hits, []string{"doc-updated"})
		if hits[0].Metadata["version"] != "new" {
			t.Fatalf("hit version = %#v, want new", hits[0].Metadata["version"])
		}
	})

	t.Run("store does not expose mutable caller or hit metadata", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		embedding := []float32{1, 0, 0}
		metadata := map[string]any{
			"tenant_id": "tenant-a",
			"tags":      []string{"rag", "contract"},
		}

		if err := store.Upsert(ctx, []Vector{{
			ID:        "doc-copy",
			Embedding: embedding,
			Metadata:  metadata,
		}}); err != nil {
			t.Fatalf("Upsert() error = %v", err)
		}

		embedding[0] = 0
		metadata["tenant_id"] = "mutated"
		metadata["tags"].([]string)[0] = "mutated"

		hits, err := store.Search(ctx, Query{
			Vector: []float32{1, 0, 0},
			TopK:   1,
			Filter: map[string]any{"tenant_id": "tenant-a"},
		})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		assertContractHitIDs(t, hits, []string{"doc-copy"})

		hits[0].Metadata["tenant_id"] = "mutated-hit"
		hits[0].Metadata["tags"].([]string)[0] = "mutated-hit"

		hits, err = store.Search(ctx, Query{
			Vector: []float32{1, 0, 0},
			TopK:   1,
			Filter: map[string]any{"tenant_id": "tenant-a"},
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
	})

	t.Run("context cancellation is returned before work", func(t *testing.T) {
		store := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := store.Health(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Health() error = %v, want context.Canceled", err)
		}
		if err := store.Upsert(ctx, []Vector{{ID: "doc", Embedding: []float32{1, 0, 0}}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Upsert() error = %v, want context.Canceled", err)
		}
		if _, err := store.Search(ctx, Query{Vector: []float32{1, 0, 0}, TopK: 1}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Search() error = %v, want context.Canceled", err)
		}
		if err := store.Delete(ctx, []string{"doc"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Delete() error = %v, want context.Canceled", err)
		}
	})
}

func assertContractHitIDs(t *testing.T, hits []Hit, want []string) {
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

func assertContractScoreNear(t *testing.T, got float64, want float64) {
	t.Helper()

	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("score = %v, want near %v", got, want)
	}
}
