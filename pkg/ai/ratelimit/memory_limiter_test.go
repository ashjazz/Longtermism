package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemoryLimiterAllowsConfiguredRatePerPeriod(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(MemoryLimiterConfig{
		Now: clock.Now,
	})
	limiter.Configure("global", 2, time.Minute)

	assertAllow(t, limiter, "global", true)
	assertAllow(t, limiter, "global", true)
	assertAllow(t, limiter, "global", false)

	// token bucket 的 refill 必须由时间推进触发，而不是依赖 time.Sleep。
	// 这样测试不会因为机器负载、CI 调度或真实 Redis 不可用而变得不稳定。
	clock.Advance(time.Minute)
	assertAllow(t, limiter, "global", true)
}

func TestMemoryLimiterRefillsContinuously(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 12, 15, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(MemoryLimiterConfig{
		Now: clock.Now,
	})
	limiter.Configure("global", 2, time.Minute)

	assertAllow(t, limiter, "global", true)
	assertAllow(t, limiter, "global", true)
	assertAllow(t, limiter, "global", false)

	clock.Advance(29 * time.Second)
	assertAllow(t, limiter, "global", false)

	// 连续 refill 下，2/minute 等价于约 30 秒补 1 个 token。
	// 如果这里采用整分钟一次性补满，就会在窗口边界附近放过短时间突刺流量。
	clock.Advance(2 * time.Second)
	assertAllow(t, limiter, "global", true)
	assertAllow(t, limiter, "global", false)
}

func TestMemoryLimiterIsolatesGlobalUserAndProviderKeys(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 12, 30, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(MemoryLimiterConfig{
		Now: clock.Now,
	})
	limiter.Configure("global", 1, time.Minute)
	limiter.Configure("user:user-001", 1, time.Minute)
	limiter.Configure("user:user-002", 1, time.Minute)
	limiter.Configure("provider:openai", 1, time.Minute)

	assertAllow(t, limiter, "global", true)
	assertAllow(t, limiter, "global", false)

	// 全局限流耗尽不应污染用户或 provider 维度；生产路径会同时检查多把 key，
	// 但每把 key 的计数必须独立，否则一个热用户可能误伤其它租户或供应商。
	assertAllow(t, limiter, "user:user-001", true)
	assertAllow(t, limiter, "user:user-001", false)
	assertAllow(t, limiter, "user:user-002", true)
	assertAllow(t, limiter, "provider:openai", true)
	assertAllow(t, limiter, "provider:openai", false)
}

func TestMemoryLimiterAllowsUnconfiguredKeysWithoutExternalService(t *testing.T) {
	limiter := NewMemoryLimiter(MemoryLimiterConfig{})

	// 内存 limiter 是 P1 阶段的本地 smoke/test 实现，不应该要求 Redis、网络、
	// API key 或应用配置先就绪。未配置 key 默认放行，真实生产默认限额后续由应用层装配。
	assertAllow(t, limiter, "user:unconfigured", true)
}

func TestMemoryLimiterInvalidConfigFallsBackToAllow(t *testing.T) {
	limiter := NewMemoryLimiter(MemoryLimiterConfig{})

	limiter.Configure("global", 0, time.Minute)
	assertAllow(t, limiter, "global", true)

	limiter.Configure("provider:openai", 1, 0)
	assertAllow(t, limiter, "provider:openai", true)
}

func TestMemoryLimiterNilReceiverAllowsRequests(t *testing.T) {
	var limiter *MemoryLimiter

	allowed, err := limiter.Allow(context.Background(), "global")
	if err != nil {
		t.Fatalf("Allow() error = %v", err)
	}
	if !allowed {
		t.Fatal("Allow() = false, want nil limiter to allow local fallback")
	}

	limiter.Configure("global", 1, time.Minute)
}

func TestMemoryLimiterReturnsContextErrorBeforeConsumingToken(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 13, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(MemoryLimiterConfig{
		Now: clock.Now,
	})
	limiter.Configure("provider:deepseek", 1, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	allowed, err := limiter.Allow(ctx, "provider:deepseek")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Allow() error = %v, want context.Canceled", err)
	}
	if allowed {
		t.Fatal("Allow() allowed canceled request, want false")
	}

	assertAllow(t, limiter, "provider:deepseek", true)
	assertAllow(t, limiter, "provider:deepseek", false)
}

func TestMemoryLimiterConcurrentAllowSharesOneBucket(t *testing.T) {
	clock := newManualClock(time.Date(2026, time.June, 29, 13, 30, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(MemoryLimiterConfig{
		Now: clock.Now,
	})
	limiter.Configure("provider:qwen", 3, time.Minute)

	const workers = 16
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			allowed, err := limiter.Allow(context.Background(), "provider:qwen")
			if err != nil {
				t.Errorf("Allow() error = %v", err)
				return
			}
			results <- allowed
		}()
	}
	wg.Wait()
	close(results)

	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != 3 {
		t.Fatalf("allowed concurrent calls = %d, want 3", allowedCount)
	}
}

func assertAllow(t *testing.T, limiter Limiter, key string, want bool) {
	t.Helper()

	got, err := limiter.Allow(context.Background(), key)
	if err != nil {
		t.Fatalf("Allow(%q) error = %v", key, err)
	}
	if got != want {
		t.Fatalf("Allow(%q) = %v, want %v", key, got, want)
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
