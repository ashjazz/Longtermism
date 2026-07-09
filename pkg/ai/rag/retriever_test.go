package rag

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/vectordb"
)

func TestBasicRetrieverReturnsChunksAndPassesFilter(t *testing.T) {
	wantFilter := map[string]any{
		"tenant_id": "tenant-a",
		"lang":      "zh",
	}
	store := &spyStore{
		searchHits: []vectordb.Hit{
			{
				ID:    "chunk-a",
				Score: 0.91,
				Metadata: map[string]any{
					"content":   "alpha context",
					"parent_id": "doc-a",
					"tenant_id": "tenant-a",
					"source":    "a.md",
				},
			},
			{
				ID:    "chunk-b",
				Score: 0.82,
				Metadata: map[string]any{
					"content":   "beta context",
					"parent_id": "doc-b",
					"tenant_id": "tenant-a",
					"source":    "b.md",
				},
			},
		},
	}
	retriever := NewBasicRetriever(BasicRetrieverConfig{
		Embedder: &stubEmbedder{
			vectors: [][]float32{{1, 0, 0}},
		},
		Store: store,
	})

	chunks, err := retriever.Retrieve(context.Background(), "如何构建 P0 闭环？", 2, wantFilter)

	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if store.searchCalls != 1 {
		t.Fatalf("store search calls = %d, want 1", store.searchCalls)
	}
	if !reflect.DeepEqual(store.lastQuery.Filter, wantFilter) {
		t.Fatalf("store filter = %#v, want %#v", store.lastQuery.Filter, wantFilter)
	}
	if store.lastQuery.TopK != 2 {
		t.Fatalf("store topK = %d, want 2", store.lastQuery.TopK)
	}
	if !reflect.DeepEqual(store.lastQuery.Vector, []float32{1, 0, 0}) {
		t.Fatalf("store vector = %#v, want embedded query vector", store.lastQuery.Vector)
	}
	assertRetrievedChunks(t, chunks, []wantRetrievedChunk{
		{
			id:       "chunk-a",
			content:  "alpha context",
			parentID: "doc-a",
			score:    0.91,
			metadata: map[string]any{
				"tenant_id": "tenant-a",
				"source":    "a.md",
			},
		},
		{
			id:       "chunk-b",
			content:  "beta context",
			parentID: "doc-b",
			score:    0.82,
			metadata: map[string]any{
				"tenant_id": "tenant-a",
				"source":    "b.md",
			},
		},
	})
}

func TestBasicRetrieverReturnsEmptyWhenStoreHasNoHits(t *testing.T) {
	retriever := NewBasicRetriever(BasicRetrieverConfig{
		Embedder: &stubEmbedder{
			vectors: [][]float32{{1, 0}},
		},
		Store: &spyStore{},
	})

	chunks, err := retriever.Retrieve(context.Background(), "没有匹配上下文", 3, map[string]any{
		"tenant_id": "tenant-a",
	})

	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks length = %d, want 0", len(chunks))
	}
}

func TestBasicRetrieverErrorDoesNotLeakRawQuery(t *testing.T) {
	rawQuery := "用户手机号 13800138000 的订单在哪里？"
	tests := []struct {
		name   string
		config BasicRetrieverConfig
	}{
		{
			name: "embedder error",
			config: BasicRetrieverConfig{
				Embedder: &stubEmbedder{
					err: errors.New("embedding backend unavailable"),
				},
				Store: &spyStore{},
			},
		},
		{
			name: "store error",
			config: BasicRetrieverConfig{
				Embedder: &stubEmbedder{
					vectors: [][]float32{{1, 0}},
				},
				Store: &spyStore{
					searchErr: errors.New("vector backend unavailable"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retriever := NewBasicRetriever(tt.config)

			_, err := retriever.Retrieve(context.Background(), rawQuery, 1, nil)

			if err == nil {
				t.Fatalf("Retrieve() error = nil, want error")
			}
			if strings.Contains(err.Error(), rawQuery) || strings.Contains(err.Error(), "13800138000") {
				t.Fatalf("error leaks raw query: %q", err.Error())
			}
			if !strings.Contains(err.Error(), "query_hash=") {
				t.Fatalf("error = %q, want query_hash diagnostic", err.Error())
			}
		})
	}
}

func TestBasicRetrieverRejectsEmptyEmbedding(t *testing.T) {
	retriever := NewBasicRetriever(BasicRetrieverConfig{
		Embedder: &stubEmbedder{
			vectors: [][]float32{{}},
		},
		Store: &spyStore{},
	})

	_, err := retriever.Retrieve(context.Background(), "query", 1, nil)

	if err == nil {
		t.Fatalf("Retrieve() error = nil, want empty embedding error")
	}
}

func TestBasicRetrieverRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		config BasicRetrieverConfig
		query  string
		topK   int
	}{
		{
			name: "embedder is required",
			config: BasicRetrieverConfig{
				Store: &spyStore{},
			},
			query: "query",
			topK:  1,
		},
		{
			name: "store is required",
			config: BasicRetrieverConfig{
				Embedder: &stubEmbedder{vectors: [][]float32{{1, 0}}},
			},
			query: "query",
			topK:  1,
		},
		{
			name: "query is required",
			config: BasicRetrieverConfig{
				Embedder: &stubEmbedder{vectors: [][]float32{{1, 0}}},
				Store:    &spyStore{},
			},
			query: " ",
			topK:  1,
		},
		{
			name: "topK is required",
			config: BasicRetrieverConfig{
				Embedder: &stubEmbedder{vectors: [][]float32{{1, 0}}},
				Store:    &spyStore{},
			},
			query: "query",
			topK:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retriever := NewBasicRetriever(tt.config)

			_, err := retriever.Retrieve(context.Background(), tt.query, tt.topK, nil)

			if err == nil {
				t.Fatalf("Retrieve() error = nil, want validation error")
			}
		})
	}
}

type stubEmbedder struct {
	vectors [][]float32
	err     error
}

func (e *stubEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.err != nil {
		return nil, e.err
	}
	if len(texts) != 1 {
		return nil, fmt.Errorf("texts length = %d, want 1", len(texts))
	}
	return cloneFloat32Matrix(e.vectors), nil
}

func (e *stubEmbedder) Dim() int {
	if len(e.vectors) == 0 {
		return 0
	}
	return len(e.vectors[0])
}

type spyStore struct {
	searchCalls int
	lastQuery   vectordb.Query
	searchHits  []vectordb.Hit
	searchErr   error
}

func (s *spyStore) Upsert(context.Context, []vectordb.Vector) error {
	return nil
}

func (s *spyStore) Search(ctx context.Context, q vectordb.Query) ([]vectordb.Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.searchCalls++
	s.lastQuery = cloneQuery(q)
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return cloneHits(s.searchHits), nil
}

func (s *spyStore) Delete(context.Context, []string) error {
	return nil
}

func (s *spyStore) Health(context.Context) error {
	return nil
}

type wantRetrievedChunk struct {
	id       string
	content  string
	parentID string
	score    float64
	metadata map[string]any
}

func assertRetrievedChunks(t *testing.T, got []Chunk, want []wantRetrievedChunk) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("chunks length = %d, want %d: %#v", len(got), len(want), got)
	}
	for index, wantChunk := range want {
		chunk := got[index]
		if chunk.ID != wantChunk.id {
			t.Fatalf("chunk[%d].ID = %q, want %q", index, chunk.ID, wantChunk.id)
		}
		if chunk.Content != wantChunk.content {
			t.Fatalf("chunk[%d].Content = %q, want %q", index, chunk.Content, wantChunk.content)
		}
		if chunk.ParentID != wantChunk.parentID {
			t.Fatalf("chunk[%d].ParentID = %q, want %q", index, chunk.ParentID, wantChunk.parentID)
		}
		if chunk.Score != wantChunk.score {
			t.Fatalf("chunk[%d].Score = %v, want %v", index, chunk.Score, wantChunk.score)
		}
		for key, wantValue := range wantChunk.metadata {
			if gotValue := chunk.Metadata[key]; gotValue != wantValue {
				t.Fatalf("chunk[%d].Metadata[%q] = %#v, want %#v", index, key, gotValue, wantValue)
			}
		}
	}
}

func cloneQuery(query vectordb.Query) vectordb.Query {
	return vectordb.Query{
		Vector:    append([]float32(nil), query.Vector...),
		TopK:      query.TopK,
		Filter:    cloneMap(query.Filter),
		Threshold: query.Threshold,
	}
}

func cloneHits(hits []vectordb.Hit) []vectordb.Hit {
	cloned := make([]vectordb.Hit, len(hits))
	for index, hit := range hits {
		cloned[index] = vectordb.Hit{
			ID:       hit.ID,
			Score:    hit.Score,
			Metadata: cloneMap(hit.Metadata),
		}
	}
	return cloned
}

func cloneFloat32Matrix(values [][]float32) [][]float32 {
	cloned := make([][]float32, len(values))
	for index, value := range values {
		cloned[index] = append([]float32(nil), value...)
	}
	return cloned
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
