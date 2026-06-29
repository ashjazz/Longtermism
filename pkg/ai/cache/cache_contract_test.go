package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

// FallbackCacheContractFactory 为契约测试创建一个全新的 fallback cache。
//
// 契约测试不是 MemoryFallbackCache 的私有单测，而是后续 Redis/分布式 cache adapter
// 的共同验收入口。每个子测试都必须拿到独立 cache，避免命中数据、TTL 状态或时钟推进
// 在测试之间互相污染。
type FallbackCacheContractFactory func(t *testing.T) (FallbackCache, FallbackCacheClock)

// FallbackCacheClock 为 TTL/stale 契约提供可控时间。
//
// 真实生产 cache 往往依赖 Redis/server time，但契约测试需要确定性地跨过 exact/stale
// 窗口；因此 adapter 测试应提供可控时钟、测试命名空间，或等价的时间推进机制。
type FallbackCacheClock interface {
	Now() time.Time
	Advance(duration time.Duration)
}

func TestMemoryFallbackCacheContract(t *testing.T) {
	RunFallbackCacheContract(t, func(t *testing.T) (FallbackCache, FallbackCacheClock) {
		t.Helper()

		clock := newFallbackContractClock(time.Date(2026, time.June, 29, 18, 0, 0, 0, time.UTC))
		return NewMemoryFallbackCache(MemoryFallbackConfig{
			Now:      clock.Now,
			StaleTTL: 10 * time.Minute,
		}), clock
	})
}

// RunFallbackCacheContract 验证所有 FallbackCache 实现必须保持的用户可见语义。
//
// fallback cache 是上游模型不可用时的降级安全网：它必须严格按 tenant/user scope
// 隔离，必须区分 exact 与 stale，必须在 miss 时返回 nil,nil，并且必须尊重 context
// cancellation，避免用户取消后还继续访问远端缓存。
func RunFallbackCacheContract(t *testing.T, newCache FallbackCacheContractFactory) {
	t.Helper()

	t.Run("returns exact hit before ttl expires", func(t *testing.T) {
		cache, clock := newCache(t)
		scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}

		if err := cache.Set(context.Background(), scope, "query-hash:model-a", "fresh answer", time.Minute); err != nil {
			t.Fatalf("Set() error = %v", err)
		}

		entry := assertFallbackContractHit(t, cache, scope, "query-hash:model-a", "fresh answer", sourceExact)
		if entry.CreatedAt != clock.Now() {
			t.Fatalf("CreatedAt = %v, want %v", entry.CreatedAt, clock.Now())
		}
		if entry.ExpiresAt != clock.Now().Add(time.Minute) {
			t.Fatalf("ExpiresAt = %v, want %v", entry.ExpiresAt, clock.Now().Add(time.Minute))
		}
	})

	t.Run("returns stale hit only inside stale window", func(t *testing.T) {
		cache, clock := newCache(t)
		scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}

		if err := cache.Set(context.Background(), scope, "query-hash:model-a", "cached answer", time.Minute); err != nil {
			t.Fatalf("Set() error = %v", err)
		}

		clock.Advance(2 * time.Minute)
		assertFallbackContractHit(t, cache, scope, "query-hash:model-a", "cached answer", sourceStale)

		clock.Advance(10 * time.Minute)
		assertFallbackContractMiss(t, cache, scope, "query-hash:model-a")
	})

	t.Run("ttl zero keeps exact entry without expiry", func(t *testing.T) {
		cache, clock := newCache(t)
		scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}

		if err := cache.Set(context.Background(), scope, "stable-query:model-a", "stable answer", 0); err != nil {
			t.Fatalf("Set() error = %v", err)
		}

		clock.Advance(24 * time.Hour)
		entry := assertFallbackContractHit(t, cache, scope, "stable-query:model-a", "stable answer", sourceExact)
		if !entry.ExpiresAt.IsZero() {
			t.Fatalf("ExpiresAt = %v, want zero for ttl=0", entry.ExpiresAt)
		}
	})

	t.Run("isolates tenant and user scope", func(t *testing.T) {
		cache, _ := newCache(t)
		key := "same-query-hash:model-a"
		scopeA := Scope{TenantID: "tenant-a", UserScope: "user-001"}
		scopeB := Scope{TenantID: "tenant-a", UserScope: "user-002"}
		scopeC := Scope{TenantID: "tenant-b", UserScope: "user-001"}

		if err := cache.Set(context.Background(), scopeA, key, "tenant-a user-001 answer", time.Minute); err != nil {
			t.Fatalf("Set(scopeA) error = %v", err)
		}
		if err := cache.Set(context.Background(), scopeB, key, "tenant-a user-002 answer", time.Minute); err != nil {
			t.Fatalf("Set(scopeB) error = %v", err)
		}

		assertFallbackContractHit(t, cache, scopeA, key, "tenant-a user-001 answer", sourceExact)
		assertFallbackContractHit(t, cache, scopeB, key, "tenant-a user-002 answer", sourceExact)
		assertFallbackContractMiss(t, cache, scopeC, key)
	})

	t.Run("missing key returns nil entry and nil error", func(t *testing.T) {
		cache, _ := newCache(t)
		scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}

		entry, err := cache.Get(context.Background(), scope, "missing-query:model-a")
		if err != nil {
			t.Fatalf("Get() error = %v, want nil miss error", err)
		}
		if entry != nil {
			t.Fatalf("Get() entry = %#v, want nil miss", entry)
		}
	})

	t.Run("returned entry does not expose mutable cache state", func(t *testing.T) {
		cache, _ := newCache(t)
		scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}

		if err := cache.Set(context.Background(), scope, "copy-query:model-a", "original answer", time.Minute); err != nil {
			t.Fatalf("Set() error = %v", err)
		}

		first := assertFallbackContractHit(t, cache, scope, "copy-query:model-a", "original answer", sourceExact)
		first.Response = "mutated answer"
		first.QueryHash = "mutated-key"
		first.Source = "mutated-source"

		assertFallbackContractHit(t, cache, scope, "copy-query:model-a", "original answer", sourceExact)
	})

	t.Run("context cancellation is returned before cache access", func(t *testing.T) {
		cache, _ := newCache(t)
		scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := cache.Set(ctx, scope, "query-hash:model-a", "answer", time.Minute); !errors.Is(err, context.Canceled) {
			t.Fatalf("Set() error = %v, want context.Canceled", err)
		}

		entry, err := cache.Get(ctx, scope, "query-hash:model-a")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Get() error = %v, want context.Canceled", err)
		}
		if entry != nil {
			t.Fatalf("Get() entry = %#v, want nil on canceled context", entry)
		}
	})
}

func assertFallbackContractHit(t *testing.T, cache FallbackCache, scope Scope, key, wantResponse, wantSource string) *Entry {
	t.Helper()

	entry, err := cache.Get(context.Background(), scope, key)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", key, err)
	}
	if entry == nil {
		t.Fatalf("Get(%q) = nil, want cache hit", key)
	}
	if entry.QueryHash != key {
		t.Fatalf("QueryHash = %q, want %q", entry.QueryHash, key)
	}
	if entry.Response != wantResponse {
		t.Fatalf("Response = %q, want %q", entry.Response, wantResponse)
	}
	if entry.Source != wantSource {
		t.Fatalf("Source = %q, want %q", entry.Source, wantSource)
	}
	return entry
}

func assertFallbackContractMiss(t *testing.T, cache FallbackCache, scope Scope, key string) {
	t.Helper()

	entry, err := cache.Get(context.Background(), scope, key)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", key, err)
	}
	if entry != nil {
		t.Fatalf("Get(%q) = %#v, want nil miss", key, entry)
	}
}

type fallbackContractClock struct {
	now time.Time
}

func newFallbackContractClock(now time.Time) *fallbackContractClock {
	return &fallbackContractClock{now: now}
}

func (c *fallbackContractClock) Now() time.Time {
	return c.now
}

func (c *fallbackContractClock) Advance(duration time.Duration) {
	c.now = c.now.Add(duration)
}
