package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
)

func TestProviderWrapperPassesThroughProviderSemantics(t *testing.T) {
	t.Parallel()

	provider := newCountingProvider()
	provider.capabilities["capable-model"] = llm.ProviderCapabilities{ToolCalling: true}
	provider.chatResponses["ok-model"] = llm.ChatResponse{
		Content:      "ok",
		Model:        "ok-model",
		FinishReason: llm.FinishStop,
	}
	wrapped := NewProviderWrapper(provider, NewCircuitBreaker(Config{FailureThreshold: 1}))

	if wrapped.Name() != provider.Name() {
		t.Fatalf("Name() = %q, want provider name", wrapped.Name())
	}
	if !wrapped.Capabilities("capable-model").ToolCalling {
		t.Fatal("Capabilities() did not pass through provider capability")
	}

	got, err := wrapped.Chat(context.Background(), &llm.ChatRequest{Model: "ok-model"})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Content != "ok" {
		t.Fatalf("Chat() content = %q, want ok", got.Content)
	}
	if provider.ChatCalls() != 1 {
		t.Fatalf("provider chat calls = %d, want 1", provider.ChatCalls())
	}
}

func TestProviderWrapperOpensCircuitOnErrUpstream(t *testing.T) {
	t.Parallel()

	provider := newCountingProvider()
	provider.chatErrors["upstream-model"] = fmt.Errorf("provider unavailable: %w", llm.ErrUpstream)
	provider.chatResponses["ok-model"] = llm.ChatResponse{Content: "ok", FinishReason: llm.FinishStop}
	breaker := NewCircuitBreaker(Config{
		FailureThreshold: 1,
		RecoveryTimeout:  time.Minute,
	})
	wrapped := NewProviderWrapper(provider, breaker)

	_, err := wrapped.Chat(context.Background(), &llm.ChatRequest{Model: "upstream-model"})
	if !errors.Is(err, llm.ErrUpstream) {
		t.Fatalf("upstream Chat() error = %v, want preserve llm.ErrUpstream", err)
	}
	if breaker.State() != StateOpen {
		t.Fatalf("breaker State() = %q, want %q", breaker.State(), StateOpen)
	}

	_, err = wrapped.Chat(context.Background(), &llm.ChatRequest{Model: "ok-model"})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("open circuit Chat() error = %v, want ErrCircuitOpen", err)
	}
	if provider.ChatCalls() != 1 {
		t.Fatalf("provider chat calls = %d, want 1 because second call fast-fails", provider.ChatCalls())
	}
}

func TestProviderWrapperDoesNotOpenCircuitOnCallerError(t *testing.T) {
	t.Parallel()

	callerErr := errors.New("openai chat request failed with status 400")
	provider := newCountingProvider()
	provider.chatErrors["bad-request-model"] = callerErr
	provider.chatResponses["ok-model"] = llm.ChatResponse{Content: "ok", FinishReason: llm.FinishStop}
	breaker := NewCircuitBreaker(Config{
		FailureThreshold: 1,
		RecoveryTimeout:  time.Minute,
	})
	wrapped := NewProviderWrapper(provider, breaker)

	_, err := wrapped.Chat(context.Background(), &llm.ChatRequest{Model: "bad-request-model"})
	if !errors.Is(err, callerErr) {
		t.Fatalf("caller Chat() error = %v, want preserve caller error", err)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("breaker State() = %q, want %q after caller error", breaker.State(), StateClosed)
	}

	got, err := wrapped.Chat(context.Background(), &llm.ChatRequest{Model: "ok-model"})
	if err != nil {
		t.Fatalf("next Chat() error = %v", err)
	}
	if got.Content != "ok" {
		t.Fatalf("next Chat() content = %q, want ok", got.Content)
	}
	if provider.ChatCalls() != 2 {
		t.Fatalf("provider chat calls = %d, want both calls to reach provider", provider.ChatCalls())
	}
}

type countingProvider struct {
	mu            sync.Mutex
	chatCalls     int
	capabilities  map[string]llm.ProviderCapabilities
	chatResponses map[string]llm.ChatResponse
	chatErrors    map[string]error
}

func newCountingProvider() *countingProvider {
	return &countingProvider{
		capabilities:  make(map[string]llm.ProviderCapabilities),
		chatResponses: make(map[string]llm.ChatResponse),
		chatErrors:    make(map[string]error),
	}
}

func (p *countingProvider) Name() string {
	return "counting-provider"
}

func (p *countingProvider) Capabilities(model string) llm.ProviderCapabilities {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.capabilities[model]
}

func (p *countingProvider) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.chatCalls++
	if err, ok := p.chatErrors[req.Model]; ok {
		return nil, err
	}
	response, ok := p.chatResponses[req.Model]
	if !ok {
		return nil, fmt.Errorf("missing response for model %q", req.Model)
	}
	return &response, nil
}

func (p *countingProvider) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	return nil, fmt.Errorf("counting provider stream is not used by provider wrapper tests")
}

func (p *countingProvider) ChatCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.chatCalls
}
