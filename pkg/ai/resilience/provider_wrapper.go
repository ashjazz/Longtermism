package resilience

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

// ProviderWrapper 用断路器保护 llm.Provider。
//
// 这里刻意保持 llm.Provider 接口不变：调用方仍然只看到 Name/Capabilities/Chat/ChatStream。
// wrapper 只在内部判断错误是否属于 llm.ErrUpstream，并据此决定是否计入断路器失败。
type ProviderWrapper struct {
	provider llm.Provider
	breaker  CircuitBreaker
	tracer   obs.Tracer
	feature  string
	now      func() time.Time
}

// ProviderWrapperOption 描述 provider wrapper 的可选装配项。
type ProviderWrapperOption func(*ProviderWrapper)

// NewProviderWrapper 创建带 resilience 保护的 provider。
func NewProviderWrapper(provider llm.Provider, breaker CircuitBreaker, options ...ProviderWrapperOption) *ProviderWrapper {
	wrapper := &ProviderWrapper{
		provider: provider,
		breaker:  breaker,
		now:      time.Now,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(wrapper)
	}
	if wrapper.now == nil {
		wrapper.now = time.Now
	}
	return wrapper
}

// WithTracer 配置 provider outcome 的语义观测出口。
func WithTracer(tracer obs.Tracer) ProviderWrapperOption {
	return func(wrapper *ProviderWrapper) {
		wrapper.tracer = tracer
	}
}

// WithFeature 配置 provider trace 的功能维度。
func WithFeature(feature string) ProviderWrapperOption {
	return func(wrapper *ProviderWrapper) {
		wrapper.feature = feature
	}
}

// WithNow 注入时钟，便于测试稳定断言 provider latency。
func WithNow(now func() time.Time) ProviderWrapperOption {
	return func(wrapper *ProviderWrapper) {
		wrapper.now = now
	}
}

func (p *ProviderWrapper) Name() string {
	if p == nil || p.provider == nil {
		return ""
	}
	return p.provider.Name()
}

func (p *ProviderWrapper) Capabilities(model string) llm.ProviderCapabilities {
	if p == nil || p.provider == nil {
		return llm.ProviderCapabilities{}
	}
	return p.provider.Capabilities(model)
}

func (p *ProviderWrapper) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}

	startedAt := p.now()
	var response *llm.ChatResponse
	callResult := p.call(ctx, func(ctx context.Context) error {
		got, err := p.provider.Chat(ctx, req)
		response = got
		return err
	})
	err := callResult.err()
	p.recordOutcome(ctx, providerOutcomeObservation{
		RequestedModel: requestedModel(req),
		ActualModel:    actualModel(response),
		Err:            err,
		StartedAt:      startedAt,
		EndedAt:        p.now(),
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (p *ProviderWrapper) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}

	startedAt := p.now()
	var chunks <-chan llm.ChatChunk
	callResult := p.call(ctx, func(ctx context.Context) error {
		got, err := p.provider.ChatStream(ctx, req)
		chunks = got
		return err
	})
	err := callResult.err()
	p.recordOutcome(ctx, providerOutcomeObservation{
		RequestedModel: requestedModel(req),
		Err:            err,
		StartedAt:      startedAt,
		EndedAt:        p.now(),
	})
	if err != nil {
		return nil, err
	}
	return chunks, nil
}

func (p *ProviderWrapper) validate() error {
	if p == nil {
		return fmt.Errorf("provider wrapper is required")
	}
	if p.provider == nil {
		return fmt.Errorf("provider wrapper provider is required")
	}
	if p.breaker == nil {
		return fmt.Errorf("provider wrapper circuit breaker is required")
	}
	return nil
}

func (p *ProviderWrapper) call(ctx context.Context, fn func(context.Context) error) providerCallResult {
	result := providerCallResult{}
	breakerErr := p.breaker.Call(ctx, func(ctx context.Context) error {
		err := fn(ctx)
		if err == nil {
			return nil
		}

		result.originalErr = err
		if errors.Is(err, llm.ErrUpstream) {
			return err
		}

		// 400/401/403/404 等调用方错误必须原样返回给上层，但不能让 breaker 记录失败。
		// 否则一个坏请求会把整个 provider 熔断，影响其它正常请求。
		return nil
	})
	result.breakerErr = breakerErr
	return result
}

type providerCallResult struct {
	originalErr error
	breakerErr  error
}

func (r providerCallResult) err() error {
	if r.breakerErr != nil {
		return r.breakerErr
	}
	return r.originalErr
}

type providerOutcomeObservation struct {
	RequestedModel string
	ActualModel    string
	Err            error
	StartedAt      time.Time
	EndedAt        time.Time
}

func (p *ProviderWrapper) recordOutcome(ctx context.Context, observation providerOutcomeObservation) {
	if p == nil || p.tracer == nil {
		return
	}
	if strings.TrimSpace(p.feature) == "" {
		return
	}

	identity, ok := obs.CorrelationIdentityFromContext(ctx)
	if !ok || strings.TrimSpace(identity.AITraceID) == "" {
		return
	}

	outcomeStatus, failureStatus, degraded, rateLimited := classifyProviderOutcome(observation)
	trace := obs.NewTrace(
		identity.AITraceID,
		p.feature,
		observation.EndedAt,
		obs.WithCorrelationIdentity(identity),
		obs.WithObservationType(obs.ObservationTypeGeneration),
		obs.WithModel(observation.ActualModel),
		obs.WithLatency(0, observation.EndedAt.Sub(observation.StartedAt).Milliseconds()),
		obs.WithOutcome(outcomeStatus),
	)
	trace.FailureStatus = failureStatus
	trace.ProviderName = p.Name()
	trace.RequestedModel = observation.RequestedModel
	trace.CircuitState = string(p.breaker.State())
	trace.Degraded = degraded
	trace.RateLimited = rateLimited

	_ = obs.RecordWithExportFailureProtection(ctx, p.tracer, trace)
}

func classifyProviderOutcome(observation providerOutcomeObservation) (outcomeStatus, failureStatus string, degraded, rateLimited bool) {
	if observation.Err != nil {
		rateLimited = errors.Is(observation.Err, llm.ErrRateLimit)
		switch {
		case rateLimited:
			return "failure", string(obs.FailureRateLimit), false, true
		case errors.Is(observation.Err, llm.ErrUpstream), errors.Is(observation.Err, ErrCircuitOpen):
			return "failure", string(obs.FailureUpstream), false, false
		default:
			return "failure", string(obs.FailureCallerError), false, false
		}
	}

	degraded = observation.ActualModel != "" &&
		observation.RequestedModel != "" &&
		observation.ActualModel != observation.RequestedModel
	if degraded {
		return "degraded", "", true, false
	}
	return "success", "", false, false
}

func requestedModel(req *llm.ChatRequest) string {
	if req == nil {
		return ""
	}
	return req.Model
}

func actualModel(response *llm.ChatResponse) string {
	if response == nil {
		return ""
	}
	return response.Model
}
