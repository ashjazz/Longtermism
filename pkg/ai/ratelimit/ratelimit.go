// Package ratelimit 实现限流与成本控制（准备清单 §11）。
//
// 两件事：
//  1. 多维度限流（§10.4 token bucket）：全局 QPS / 用户级 / provider 级，避免打爆上游与自身。
//  2. 成本控制（§11.1）：模型路由（简单→小模型）、token 预算、prompt caching、batch。
//
// 限流计数与模型路由状态应持久化到 Redis（见应用配置 redis.default）。
package ratelimit

import (
	"context"
	"time"
)

// Limiter 多维度限流器（§10.4）。key 维度由调用方决定（global / user:<id> / provider:<p>）。
type Limiter interface {
	// Allow 返回是否放行；false 时调用方应返回 429 而非排队阻塞。
	Allow(ctx context.Context, key string) (bool, error)
	// Configure 设置某 key 的速率：每 period 允许 rate 次。
	Configure(key string, rate int, period time.Duration)
}

// Complexity 查询复杂度分级，用于模型路由决策（§11.2）。
type Complexity string

const (
	ComplexitySimple   Complexity = "simple"   // 问候、翻译、单点 QA → 小模型
	ComplexityModerate Complexity = "moderate" // 默认中等模型
	ComplexityReasoned Complexity = "reasoned" // 分析、比较、推理 → 大模型
)

// ModelRouter 按查询复杂度路由到合适模型，平衡延迟/成本/质量（§11.1 策略1）。
type ModelRouter interface {
	Route(ctx context.Context, query string, ctx2 map[string]any) (model string, err error)
}
