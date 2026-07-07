package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
	"github.com/jazzash/ashjazz-aiagent/pkg/ai/vectordb"
)

const (
	hitMetadataContentKey  = "content"
	hitMetadataParentIDKey = "parent_id"
	queryHashBytes         = 8
)

// BasicRetrieverConfig 是基础 retriever 的装配配置。
//
// P2 初始版本只做“query embedding -> vector store search -> hit 映射”的最短路径。
// 后续混合检索、RRF、rerank、query rewrite/HyDE 都应作为更高层组合能力加入，而不是
// 让这个基础 retriever 过早膨胀。
type BasicRetrieverConfig struct {
	Embedder  Embedder
	Store     vectordb.Store
	Threshold float64
	Tracer    obs.Tracer
	Feature   string
	Now       func() time.Time
}

// BasicRetriever 是最小 RAG 检索器。
//
// 它只依赖 rag.Embedder 和 vectordb.Store 两个抽象，因此默认可以配合 MemoryStore
// 离线测试，也可以在后续替换为 pgvector/Milvus adapter 而不改上层 RAG 流程。
type BasicRetriever struct {
	embedder  Embedder
	store     vectordb.Store
	threshold float64
	tracer    obs.Tracer
	feature   string
	now       func() time.Time
}

func NewBasicRetriever(config BasicRetrieverConfig) *BasicRetriever {
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &BasicRetriever{
		embedder:  config.Embedder,
		store:     config.Store,
		threshold: config.Threshold,
		tracer:    config.Tracer,
		feature:   config.Feature,
		now:       now,
	}
}

// Retrieve 按 query 返回 topK 个相关 chunk。
//
// 注意错误信息只携带 query_hash，不携带原始 query。真实用户问题可能包含手机号、账号、
// 身份证或业务机密；普通错误和日志必须可诊断但不泄露原文。
func (r *BasicRetriever) Retrieve(ctx context.Context, query string, topK int, filter map[string]any) ([]Chunk, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if err := r.validate(query, topK); err != nil {
		return nil, err
	}

	startedAt := r.now()
	queryHash := hashQuery(query)
	embeddings, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed retrieval query query_hash=%s: %w", queryHash, err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("embed retrieval query query_hash=%s: expected one non-empty vector", queryHash)
	}

	hits, err := r.store.Search(ctx, vectordb.Query{
		Vector:    append([]float32(nil), embeddings[0]...),
		TopK:      topK,
		Filter:    cloneMetadata(filter),
		Threshold: r.threshold,
	})
	if err != nil {
		return nil, fmt.Errorf("search vector store query_hash=%s: %w", queryHash, err)
	}

	chunks := chunksFromHits(hits)
	r.recordObservation(ctx, queryHash, len(query), chunks, startedAt, r.now())
	return chunks, nil
}

func (r *BasicRetriever) validate(query string, topK int) error {
	if r == nil {
		return fmt.Errorf("basic retriever is required")
	}
	if r.embedder == nil {
		return fmt.Errorf("basic retriever embedder is required")
	}
	if r.store == nil {
		return fmt.Errorf("basic retriever store is required")
	}
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("retrieval query is required")
	}
	if topK <= 0 {
		return fmt.Errorf("retrieval topK must be positive")
	}
	return nil
}

func chunksFromHits(hits []vectordb.Hit) []Chunk {
	if len(hits) == 0 {
		return nil
	}

	chunks := make([]Chunk, len(hits))
	for index, hit := range hits {
		metadata := cloneMetadata(hit.Metadata)
		chunks[index] = Chunk{
			ID:       hit.ID,
			Content:  stringMetadata(metadata, hitMetadataContentKey),
			ParentID: stringMetadata(metadata, hitMetadataParentIDKey),
			Score:    hit.Score,
			Metadata: chunkHitMetadata(metadata),
		}
	}
	return chunks
}

func chunkHitMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}

	delete(metadata, hitMetadataContentKey)
	delete(metadata, hitMetadataParentIDKey)
	return metadata
}

func stringMetadata(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func hashQuery(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:queryHashBytes])
}

func (r *BasicRetriever) recordObservation(ctx context.Context, queryHash string, queryLen int, chunks []Chunk, startedAt, endedAt time.Time) {
	if r == nil || r.tracer == nil {
		return
	}
	if strings.TrimSpace(r.feature) == "" {
		return
	}

	identity, ok := obs.CorrelationIdentityFromContext(ctx)
	if !ok || strings.TrimSpace(identity.AITraceID) == "" {
		return
	}

	topScores := topScoresFromChunks(chunks)
	outcomeStatus := "success"
	failureStatus := ""
	retrievalStatus := "success"
	if len(chunks) == 0 {
		outcomeStatus = "failure"
		failureStatus = string(obs.FailureRetrievalMiss)
		retrievalStatus = "miss"
	}

	trace := obs.NewTrace(
		identity.AITraceID,
		r.feature,
		endedAt,
		obs.WithCorrelationIdentity(identity),
		obs.WithObservationType(obs.ObservationTypeRetriever),
		obs.WithQuery(queryHash, "", queryLen),
		obs.WithRetrieval(len(chunks), "", topScores, endedAt.Sub(startedAt).Milliseconds()),
		obs.WithSafeSummaries(
			obs.NewSafeSummary(obs.WithSummaryHash(queryHash), obs.WithSummaryLength(queryLen)),
			obs.SafeSummary{},
			retrievalSummaryFromChunks(chunks, retrievalStatus, failureStatus),
			obs.SafeSummary{},
		),
		obs.WithOutcome(outcomeStatus),
	)
	trace.FailureStatus = failureStatus

	_ = obs.RecordWithExportFailureProtection(ctx, r.tracer, trace)
}

func topScoresFromChunks(chunks []Chunk) []float64 {
	if len(chunks) == 0 {
		return nil
	}

	scores := make([]float64, len(chunks))
	for index, chunk := range chunks {
		scores[index] = chunk.Score
	}
	return scores
}

func retrievalSummaryFromChunks(chunks []Chunk, status, errorClass string) obs.SafeSummary {
	options := []obs.SafeSummaryOption{
		obs.WithSummaryCount(len(chunks)),
		obs.WithSummaryStatus(status),
		obs.WithSummaryErrorClass(errorClass),
	}
	if len(chunks) > 0 {
		options = append(options, obs.WithSummaryScore(chunks[0].Score))
	}
	return obs.NewSafeSummary(options...)
}
