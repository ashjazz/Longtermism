package ratelimit

import (
	"context"
	"sync"
	"time"
)

// MemoryLimiterConfig 是内存限流器的装配配置。
//
// Now 用于测试中注入可控时钟，避免使用 time.Sleep 等待 refill。生产或 smoke
// 环境不传时默认使用 time.Now。
type MemoryLimiterConfig struct {
	Now func() time.Time
}

// MemoryLimiter 是 Limiter 的本地内存实现。
//
// 它用于单元测试、本地 smoke 和教学演示，不替代后续 Redis/分布式限流实现。
// 每个 key 拥有独立 token bucket，因此 global、user:<id>、provider:<name>
// 可以由调用方分别检查，避免一个维度耗尽后污染其它维度的计数。
type MemoryLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]memoryBucket
}

type memoryBucket struct {
	rate       int
	period     time.Duration
	tokens     float64
	lastRefill time.Time
}

// NewMemoryLimiter 创建内存 token bucket 限流器。
func NewMemoryLimiter(config MemoryLimiterConfig) *MemoryLimiter {
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &MemoryLimiter{
		now:     now,
		buckets: make(map[string]memoryBucket),
	}
}

// Allow 判断 key 当前是否还有可用 token。
//
// 未配置 key 默认放行：这个实现主要服务本地测试和 smoke，真实生产默认限额应由
// 应用装配阶段显式 Configure。ctx 已取消时直接返回 ctx.Err()，并且不消耗 token。
func (l *MemoryLimiter) Allow(ctx context.Context, key string) (bool, error) {
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	if l == nil {
		return true, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if !ok {
		return true, nil
	}

	bucket = refillBucket(bucket, l.now())
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	l.buckets[key] = bucket
	return allowed, nil
}

// Configure 设置某个 key 的 token bucket。
//
// rate 或 period 非法时删除该 key 的配置并退回默认放行语义。这样可以避免把错误配置
// 静默固化成“永远拒绝”，本地 smoke 阶段更容易发现装配问题。
func (l *MemoryLimiter) Configure(key string, rate int, period time.Duration) {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if rate <= 0 || period <= 0 {
		delete(l.buckets, key)
		return
	}

	now := l.now()
	l.buckets[key] = memoryBucket{
		rate:       rate,
		period:     period,
		tokens:     float64(rate),
		lastRefill: now,
	}
}

func refillBucket(bucket memoryBucket, now time.Time) memoryBucket {
	if bucket.rate <= 0 || bucket.period <= 0 {
		return bucket
	}
	if bucket.lastRefill.IsZero() {
		bucket.lastRefill = now
		return bucket
	}
	if now.Before(bucket.lastRefill) {
		return bucket
	}

	elapsed := now.Sub(bucket.lastRefill)
	if elapsed <= 0 {
		return bucket
	}

	// 连续 refill：把 lastRefill 到 now 的全部时间按比例兑换为 token。
	// 与“每个完整 period 一次性补满”相比，这能减少窗口边界附近的瞬时突刺；
	// 与“离散 refill 后 lastRefill=now”相比，也不会丢掉不足一个周期的零头时间。
	refill := elapsed.Seconds() * float64(bucket.rate) / bucket.period.Seconds()
	bucket.tokens += refill
	if bucket.tokens > float64(bucket.rate) {
		bucket.tokens = float64(bucket.rate)
	}
	bucket.lastRefill = now
	return bucket
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
