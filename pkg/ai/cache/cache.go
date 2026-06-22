// Package cache 实现 exact/stale fallback cache（P1）与语义缓存（准备清单 §12）。
//
// 语义缓存：基于 embedding 相似度命中，「如何重置密码」与「密码忘了怎么办」命中同一缓存。
// 实测可减少 30-68% LLM 调用、延迟 10-100x 提升（§12.1）。
//
// 优化要点见 §12.3 十法：去噪、领域适配 embedding、metadata 过滤防跨用户污染、
// 自适应 TTL、命中率监控。命中结果必须可被 eval/ 标记，避免「缓存了错误答案」。
// 缓存 key 必须包含 tenant/user scope，缓存内容默认不存原始 query。
package cache

import (
	"context"
	"time"
)

// Scope 描述缓存命中作用域，防止跨租户/跨用户污染。
type Scope struct {
	TenantID  string `json:"tenantId"`
	UserScope string `json:"userScope"`
}

// Entry 缓存条目。
type Entry struct {
	QueryHash string    `json:"queryHash"`
	Response  string    `json:"response"`
	Score     float64   `json:"score,omitempty"`  // 命中相似度，便于审计
	Source    string    `json:"source,omitempty"` // exact | stale | semantic
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// FallbackCache 是 P1 降级用 exact/stale cache，不做向量相似检索。
type FallbackCache interface {
	Get(ctx context.Context, scope Scope, key string) (*Entry, error)
	Set(ctx context.Context, scope Scope, key string, response string, ttl time.Duration) error
}

// SemanticCache 语义缓存契约。实现可用 Redis 向量搜索（§12.2）。
type SemanticCache interface {
	// Get 返回相似度达 threshold 的命中；未命中返回 nil, nil（而非 error）。
	Get(ctx context.Context, scope Scope, query string) (*Entry, error)
	// Set 写入缓存。ttl=0 表示不过期。带 metadata 以便 §12.3 技巧 6 过滤。
	Set(ctx context.Context, scope Scope, query, response string, ttl time.Duration, metadata map[string]any) error
}

// 命中判断必须结合 metadata（用户/产品/语言），防止跨租户缓存污染（§12.3 技巧 6）。
