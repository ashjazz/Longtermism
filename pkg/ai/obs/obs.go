// Package obs 实现 AI 系统可观测性（准备清单 §8）。
//
// 区别于传统后端，AI 系统需额外追踪：TTFT、token 用量、单请求成本、
// prompt 版本 hash、幻觉率、用户反馈。每次 LLM/检索调用都应产出结构化 trace。
// 普通 trace/log 不记录用户原始 query、完整 prompt 或 tool 参数原文；敏感原文只能进入加密审计存储。
//
// trace 字段集见 §8.3。实现可选 OTEL / LangFuse / 自建，对业务代码只暴露本包接口。
package obs

import (
	"context"
	"time"
)

// Trace 是一次端到端 AI 请求的完整记录（§8.3）。字段不可缺省，便于线上归因。
type Trace struct {
	TraceID   string    `json:"traceId"`
	TenantID  string    `json:"tenantId,omitempty"`
	UserID    string    `json:"userId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Feature   string    `json:"feature"` // 功能模块标识，如 rag_qa
	Timestamp time.Time `json:"timestamp"`
	QueryHash string    `json:"queryHash,omitempty"`
	QueryLang string    `json:"queryLang,omitempty"`
	QueryLen  int       `json:"queryLen,omitempty"`

	// 生成阶段
	Model             string  `json:"model,omitempty"`
	PromptTemplateVer string  `json:"promptTemplateVer,omitempty"`
	PromptHash        string  `json:"promptHash,omitempty"`
	InputTokens       int     `json:"inputTokens,omitempty"`
	OutputTokens      int     `json:"outputTokens,omitempty"`
	ReasoningTokens   int     `json:"reasoningTokens,omitempty"`
	CacheReadTokens   int     `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens  int     `json:"cacheWriteTokens,omitempty"`
	Temperature       float64 `json:"temperature,omitempty"`
	TTFTMs            int64   `json:"ttftMs,omitempty"` // 首 token 延迟（§13.2）
	TotalLatencyMs    int64   `json:"totalLatencyMs,omitempty"`

	// 检索阶段
	ChunksRetrieved    int       `json:"chunksRetrieved,omitempty"`
	QueryRewrittenHash string    `json:"queryRewrittenHash,omitempty"`
	TopScores          []float64 `json:"topScores,omitempty"`
	RetrievalLatencyMs int64     `json:"retrievalLatencyMs,omitempty"`

	// 成本
	CostUSD       float64 `json:"costUsd,omitempty"`
	OutcomeStatus string  `json:"outcomeStatus,omitempty"`

	// 反馈（后续回填）
	UserRating    *int     `json:"userRating,omitempty"`    // -1/0/+1
	AutoEvalScore *float64 `json:"autoEvalScore,omitempty"` // eval/ 离线评分
}

// Tracer trace 收集器。实现可异步落库，避免阻塞热路径。
type Tracer interface {
	Record(ctx context.Context, t Trace)
}

// SLOAI AI 系统性能 SLO（§13.3）。告警阈值见 §8.1。
type SLOAI struct {
	TTFTP95Ms         int64
	TotalLatencyP95Ms int64
	ErrorRatePct      float64
}
