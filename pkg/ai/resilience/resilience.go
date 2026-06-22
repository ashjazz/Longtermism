// Package resilience 实现降级容错与高可用（准备清单 §10）。
//
// 三大组件：
//   - 断路器（Circuit Breaker）：CLOSED → OPEN → HALF_OPEN → CLOSED（§10.2）
//   - 多 provider failover：主挂切备，延迟感知路由（§10.3）
//   - 降级层次：模型降级 → exact/stale cache 兜底 → 规则兜底 → 优雅降级（§10.1）
//
// 与 llm/ 协作：provider 返回 ErrUpstream 时，由本包决定重试/熔断/切换。
package resilience

import (
	"context"
	"time"
)

// State 断路器状态。
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

// Config 断路器参数（§10.2）。
type Config struct {
	FailureThreshold int           `json:"failureThreshold"` // 连续失败次数达此值 → OPEN
	RecoveryTimeout  time.Duration `json:"recoveryTimeout"`  // OPEN 后多久尝试 HALF_OPEN
	HalfOpenMaxProbe int           `json:"halfOpenMaxProbe"` // HALF_OPEN 探测请求数
}

// CircuitBreaker 断路器契约。
type CircuitBreaker interface {
	// Call 在断路器保护下执行 fn。OPEN 时直接拒绝（快速失败），触发上层降级。
	Call(ctx context.Context, fn func(ctx context.Context) error) error
	State() State
}

// FailoverPolicy 决定多个 provider 间的路由与切换（§10.3）。
// 实现：权重轮询 + 延迟/错误率感知 + 健康检查摘除/渐进恢复。
type FailoverPolicy interface {
	// Pick 依据实时健康与延迟选出目标 provider 名。
	Pick(ctx context.Context) (string, error)
	// Record 上报一次调用结果，用于动态权重调整。
	Record(ctx context.Context, provider string, latency time.Duration, err error)
}
