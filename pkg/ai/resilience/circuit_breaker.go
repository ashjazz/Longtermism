package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultFailureThreshold = 1
	defaultRecoveryTimeout  = 30 * time.Second
	defaultHalfOpenMaxProbe = 1
)

// ErrCircuitOpen 表示断路器处于 OPEN，调用被快速拒绝。
//
// provider wrapper 后续会用 errors.Is(err, ErrCircuitOpen) 判断是否应该进入降级路径。
// 这里不把它包装成上游错误，是因为“断路器拒绝”发生在本地保护层，而不是远端 provider 返回。
var ErrCircuitOpen = errors.New("resilience: circuit breaker open")

// circuitBreaker 是 CircuitBreaker 的默认实现。
//
// 状态机遵循 CLOSED -> OPEN -> HALF_OPEN -> CLOSED：
//   - CLOSED：正常放行，连续失败达到阈值后 OPEN。
//   - OPEN：在恢复窗口内快速失败，不调用下游。
//   - HALF_OPEN：恢复窗口后放少量探测请求；探测成功足够次数则 CLOSED，失败则重新 OPEN。
//
// 注意：锁只保护状态读写，不包住被保护函数 fn。真实 LLM 调用可能很慢，如果持锁执行，
// 会让并发请求无法快速观察 OPEN 状态，也会放大尾延迟。
type circuitBreaker struct {
	mu sync.Mutex

	config Config
	state  State

	consecutiveFailures int
	openedAt            time.Time
	halfOpenSuccesses   int
	halfOpenInFlight    int
}

// NewCircuitBreaker 创建断路器。
func NewCircuitBreaker(config Config) CircuitBreaker {
	return &circuitBreaker{
		config: normalizeConfig(config),
		state:  StateClosed,
	}
}

func (b *circuitBreaker) Call(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("circuit breaker protected function is required")
	}

	probe, err := b.beforeCall()
	if err != nil {
		return err
	}

	cause := fn(ctx)
	b.afterCall(probe, cause)
	return cause
}

func (b *circuitBreaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *circuitBreaker) beforeCall() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return false, nil
	case StateOpen:
		if b.config.Now().Sub(b.openedAt) < b.config.RecoveryTimeout {
			return false, fmt.Errorf("circuit breaker state %q: %w", b.state, ErrCircuitOpen)
		}
		b.toHalfOpen()
		return b.reserveHalfOpenProbe()
	case StateHalfOpen:
		return b.reserveHalfOpenProbe()
	default:
		return false, fmt.Errorf("unknown circuit breaker state %q", b.state)
	}
}

func (b *circuitBreaker) afterCall(probe bool, cause error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if probe {
		b.finishHalfOpenProbe(cause)
		return
	}

	if cause == nil {
		b.consecutiveFailures = 0
		return
	}

	b.consecutiveFailures++
	if b.consecutiveFailures >= b.config.FailureThreshold {
		b.toOpen()
	}
}

func (b *circuitBreaker) reserveHalfOpenProbe() (bool, error) {
	if b.halfOpenInFlight >= b.config.HalfOpenMaxProbe {
		return false, fmt.Errorf("circuit breaker half-open probes exhausted: %w", ErrCircuitOpen)
	}
	b.halfOpenInFlight++
	return true, nil
}

func (b *circuitBreaker) finishHalfOpenProbe(cause error) {
	if b.halfOpenInFlight > 0 {
		b.halfOpenInFlight--
	}
	if cause != nil {
		b.toOpen()
		return
	}

	b.halfOpenSuccesses++
	if b.halfOpenSuccesses >= b.config.HalfOpenMaxProbe {
		b.toClosed()
	}
}

func (b *circuitBreaker) toClosed() {
	b.state = StateClosed
	b.consecutiveFailures = 0
	b.openedAt = time.Time{}
	b.halfOpenSuccesses = 0
	b.halfOpenInFlight = 0
}

func (b *circuitBreaker) toOpen() {
	b.state = StateOpen
	b.openedAt = b.config.Now()
	b.consecutiveFailures = 0
	b.halfOpenSuccesses = 0
	b.halfOpenInFlight = 0
}

func (b *circuitBreaker) toHalfOpen() {
	b.state = StateHalfOpen
	b.consecutiveFailures = 0
	b.halfOpenSuccesses = 0
	b.halfOpenInFlight = 0
}

func normalizeConfig(config Config) Config {
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = defaultFailureThreshold
	}
	if config.RecoveryTimeout <= 0 {
		config.RecoveryTimeout = defaultRecoveryTimeout
	}
	if config.HalfOpenMaxProbe <= 0 {
		config.HalfOpenMaxProbe = defaultHalfOpenMaxProbe
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return config
}
