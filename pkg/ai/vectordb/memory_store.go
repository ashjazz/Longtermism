package vectordb

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// MemoryStoreConfig 是内存向量库的装配配置。
//
// Dimension 必须和所有写入/查询向量一致。真实向量库通常会在 collection/schema
// 层面固定维度；本地内存实现也保留这个边界，避免测试里悄悄混入不可比较的向量。
type MemoryStoreConfig struct {
	Dimension int
}

// MemoryStore 是 Store 的内存实现，仅用于测试、本地 smoke 和教学 demo。
//
// 它不代表 pgvector、Milvus 或其它生产向量库选型；P2 先用它打通 RAG 链路，是为了让
// retriever、metadata filter 和 retrieval metrics 可以在无外部服务环境下稳定回归。
type MemoryStore struct {
	mu        sync.RWMutex
	dimension int
	vectors   map[string]Vector
}

// NewMemoryStore 创建内存向量库。
func NewMemoryStore(config MemoryStoreConfig) *MemoryStore {
	return &MemoryStore{
		dimension: config.Dimension,
		vectors:   make(map[string]Vector),
	}
}

// Upsert 写入或覆盖向量记录。
//
// 输入向量与 metadata 都会被复制后再存储，防止调用方复用 slice/map 时污染 store 内部状态。
func (s *MemoryStore) Upsert(ctx context.Context, vecs []Vector) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := s.validateReady(); err != nil {
		return err
	}

	cloned := make([]Vector, len(vecs))
	for index, vector := range vecs {
		if err := validateVector(vector, s.dimension); err != nil {
			return fmt.Errorf("upsert vector at index %d: %w", index, err)
		}
		cloned[index] = cloneVector(vector)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, vector := range cloned {
		s.vectors[vector.ID] = vector
	}
	return nil
}

// Search 按 cosine similarity 返回命中结果。
//
// metadata filter 采用精确匹配语义：filter 中的每个 key/value 都必须在记录 metadata
// 中存在且相等。这是防止跨租户、跨语言、跨来源污染的最小本地语义。
func (s *MemoryStore) Search(ctx context.Context, q Query) ([]Hit, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if err := s.validateReady(); err != nil {
		return nil, err
	}
	if err := validateQuery(q, s.dimension); err != nil {
		return nil, err
	}

	s.mu.RLock()
	vectors := make([]Vector, 0, len(s.vectors))
	for _, vector := range s.vectors {
		vectors = append(vectors, cloneVector(vector))
	}
	s.mu.RUnlock()

	hits := make([]Hit, 0, len(vectors))
	for _, vector := range vectors {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		if !matchesFilter(vector.Metadata, q.Filter) {
			continue
		}

		score := cosineSimilarity(q.Vector, vector.Embedding)
		if score < q.Threshold {
			continue
		}
		hits = append(hits, Hit{
			ID:       vector.ID,
			Score:    score,
			Metadata: cloneMetadata(vector.Metadata),
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].ID < hits[j].ID
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > q.TopK {
		hits = hits[:q.TopK]
	}
	return hits, nil
}

// Delete 删除指定向量。删除不存在的 ID 是幂等成功，方便上层重试清理。
func (s *MemoryStore) Delete(ctx context.Context, ids []string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := s.validateReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.vectors, id)
	}
	return nil
}

// Health 校验本地 store 是否可用。
func (s *MemoryStore) Health(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return s.validateReady()
}

func (s *MemoryStore) validateReady() error {
	if s == nil {
		return fmt.Errorf("memory vector store is required")
	}
	if s.dimension <= 0 {
		return fmt.Errorf("memory vector store dimension must be positive")
	}
	if s.vectors == nil {
		return fmt.Errorf("memory vector store is not initialized")
	}
	return nil
}

func validateVector(vector Vector, dimension int) error {
	if strings.TrimSpace(vector.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if len(vector.Embedding) != dimension {
		return fmt.Errorf("embedding dimension = %d, want %d", len(vector.Embedding), dimension)
	}
	if zeroNorm(vector.Embedding) {
		return fmt.Errorf("embedding norm must be positive")
	}
	return nil
}

func validateQuery(query Query, dimension int) error {
	if len(query.Vector) != dimension {
		return fmt.Errorf("query vector dimension = %d, want %d", len(query.Vector), dimension)
	}
	if query.TopK <= 0 {
		return fmt.Errorf("query topK must be positive")
	}
	if zeroNorm(query.Vector) {
		return fmt.Errorf("query vector norm must be positive")
	}
	return nil
}

func cosineSimilarity(a, b []float32) float64 {
	var dot float64
	var normA float64
	var normB float64
	for index := range a {
		av := float64(a[index])
		bv := float64(b[index])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func zeroNorm(vector []float32) bool {
	for _, value := range vector {
		if value != 0 {
			return false
		}
	}
	return true
}

func matchesFilter(metadata map[string]any, filter map[string]any) bool {
	for key, want := range filter {
		got, ok := metadata[key]
		if !ok || !reflect.DeepEqual(got, want) {
			return false
		}
	}
	return true
}

func cloneVector(vector Vector) Vector {
	return Vector{
		ID:        vector.ID,
		Embedding: append([]float32(nil), vector.Embedding...),
		Metadata:  cloneMetadata(vector.Metadata),
	}
}

func cloneMetadata(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}

	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneMetadataValue(value)
	}
	return cloned
}

func cloneMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMetadata(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneMetadataValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
