package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/llm/openai"
)

const t069Secret = "T069_SECRET_MUST_NOT_LEAK"

func TestBuildLLMProviderDisabledUsesOfflineProviderWithoutReadingEnvironment(t *testing.T) {
	var (
		lookupCalls  int
		factoryCalls int
	)
	offline := &scriptedLLMProvider{responses: []llm.ChatResponse{{Content: "offline", Model: "offline-model"}}}

	provider, snapshot, err := BuildLLMProvider(context.Background(), LLMProviderConfigInput{
		ChatEnabled: false,
	}, LLMProviderDependencies{
		LookupEnv: func(string) string {
			lookupCalls++
			return t069Secret
		},
		NewOpenAI: func(openai.Config) (llm.Provider, error) {
			factoryCalls++
			return nil, errors.New("must not construct OpenAI provider when chat is disabled")
		},
		NewOfflineFake: func() llm.Provider { return offline },
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if lookupCalls != 0 || factoryCalls != 0 {
		t.Fatalf("disabled chat reads=%d factories=%d, want both zero", lookupCalls, factoryCalls)
	}
	if snapshot.Enabled || snapshot.CredentialPresent || snapshot.BaseURLPresent {
		t.Fatalf("disabled chat safe snapshot = %#v, want no configured provider", snapshot)
	}
	response, err := provider.Chat(context.Background(), validLLMChatRequest())
	if err != nil || response.Content != "offline" || offline.chatCalls != 1 {
		t.Fatalf("offline provider result = %#v, error = %v, calls = %d", response, err, offline.chatCalls)
	}
}

func TestBuildLLMProviderEnabledFailsFastForMissingConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		input LLMProviderConfigInput
		env   map[string]string
	}{
		{name: "missing base URL environment name", input: enabledLLMProviderInput("", "OPENAI_API_KEY", "chat-model")},
		{name: "missing API key environment name", input: enabledLLMProviderInput("OPENAI_BASE_URL", "", "chat-model")},
		{name: "missing default model", input: enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "")},
		{name: "blank base URL value", input: enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), env: map[string]string{"OPENAI_BASE_URL": " ", "OPENAI_API_KEY": t069Secret}},
		{name: "blank API key value", input: enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), env: map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": "\t"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factoryCalls := 0
			provider, _, err := BuildLLMProvider(context.Background(), tt.input, LLMProviderDependencies{
				LookupEnv: lookupEnv(tt.env),
				NewOpenAI: func(openai.Config) (llm.Provider, error) {
					factoryCalls++
					return nil, nil
				},
			})
			if err == nil || provider != nil {
				t.Fatalf("BuildLLMProvider() provider = %#v, error = %v; want fail-fast", provider, err)
			}
			if factoryCalls != 0 {
				t.Fatalf("OpenAI factory calls = %d, want 0 before configuration is valid", factoryCalls)
			}
			if strings.Contains(fmt.Sprint(err), t069Secret) {
				t.Fatalf("configuration error leaked secret: %v", err)
			}
		})
	}
}

func TestBuildLLMProviderCreatesSixtySecondRequestBudget(t *testing.T) {
	providerStub := &scriptedLLMProvider{responses: []llm.ChatResponse{{Content: "ok", Model: "chat-model"}}}
	var factoryConfig openai.Config
	provider, snapshot, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{
			"OPENAI_BASE_URL": "https://api.example.test/v1",
			"OPENAI_API_KEY":  t069Secret,
		}),
		NewOpenAI: func(config openai.Config) (llm.Provider, error) {
			factoryConfig = config
			return providerStub, nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if snapshot.Timeout != 60*time.Second || factoryConfig.HTTPClient == nil || factoryConfig.HTTPClient.Timeout != 60*time.Second {
		t.Fatalf("request timeout snapshot=%s client=%#v, want 60s", snapshot.Timeout, factoryConfig.HTTPClient)
	}
	if factoryConfig.HTTPClient.CheckRedirect == nil || factoryConfig.HTTPClient.CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("DI-owned HTTP client must reject redirects before a Bearer credential can be forwarded")
	}

	startedAt := time.Now()
	if _, err := provider.Chat(context.Background(), validLLMChatRequest()); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if remaining := time.Until(providerStub.lastDeadline); remaining < 59*time.Second || remaining > 60*time.Second || providerStub.lastDeadline.Before(startedAt) {
		t.Fatalf("provider deadline remaining = %s, want a 60s request budget", remaining)
	}
}

func TestBuildLLMProviderRetriesRetryableFailuresAtMostTwice(t *testing.T) {
	providerStub := &scriptedLLMProvider{
		errors: []error{
			fmt.Errorf("temporary upstream one: %w", llm.ErrUpstream),
			fmt.Errorf("temporary upstream two: %w", llm.ErrUpstream),
		},
		responses: []llm.ChatResponse{{Content: "recovered", Model: "chat-model"}},
	}
	var delays []time.Duration
	provider, _, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	response, err := provider.Chat(context.Background(), validLLMChatRequest())
	if err != nil || response.Content != "recovered" {
		t.Fatalf("Chat() response = %#v, error = %v", response, err)
	}
	if providerStub.chatCalls != 3 || !reflect.DeepEqual(delays, []time.Duration{time.Second, 3 * time.Second}) {
		t.Fatalf("retry calls=%d delays=%v, want calls=3 delays=[1s 3s]", providerStub.chatCalls, delays)
	}
}

func TestBuildLLMProviderStopsAfterTwoRetryableRetries(t *testing.T) {
	providerStub := &scriptedLLMProvider{errors: []error{
		fmt.Errorf("temporary upstream one: %w", llm.ErrUpstream),
		fmt.Errorf("temporary upstream two: %w", llm.ErrUpstream),
		fmt.Errorf("temporary upstream three: %w", llm.ErrUpstream),
	}}
	var delays []time.Duration
	provider, _, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if _, err := provider.Chat(context.Background(), validLLMChatRequest()); !errors.Is(err, llm.ErrUpstream) {
		t.Fatalf("Chat() error = %v, want final retryable upstream error", err)
	}
	if providerStub.chatCalls != 3 || !reflect.DeepEqual(delays, []time.Duration{time.Second, 3 * time.Second}) {
		t.Fatalf("exhausted retry calls=%d delays=%v, want calls=3 delays=[1s 3s]", providerStub.chatCalls, delays)
	}
}

func TestBuildLLMProviderSharesOneRequestDeadlineAcrossRetries(t *testing.T) {
	providerStub := &scriptedLLMProvider{
		errors:    []error{fmt.Errorf("temporary upstream one: %w", llm.ErrUpstream), fmt.Errorf("temporary upstream two: %w", llm.ErrUpstream)},
		responses: []llm.ChatResponse{{Content: "recovered", Model: "chat-model"}},
	}
	withTimeoutCalls := 0
	provider, _, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		WithTimeout: func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
			withTimeoutCalls++
			return context.WithTimeout(parent, timeout)
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if _, err := provider.Chat(context.Background(), validLLMChatRequest()); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if withTimeoutCalls != 1 || providerStub.chatCalls != 3 {
		t.Fatalf("request deadline constructions=%d calls=%d, want one deadline for three attempts", withTimeoutCalls, providerStub.chatCalls)
	}
	if len(providerStub.deadlines) != 3 || providerStub.deadlines[0].IsZero() {
		t.Fatalf("attempt deadlines = %v, want one non-zero deadline per attempt", providerStub.deadlines)
	}
	for attempt, deadline := range providerStub.deadlines[1:] {
		if !deadline.Equal(providerStub.deadlines[0]) {
			t.Fatalf("attempt %d deadline = %s, want the shared request deadline %s", attempt+2, deadline, providerStub.deadlines[0])
		}
	}
}

func TestBuildLLMProviderStopsWhenRequestContextEndsDuringBackoff(t *testing.T) {
	providerStub := &scriptedLLMProvider{errors: []error{fmt.Errorf("temporary upstream: %w", llm.ErrUpstream)}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	provider, _, err := BuildLLMProvider(ctx, enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		Sleep: func(backoffCtx context.Context, _ time.Duration) error {
			cancel()
			return backoffCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if _, err := provider.Chat(ctx, validLLMChatRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat() error = %v, want context.Canceled", err)
	}
	if providerStub.chatCalls != 1 {
		t.Fatalf("chat calls after cancelled backoff = %d, want 1", providerStub.chatCalls)
	}
}

func TestBuildLLMProviderDoesNotRetryNonRetryable4xx(t *testing.T) {
	// This caller error deliberately does not wrap llm.ErrUpstream. The retry policy must use
	// errors.Is rather than brittle matching against a provider's status-text formatting.
	providerStub := &scriptedLLMProvider{errors: []error{errors.New("caller supplied invalid request")}}
	var sleepCalls int
	provider, _, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		Sleep: func(context.Context, time.Duration) error {
			sleepCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if _, err := provider.Chat(context.Background(), validLLMChatRequest()); err == nil {
		t.Fatal("Chat() error = nil, want the 4xx caller error")
	}
	if providerStub.chatCalls != 1 || sleepCalls != 0 {
		t.Fatalf("non-retryable calls=%d sleeps=%d, want 1 and 0", providerStub.chatCalls, sleepCalls)
	}
}

func TestBuildLLMProviderUsesInjectedFakeWithoutExternalCallsOrSecretLeakage(t *testing.T) {
	fake := &scriptedLLMProvider{responses: []llm.ChatResponse{{Content: "fake", Model: "chat-model"}}}
	networkCalls := 0
	provider, snapshot, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewHTTPClient: func(timeout time.Duration) *http.Client {
			return &http.Client{Timeout: timeout, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				networkCalls++
				return nil, errors.New("T069 test transport must not be called")
			})}
		},
		NewOpenAI: func(config openai.Config) (llm.Provider, error) {
			if config.HTTPClient == nil {
				t.Fatal("injected fake must receive the DI-owned HTTP client")
			}
			return fake, nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if _, err := provider.Chat(context.Background(), validLLMChatRequest()); err != nil || fake.chatCalls != 1 {
		t.Fatalf("injected fake chat error=%v calls=%d", err, fake.chatCalls)
	}
	if networkCalls != 0 {
		t.Fatalf("injected fake made %d HTTP calls, want zero", networkCalls)
	}
	if rendered := fmt.Sprintf("%+v", snapshot); strings.Contains(rendered, t069Secret) || strings.Contains(rendered, "https://api.example.test/v1") {
		t.Fatalf("safe snapshot leaked a credential or endpoint: %s", rendered)
	}
	if snapshot.APIKeyEnvName != "OPENAI_API_KEY" || !snapshot.CredentialPresent || !snapshot.BaseURLPresent {
		t.Fatalf("safe snapshot = %#v, want env name and presence booleans only", snapshot)
	}
}

func TestBuildLLMProviderSanitizesFactoryAndRetryErrors(t *testing.T) {
	input := enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model")
	environment := map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}

	t.Run("factory error", func(t *testing.T) {
		provider, _, err := BuildLLMProvider(context.Background(), input, LLMProviderDependencies{
			LookupEnv: lookupEnv(environment),
			NewOpenAI: func(openai.Config) (llm.Provider, error) {
				return nil, fmt.Errorf("provider construction failed with %s", t069Secret)
			},
		})
		if provider != nil || err == nil {
			t.Fatalf("BuildLLMProvider() provider=%#v error=%v, want sanitized factory failure", provider, err)
		}
		if rendered := fmt.Sprint(err); strings.Contains(rendered, t069Secret) || strings.Contains(rendered, environment["OPENAI_BASE_URL"]) {
			t.Fatalf("factory error leaked resolved configuration: %s", rendered)
		}
	})

	t.Run("retry exhaustion", func(t *testing.T) {
		upstreamError := func(attempt int) error {
			return fmt.Errorf("upstream body %d contains %s: %w", attempt, t069Secret, llm.ErrUpstream)
		}
		upstream := &scriptedLLMProvider{errors: []error{upstreamError(1), upstreamError(2), upstreamError(3)}}
		provider, _, err := BuildLLMProvider(context.Background(), input, LLMProviderDependencies{
			LookupEnv: lookupEnv(environment),
			NewOpenAI: func(openai.Config) (llm.Provider, error) { return upstream, nil },
			Sleep:     func(context.Context, time.Duration) error { return nil },
		})
		if err != nil {
			t.Fatalf("BuildLLMProvider() error = %v", err)
		}
		_, err = provider.Chat(context.Background(), validLLMChatRequest())
		if err == nil || strings.Contains(fmt.Sprint(err), t069Secret) {
			t.Fatalf("retry error leaked secret: %v", err)
		}
	})
}

func TestBuildLLMProviderRejectsUnsafeProductionBaseURLBeforeFactory(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "plaintext HTTP", baseURL: "http://api.example.test/v1"},
		{name: "URL userinfo", baseURL: "https://user:password@api.example.test/v1"},
		{name: "query string", baseURL: "https://api.example.test/v1?target=other"},
		{name: "fragment", baseURL: "https://api.example.test/v1#fragment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factoryCalls := 0
			input := enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model")
			input.Environment = "production"
			provider, _, err := BuildLLMProvider(context.Background(), input, LLMProviderDependencies{
				LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": tt.baseURL, "OPENAI_API_KEY": t069Secret}),
				NewOpenAI: func(openai.Config) (llm.Provider, error) {
					factoryCalls++
					return nil, nil
				},
			})
			if provider != nil || err == nil || factoryCalls != 0 {
				t.Fatalf("unsafe BaseURL provider=%#v error=%v factoryCalls=%d, want fail-fast before factory", provider, err, factoryCalls)
			}
			if rendered := fmt.Sprint(err); strings.Contains(rendered, t069Secret) || strings.Contains(rendered, tt.baseURL) {
				t.Fatalf("unsafe BaseURL error leaked a secret or endpoint: %s", rendered)
			}
		})
	}
}

func TestLLMProviderUsesSafeDefaultsAndReleasesStreamBudget(t *testing.T) {
	streamSource := make(chan llm.ChatChunk, 1)
	streamSource <- llm.ChatChunk{DeltaContent: "hello"}
	close(streamSource)
	providerStub := &streamingLLMProvider{chunks: streamSource}
	cancelled := make(chan struct{})
	var cancelOnce sync.Once

	provider, snapshot, err := BuildLLMProvider(context.Background(), LLMProviderConfigInput{
		ChatEnabled:     true,
		DefaultProvider: "openai",
		BaseURLEnvName:  "OPENAI_BASE_URL",
		APIKeyEnvName:   "OPENAI_API_KEY",
		DefaultModel:    "chat-model",
		Environment:     "production",
	}, LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{
			"OPENAI_BASE_URL": "https://api.example.test/v1",
			"OPENAI_API_KEY":  t069Secret,
		}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		WithTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			return parent, func() { cancelOnce.Do(func() { close(cancelled) }) }
		},
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	if snapshot.Timeout != 60*time.Second || snapshot.RetryMax != 2 || snapshot.RetryBackoff != time.Second {
		t.Fatalf("default resilience snapshot = %#v", snapshot)
	}
	if provider.Name() != "streaming" || provider.Capabilities("chat-model").Streaming != true {
		t.Fatalf("retrying provider must forward provider identity and capabilities")
	}
	chunks, err := provider.ChatStream(context.Background(), validLLMChatRequest())
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if chunk := <-chunks; chunk.DeltaContent != "hello" {
		t.Fatalf("stream chunk = %#v, want forwarded content", chunk)
	}
	if _, open := <-chunks; open {
		t.Fatal("forwarded stream must close with the provider stream")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("stream completion must release its request budget")
	}

	offline, _, err := BuildLLMProvider(context.Background(), LLMProviderConfigInput{}, LLMProviderDependencies{})
	if err != nil || offline.Name() != "offline" {
		t.Fatalf("default offline provider=%#v error=%v", offline, err)
	}
	if _, err := offline.Chat(context.Background(), validLLMChatRequest()); err == nil {
		t.Fatal("default offline provider must not fabricate a chat result")
	}
}

func TestLLMProviderRejectsInvalidResilienceConfigurationAndSanitizesClasses(t *testing.T) {
	tests := []struct {
		name  string
		input LLMProviderConfigInput
	}{
		{name: "invalid timeout", input: LLMProviderConfigInput{Timeout: "not-a-duration"}},
		{name: "negative retry count", input: LLMProviderConfigInput{RetryMax: -1}},
		{name: "too many retries", input: LLMProviderConfigInput{RetryMax: 3}},
		{name: "negative retry backoff", input: LLMProviderConfigInput{RetryBackoff: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, err := resolveLLMResilience(tt.input); err == nil {
				t.Fatal("resolveLLMResilience() error = nil, want invalid configuration rejection")
			}
		})
	}
	if got := retryDelay(0, 500*time.Millisecond); got != 500*time.Millisecond {
		t.Fatalf("first retry delay = %s, want 500ms", got)
	}
	if got := retryDelay(1, 500*time.Millisecond); got != 1500*time.Millisecond {
		t.Fatalf("second retry delay = %s, want 1.5s", got)
	}
	if !errors.Is(sanitizeLLMProviderError(fmt.Errorf("secret body: %w", llm.ErrRateLimit)), llm.ErrRateLimit) {
		t.Fatal("sanitized rate-limit error must retain its stable classification")
	}
	if !errors.Is(sanitizeLLMProviderError(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("sanitized deadline error must retain context deadline semantics")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepWithContext() error = %v, want context.Canceled", err)
	}
}

func TestBuildLLMProviderRetriesOnlyInitialStreamConnectionFailure(t *testing.T) {
	streamSource := make(chan llm.ChatChunk, 1)
	streamSource <- llm.ChatChunk{DeltaContent: "recovered"}
	close(streamSource)
	providerStub := &streamingLLMProvider{
		errors: []error{
			fmt.Errorf("temporary stream failure: %w", llm.ErrUpstream),
			fmt.Errorf("temporary stream failure: %w", llm.ErrUpstream),
		},
		chunks: streamSource,
	}
	var delays []time.Duration
	provider, _, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	chunks, err := provider.ChatStream(context.Background(), validLLMChatRequest())
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if chunk := <-chunks; chunk.DeltaContent != "recovered" {
		t.Fatalf("stream chunk = %#v, want recovered stream", chunk)
	}
	if providerStub.streamCalls != 3 || !reflect.DeepEqual(delays, []time.Duration{time.Second, 3 * time.Second}) {
		t.Fatalf("stream calls=%d delays=%v, want calls=3 delays=[1s 3s]", providerStub.streamCalls, delays)
	}
}

func TestLLMProviderStreamCancellationAndErrorSanitization(t *testing.T) {
	streamSource := make(chan llm.ChatChunk, 2)
	streamSource <- llm.ChatChunk{DeltaContent: "first"}
	streamSource <- llm.ChatChunk{DeltaContent: "second"}
	close(streamSource)
	providerStub := &streamingLLMProvider{chunks: streamSource}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)
	budgetReleased := make(chan struct{})
	var releaseOnce sync.Once
	provider, _, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
		WithTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			return parent, func() { releaseOnce.Do(func() { close(budgetReleased) }) }
		},
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	chunks, err := provider.ChatStream(requestContext, validLLMChatRequest())
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	cancelRequest()
	select {
	case <-budgetReleased:
	case <-time.After(time.Second):
		t.Fatal("cancelling an unread stream must release its request budget")
	}
	for range chunks {
	}
}

func TestLLMProviderSanitizesStreamErrorChunks(t *testing.T) {
	streamSource := make(chan llm.ChatChunk, 1)
	streamSource <- llm.ChatChunk{Err: fmt.Errorf("provider stream leaked %s: %w", t069Secret, llm.ErrUpstream)}
	close(streamSource)
	providerStub := &streamingLLMProvider{chunks: streamSource}
	provider, _, err := BuildLLMProvider(context.Background(), enabledLLMProviderInput("OPENAI_BASE_URL", "OPENAI_API_KEY", "chat-model"), LLMProviderDependencies{
		LookupEnv: lookupEnv(map[string]string{"OPENAI_BASE_URL": "https://api.example.test/v1", "OPENAI_API_KEY": t069Secret}),
		NewOpenAI: func(openai.Config) (llm.Provider, error) { return providerStub, nil },
	})
	if err != nil {
		t.Fatalf("BuildLLMProvider() error = %v", err)
	}
	chunks, err := provider.ChatStream(context.Background(), validLLMChatRequest())
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	chunk := <-chunks
	if chunk.Err == nil || strings.Contains(chunk.Err.Error(), t069Secret) || !errors.Is(chunk.Err, llm.ErrUpstream) {
		t.Fatalf("sanitized stream error = %v, want non-secret upstream classification", chunk.Err)
	}
}

func enabledLLMProviderInput(baseURLEnvName, apiKeyEnvName, model string) LLMProviderConfigInput {
	return LLMProviderConfigInput{
		ChatEnabled:     true,
		DefaultProvider: "openai",
		BaseURLEnvName:  baseURLEnvName,
		APIKeyEnvName:   apiKeyEnvName,
		DefaultModel:    model,
		Timeout:         "60s",
		RetryMax:        2,
		RetryBackoff:    time.Second,
	}
}

func lookupEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func validLLMChatRequest() *llm.ChatRequest {
	return &llm.ChatRequest{Model: "chat-model", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}}}
}

type scriptedLLMProvider struct {
	responses    []llm.ChatResponse
	errors       []error
	chatCalls    int
	lastDeadline time.Time
	deadlines    []time.Time
}

type streamingLLMProvider struct {
	chunks      <-chan llm.ChatChunk
	errors      []error
	streamCalls int
}

func (*streamingLLMProvider) Name() string { return "streaming" }

func (*streamingLLMProvider) Capabilities(string) llm.ProviderCapabilities {
	return llm.ProviderCapabilities{Streaming: true}
}

func (*streamingLLMProvider) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, errors.New("chat is not used by the stream forwarding test")
}

func (p *streamingLLMProvider) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	p.streamCalls++
	if attempt := p.streamCalls - 1; attempt < len(p.errors) && p.errors[attempt] != nil {
		return nil, p.errors[attempt]
	}
	return p.chunks, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (p *scriptedLLMProvider) Name() string { return "scripted" }

func (p *scriptedLLMProvider) Capabilities(string) llm.ProviderCapabilities {
	return llm.ProviderCapabilities{}
}

func (p *scriptedLLMProvider) Chat(ctx context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
	p.chatCalls++
	p.lastDeadline, _ = ctx.Deadline()
	p.deadlines = append(p.deadlines, p.lastDeadline)
	index := p.chatCalls - 1
	if index < len(p.errors) && p.errors[index] != nil {
		return nil, p.errors[index]
	}
	responseIndex := index - len(p.errors)
	if responseIndex < 0 || responseIndex >= len(p.responses) {
		return nil, errors.New("scripted provider has no response")
	}
	response := p.responses[responseIndex]
	return &response, nil
}

func (p *scriptedLLMProvider) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	return nil, errors.New("streaming is not used by the T069 DI contract")
}
