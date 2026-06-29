package resilience

import (
	"context"
	"errors"
	"fmt"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
)

// ProviderWrapper 用断路器保护 llm.Provider。
//
// 这里刻意保持 llm.Provider 接口不变：调用方仍然只看到 Name/Capabilities/Chat/ChatStream。
// wrapper 只在内部判断错误是否属于 llm.ErrUpstream，并据此决定是否计入断路器失败。
type ProviderWrapper struct {
	provider llm.Provider
	breaker  CircuitBreaker
}

// NewProviderWrapper 创建带 resilience 保护的 provider。
func NewProviderWrapper(provider llm.Provider, breaker CircuitBreaker) *ProviderWrapper {
	return &ProviderWrapper{
		provider: provider,
		breaker:  breaker,
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

	var response *llm.ChatResponse
	err := p.call(ctx, func(ctx context.Context) error {
		got, err := p.provider.Chat(ctx, req)
		response = got
		return err
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

	var chunks <-chan llm.ChatChunk
	err := p.call(ctx, func(ctx context.Context) error {
		got, err := p.provider.ChatStream(ctx, req)
		chunks = got
		return err
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

func (p *ProviderWrapper) call(ctx context.Context, fn func(context.Context) error) error {
	var originalErr error
	breakerErr := p.breaker.Call(ctx, func(ctx context.Context) error {
		err := fn(ctx)
		if err == nil {
			return nil
		}

		originalErr = err
		if errors.Is(err, llm.ErrUpstream) {
			return err
		}

		// 400/401/403/404 等调用方错误必须原样返回给上层，但不能让 breaker 记录失败。
		// 否则一个坏请求会把整个 provider 熔断，影响其它正常请求。
		return nil
	})
	if breakerErr != nil {
		return breakerErr
	}
	return originalErr
}
