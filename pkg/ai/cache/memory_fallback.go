package cache

import (
	"context"
	"sync"
	"time"
)

const (
	sourceExact = "exact"
	sourceStale = "stale"
)

// MemoryFallbackConfig 是内存 fallback cache 的装配配置。
//
// StaleTTL 控制过期后还能作为 stale 降级结果使用多久。Now 可注入测试时钟，
// 避免用 time.Sleep 验证 TTL 行为。
type MemoryFallbackConfig struct {
	Now      func() time.Time
	StaleTTL time.Duration
}

// MemoryFallbackCache 是 FallbackCache 的本地内存实现。
//
// 它服务测试、本地 smoke 和教学 demo，不替代后续 Redis/分布式 cache。
// entries 使用 scope+key 组成内部键，防止同一个 query hash 跨租户或跨用户误命中。
type MemoryFallbackCache struct {
	mu       sync.RWMutex
	now      func() time.Time
	staleTTL time.Duration
	entries  map[memoryCacheKey]Entry
}

type memoryCacheKey struct {
	tenantID  string
	userScope string
	key       string
}

// NewMemoryFallbackCache 创建内存 fallback cache。
func NewMemoryFallbackCache(config MemoryFallbackConfig) *MemoryFallbackCache {
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &MemoryFallbackCache{
		now:      now,
		staleTTL: config.StaleTTL,
		entries:  make(map[memoryCacheKey]Entry),
	}
}

// Get 返回 exact/stale 命中；未命中返回 nil, nil。
//
// 返回的 Entry 是副本，Source 会按当前时间重新标记为 exact 或 stale。这样调用方既能
// 清楚知道这是新鲜结果还是降级旧结果，也不会修改 cache 内部保存的原始条目。
func (c *MemoryFallbackCache) Get(ctx context.Context, scope Scope, key string) (*Entry, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}

	c.mu.RLock()
	entry, ok := c.entries[newMemoryCacheKey(scope, key)]
	c.mu.RUnlock()
	if !ok {
		return nil, nil
	}

	now := c.now()
	source, ok := c.sourceFor(entry, now)
	if !ok {
		return nil, nil
	}
	entry.Source = source
	return cloneEntryPointer(entry), nil
}

// Set 写入 fallback cache。
//
// ttl=0 表示不过期；ttl>0 时 ExpiresAt 表示 exact 命中截止时间，之后是否还能命中
// 由 StaleTTL 决定。这里不存原始 query，只保存调用方传入的 hash/key。
func (c *MemoryFallbackCache) Set(ctx context.Context, scope Scope, key string, response string, ttl time.Duration) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if c == nil {
		return nil
	}

	now := c.now()
	entry := Entry{
		QueryHash: key,
		Response:  response,
		Source:    sourceExact,
		CreatedAt: now,
	}
	if ttl > 0 {
		entry.ExpiresAt = now.Add(ttl)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[newMemoryCacheKey(scope, key)] = entry
	return nil
}

func (c *MemoryFallbackCache) sourceFor(entry Entry, now time.Time) (string, bool) {
	if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) || now.Equal(entry.ExpiresAt) {
		return sourceExact, true
	}
	if c.staleTTL <= 0 {
		return "", false
	}
	staleUntil := entry.ExpiresAt.Add(c.staleTTL)
	if now.Before(staleUntil) || now.Equal(staleUntil) {
		return sourceStale, true
	}
	return "", false
}

func newMemoryCacheKey(scope Scope, key string) memoryCacheKey {
	return memoryCacheKey{
		tenantID:  scope.TenantID,
		userScope: scope.UserScope,
		key:       key,
	}
}

func cloneEntryPointer(entry Entry) *Entry {
	cloned := entry
	return &cloned
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
