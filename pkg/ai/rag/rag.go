// Package rag 实现检索增强生成全链路（准备清单 §3）。
//
// 子能力（按推进顺序逐步落地）：
//
//	chunker    §3.1  切分策略：recursive / semantic / small-to-big / late-chunking
//	embedder   §3.2  文本转向量
//	retriever  §3.3  纯向量 → 混合(BM25+向量) → RRF 融合 → re-rank → query 改写/HyDE
//
// 故障模式见 §3.4；架构决策清单见 §3.5。检索结果质量由 eval/ 评估（§6）。
package rag

import "context"

// Chunk 是切分后的文档片段。
type Chunk struct {
	ID         string                 `json:"id"`
	Content    string                 `json:"content"`
	ParentID   string                 `json:"parentId,omitempty"` // 小→大检索时回溯父文档（§3.1 策略三）
	Score      float64                `json:"score,omitempty"`    // 检索相关度
	Metadata   map[string]any         `json:"metadata,omitempty"` // 来源、页码、用于 metadata 过滤
}

// Chunker 文档切分器。不同策略实现该接口，可按文档类型路由（§3.1 决策树）。
type Chunker interface {
	Chunk(ctx context.Context, doc Document) ([]Chunk, error)
}

// Document 待切分的源文档。
type Document struct {
	ID      string         `json:"id"`
	Content string         `json:"content"`
	Source  string         `json:"source"` // 文件名/URL
	Type    string         `json:"type"`   // markdown/code/pdf ...
	Meta    map[string]any `json:"meta,omitempty"`
}

// Embedder 文本转向量。批量化以降低成本。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dim 返回向量维度（用于向量库 schema 校验，§5）。
	Dim() int
}

// Retriever 检索器。从混合检索到 re-rank 均封装于此（§3.3）。
type Retriever interface {
	// Retrieve 按 query 返回 top_k 相关 chunk。
	Retrieve(ctx context.Context, query string, topK int, filter map[string]any) ([]Chunk, error)
}

// 决策提示：检索结果是否相关、是否够用，必须由 eval/ 用 Recall@k/MRR/NDCG 量化（§3.5.7），
// 不可凭直觉判断。
