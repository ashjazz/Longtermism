package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemoryFallbackCacheReturnsExactAndStaleEntries(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 14, 0, 0, 0, time.UTC))
	cache := NewMemoryFallbackCache(MemoryFallbackConfig{
		Now:      clock.Now,
		StaleTTL: 10 * time.Minute,
	})
	scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}

	if err := cache.Set(context.Background(), scope, "prompt-hash:model", "fresh answer", time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	exact := assertCacheHit(t, cache, scope, "prompt-hash:model", "fresh answer", "exact")
	if exact.CreatedAt != clock.Now() {
		t.Fatalf("CreatedAt = %v, want %v", exact.CreatedAt, clock.Now())
	}
	if exact.ExpiresAt != clock.Now().Add(time.Minute) {
		t.Fatalf("ExpiresAt = %v, want %v", exact.ExpiresAt, clock.Now().Add(time.Minute))
	}

	// stale cache 是上游全挂时的降级兜底：过期后不能伪装成新鲜结果，
	// 但在有限窗口内可以明确标记为 stale 交给调用方做降级提示。
	clock.Advance(2 * time.Minute)
	assertCacheHit(t, cache, scope, "prompt-hash:model", "fresh answer", "stale")

	clock.Advance(10 * time.Minute)
	assertCacheMiss(t, cache, scope, "prompt-hash:model")
}

func TestMemoryFallbackCacheKeepsTenantAndUserScopesIsolated(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 14, 30, 0, 0, time.UTC))
	cache := NewMemoryFallbackCache(MemoryFallbackConfig{
		Now:      clock.Now,
		StaleTTL: time.Minute,
	})
	key := "same-query-hash:model"

	scopeA := Scope{TenantID: "tenant-a", UserScope: "user-001"}
	scopeB := Scope{TenantID: "tenant-a", UserScope: "user-002"}
	scopeC := Scope{TenantID: "tenant-b", UserScope: "user-001"}
	if err := cache.Set(context.Background(), scopeA, key, "tenant-a user-001 answer", time.Minute); err != nil {
		t.Fatalf("Set(scopeA) error = %v", err)
	}
	if err := cache.Set(context.Background(), scopeB, key, "tenant-a user-002 answer", time.Minute); err != nil {
		t.Fatalf("Set(scopeB) error = %v", err)
	}

	assertCacheHit(t, cache, scopeA, key, "tenant-a user-001 answer", "exact")
	assertCacheHit(t, cache, scopeB, key, "tenant-a user-002 answer", "exact")
	assertCacheMiss(t, cache, scopeC, key)
}

func TestMemoryFallbackCacheTTLZeroNeverExpires(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 15, 0, 0, 0, time.UTC))
	cache := NewMemoryFallbackCache(MemoryFallbackConfig{
		Now:      clock.Now,
		StaleTTL: time.Minute,
	})
	scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}

	if err := cache.Set(context.Background(), scope, "stable-key", "stable answer", 0); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	clock.Advance(24 * time.Hour)
	entry := assertCacheHit(t, cache, scope, "stable-key", "stable answer", "exact")
	if !entry.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want zero for ttl=0", entry.ExpiresAt)
	}
}

func TestMemoryFallbackCacheReturnsContextErrorBeforeAccess(t *testing.T) {
	cache := NewMemoryFallbackCache(MemoryFallbackConfig{})
	scope := Scope{TenantID: "tenant-a", UserScope: "user-001"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := cache.Set(ctx, scope, "key", "answer", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Set() error = %v, want context.Canceled", err)
	}

	entry, err := cache.Get(ctx, scope, "key")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if entry != nil {
		t.Fatalf("Get() entry = %#v, want nil on canceled context", entry)
	}
}

func assertCacheHit(t *testing.T, cache FallbackCache, scope Scope, key, wantResponse, wantSource string) *Entry {
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

func assertCacheMiss(t *testing.T, cache FallbackCache, scope Scope, key string) {
	t.Helper()

	entry, err := cache.Get(context.Background(), scope, key)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", key, err)
	}
	if entry != nil {
		t.Fatalf("Get(%q) = %#v, want nil miss", key, entry)
	}
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(duration)
}
