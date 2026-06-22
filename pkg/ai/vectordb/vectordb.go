// Package vectordb 是向量数据库抽象层（准备清单 §5）。
//
// 目的：让 rag/ 与具体向量库（pgvector / Qdrant / Milvus / Chromo …）解耦，
// 可按数据规模与延迟要求切换实现（§5.2 决策矩阵）。索引选型（HNSW/IVF/PQ）
// 是面试区分点，应在各实现的文档中记录取舍。
package vectordb

import "context"

// Vector 一条向量记录。
type Vector struct {
	ID       string         `json:"id"`
	Embedding []float32     `json:"embedding"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Query 向量检索请求。
type Query struct {
	Vector    []float32       // 查询向量
	TopK      int             // 返回数
	Filter    map[string]any  // metadata 过滤（§5 选型要点）
	Threshold float64         // 相似度下限，低于则不返回
}

// Hit 单条命中结果。
type Hit struct {
	ID       string         `json:"id"`
	Score    float64        `json:"score"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Store 向量存储契约。实现需注意：
//   - 高频查询字段与外键性质字段必须建索引（rules/database/design.md）；
//   - 检索为空/失败时不应让整条 RAG 链路 500，而应降级（§10、§3.4）。
type Store interface {
	Upsert(ctx context.Context, vecs []Vector) error
	Search(ctx context.Context, q Query) ([]Hit, error)
	Delete(ctx context.Context, ids []string) error
	// Health 用于多向量库 failover 的健康检查（§10.3）。
	Health(ctx context.Context) error
}
