package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/llm/openai"
	"github.com/gogf/gf/v2/errors/gerror"
)

const (
	defaultLLMRequestTimeout = 60 * time.Second
	defaultLLMRetryMax       = 2
	defaultLLMRetryBackoff   = time.Second
)

var llmRetryDelayMultipliers = [...]time.Duration{1, 3}

// LLMProviderConfigInput is the composition-root representation of the checked-in
// configuration. BaseURL/API key are environment-variable names rather than resolved values.
type LLMProviderConfigInput struct {
	ChatEnabled     bool
	DefaultProvider string
	BaseURLEnvName  string
	APIKeyEnvName   string
	DefaultModel    string
	Timeout         string
	RetryMax        int
	RetryBackoff    time.Duration
	Environment     string
}

// LLMProviderConfigSnapshot records only low-sensitivity configuration facts for diagnostics.
// In particular, it deliberately excludes resolved endpoint and credential values.
type LLMProviderConfigSnapshot struct {
	Enabled           bool
	Provider          string
	APIKeyEnvName     string
	CredentialPresent bool
	BaseURLPresent    bool
	DefaultModel      string
	Timeout           time.Duration
	RetryMax          int
	RetryBackoff      time.Duration
}

// LLMProviderDependencies keeps secret lookup, networking, time, and provider construction
// injectable. Offline tests can therefore prove the default path without external calls.
//
// TODO(provider-adapter-factory): T089 只交付 OpenAI-compatible adapter，因此 cmd 暂时
// 通过 NewOpenAI 完成选择。等 Anthropic 等 adapter 落地时，应由各 adapter 自己公开
// provider-specific Config + NewProvider/factory；这里仅保留 provider kind 选择、环境变量
// 引用校验和通用 resilience 装配。不要在 cmd 演化一个同时容纳所有 provider 专属字段的
// 大 Config 或 Functional Options 集合，否则会允许无意义的配置组合并污染 llm 核心端口。
type LLMProviderDependencies struct {
	LookupEnv      func(string) string
	NewHTTPClient  func(time.Duration) *http.Client
	NewOpenAI      func(openai.Config) (llm.Provider, error)
	NewOfflineFake func() llm.Provider
	WithTimeout    func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	Sleep          func(context.Context, time.Duration) error
}

// BuildLLMProvider 是当前唯一的 OpenAI-compatible 应用装配边界。禁用 chat 时绝不读取
// 环境变量；启用时必须在 client 构建前 fail-fast，不能静默返回 fake result。
//
// 这个函数刻意不属于 pkg/ai/llm：核心包只能定义 Provider 端口，不能反向依赖 OpenAI
// 或未来的 Anthropic adapter。见 LLMProviderDependencies 的重构 TODO。
func BuildLLMProvider(ctx context.Context, input LLMProviderConfigInput, dependencies LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
	dependencies = defaultLLMProviderDependencies(dependencies)
	if !input.ChatEnabled {
		return dependencies.NewOfflineFake(), LLMProviderConfigSnapshot{}, nil
	}

	providerName := strings.TrimSpace(input.DefaultProvider)
	if providerName != "openai" {
		return nil, LLMProviderConfigSnapshot{}, gerror.New("LLM provider is unsupported")
	}
	baseURLEnvName := strings.TrimSpace(input.BaseURLEnvName)
	apiKeyEnvName := strings.TrimSpace(input.APIKeyEnvName)
	model := strings.TrimSpace(input.DefaultModel)
	if !validLLMEnvironmentName(baseURLEnvName) || !validLLMEnvironmentName(apiKeyEnvName) || model == "" {
		return nil, LLMProviderConfigSnapshot{}, gerror.New("LLM provider configuration is incomplete")
	}

	baseURL := strings.TrimSpace(dependencies.LookupEnv(baseURLEnvName))
	apiKey := strings.TrimSpace(dependencies.LookupEnv(apiKeyEnvName))
	snapshot := LLMProviderConfigSnapshot{
		Enabled:           true,
		Provider:          providerName,
		APIKeyEnvName:     apiKeyEnvName,
		CredentialPresent: apiKey != "",
		BaseURLPresent:    baseURL != "",
		DefaultModel:      model,
	}
	if baseURL == "" || apiKey == "" {
		return nil, snapshot, gerror.New("LLM provider configuration is incomplete")
	}
	if err := validateProductionLLMBaseURL(input.Environment, baseURL); err != nil {
		return nil, snapshot, err
	}

	timeout, retryMax, retryBackoff, err := resolveLLMResilience(input)
	if err != nil {
		return nil, snapshot, err
	}
	snapshot.Timeout = timeout
	snapshot.RetryMax = retryMax
	snapshot.RetryBackoff = retryBackoff

	provider, err := dependencies.NewOpenAI(openai.Config{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		DefaultModel: model,
		HTTPClient:   dependencies.NewHTTPClient(timeout),
	})
	if err != nil {
		return nil, snapshot, gerror.New("LLM provider construction failed")
	}
	if provider == nil {
		return nil, snapshot, gerror.New("LLM provider construction failed")
	}
	return &retryingLLMProvider{
		provider:     provider,
		timeout:      timeout,
		retryMax:     retryMax,
		retryBackoff: retryBackoff,
		withTimeout:  dependencies.WithTimeout,
		sleep:        dependencies.Sleep,
	}, snapshot, nil
}

func defaultLLMProviderDependencies(dependencies LLMProviderDependencies) LLMProviderDependencies {
	if dependencies.LookupEnv == nil {
		dependencies.LookupEnv = func(name string) string { return "" }
	}
	if dependencies.NewHTTPClient == nil {
		dependencies.NewHTTPClient = newLLMHTTPClient
	}
	if dependencies.NewOpenAI == nil {
		dependencies.NewOpenAI = func(config openai.Config) (llm.Provider, error) { return openai.NewProvider(config) }
	}
	if dependencies.NewOfflineFake == nil {
		dependencies.NewOfflineFake = func() llm.Provider { return offlineLLMProvider{} }
	}
	if dependencies.WithTimeout == nil {
		dependencies.WithTimeout = context.WithTimeout
	}
	if dependencies.Sleep == nil {
		dependencies.Sleep = sleepWithContext
	}
	return dependencies
}

func resolveLLMResilience(input LLMProviderConfigInput) (time.Duration, int, time.Duration, error) {
	timeout := defaultLLMRequestTimeout
	if rawTimeout := strings.TrimSpace(input.Timeout); rawTimeout != "" {
		parsed, err := time.ParseDuration(rawTimeout)
		if err != nil || parsed <= 0 {
			return 0, 0, 0, gerror.New("LLM provider timeout is invalid")
		}
		timeout = parsed
	}
	retryMax := input.RetryMax
	if retryMax == 0 {
		retryMax = defaultLLMRetryMax
	}
	if retryMax < 0 || retryMax > defaultLLMRetryMax {
		return 0, 0, 0, gerror.New("LLM provider retry limit is invalid")
	}
	retryBackoff := input.RetryBackoff
	if retryBackoff == 0 {
		retryBackoff = defaultLLMRetryBackoff
	}
	if retryBackoff <= 0 {
		return 0, 0, 0, gerror.New("LLM provider retry backoff is invalid")
	}
	return timeout, retryMax, retryBackoff, nil
}

func newLLMHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		// Never forward the Authorization header to a redirect target. An upstream base URL is
		// deployment configuration, but redirect destinations are not part of that trust boundary.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func validateProductionLLMBaseURL(environment, rawURL string) error {
	if !strings.EqualFold(strings.TrimSpace(environment), productionEnvironment) {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return gerror.New("LLM provider base URL is invalid for production")
	}
	return nil
}

func validLLMEnvironmentName(name string) bool {
	for index, character := range name {
		if character == '_' || (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return name != ""
}

type retryingLLMProvider struct {
	provider     llm.Provider
	timeout      time.Duration
	retryMax     int
	retryBackoff time.Duration
	withTimeout  func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	sleep        func(context.Context, time.Duration) error
}

// TODO(provider-resilience-refactor): 当前 T089 为最小 OpenAI-compatible 路径，暂在
// composition root 实现总 deadline、retry 与流取消。`pkg/ai/resilience.ProviderWrapper`
// 已拥有断路器和 outcome 观测；引入第二个 provider 前，必须先把这些通用执行策略合并
// 到 resilience 的独立 decorator，再由 cmd 组合一次，避免多层 wrapper 对重试、熔断和
// 错误脱敏作出不一致判断。此处不能直接复制到 logic：logic 只选择业务策略，不执行流
// 转发、退避或网络安全边界。
// 目标组合顺序必须是 ProviderWrapper(RetryTimeout(adapter))：一次用户调用只让断路器
// 与 outcome 观察最终结果一次，而不是让每次临时重试都触发熔断或重复记录。

func (p *retryingLLMProvider) Name() string { return p.provider.Name() }

func (p *retryingLLMProvider) Capabilities(model string) llm.ProviderCapabilities {
	return p.provider.Capabilities(model)
}

func (p *retryingLLMProvider) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := p.withTimeout(ctx, p.timeout)
	defer cancel()

	for attempt := 0; ; attempt++ {
		response, err := p.provider.Chat(requestCtx, request)
		if err == nil {
			return response, nil
		}
		if contextErr := requestCtx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if !errors.Is(err, llm.ErrUpstream) || attempt >= p.retryMax {
			return nil, sanitizeLLMProviderError(err)
		}
		if err := p.sleep(requestCtx, retryDelay(attempt, p.retryBackoff)); err != nil {
			return nil, err
		}
	}
}

func (p *retryingLLMProvider) ChatStream(ctx context.Context, request *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := p.withTimeout(ctx, p.timeout)
	chunks, err := p.openChatStream(requestCtx, request)
	if err != nil {
		cancel()
		return nil, err
	}
	// Stream ownership moves to the consumer once established. Forwarding through a local channel
	// lets this wrapper cancel its request budget exactly when the provider closes the stream.
	forwarded := make(chan llm.ChatChunk, 1)
	go func() {
		defer cancel()
		defer close(forwarded)
		for chunk := range chunks {
			if chunk.Err != nil {
				chunk.Err = sanitizeLLMProviderError(chunk.Err)
			}
			select {
			case forwarded <- chunk:
			case <-requestCtx.Done():
				return
			}
		}
	}()
	return forwarded, nil
}

// openChatStream retries only before a chunk escapes the provider boundary. Replaying after a
// partial stream would duplicate model output or tool-call effects, so it is deliberately unsafe.
func (p *retryingLLMProvider) openChatStream(ctx context.Context, request *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	for attempt := 0; ; attempt++ {
		chunks, err := p.provider.ChatStream(ctx, request)
		if err == nil {
			return chunks, nil
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if !errors.Is(err, llm.ErrUpstream) || attempt >= p.retryMax {
			return nil, sanitizeLLMProviderError(err)
		}
		if err := p.sleep(ctx, retryDelay(attempt, p.retryBackoff)); err != nil {
			return nil, err
		}
	}
}

func retryDelay(attempt int, base time.Duration) time.Duration {
	if attempt >= len(llmRetryDelayMultipliers) {
		return base * llmRetryDelayMultipliers[len(llmRetryDelayMultipliers)-1]
	}
	return base * llmRetryDelayMultipliers[attempt]
}

func sanitizeLLMProviderError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, llm.ErrRateLimit) {
		return fmt.Errorf("LLM provider request failed: %w", errors.Join(llm.ErrUpstream, llm.ErrRateLimit))
	}
	if errors.Is(err, llm.ErrUpstream) {
		return fmt.Errorf("LLM provider request failed: %w", llm.ErrUpstream)
	}
	return gerror.New("LLM provider request failed")
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type offlineLLMProvider struct{}

func (offlineLLMProvider) Name() string { return "offline" }

func (offlineLLMProvider) Capabilities(string) llm.ProviderCapabilities {
	return llm.ProviderCapabilities{}
}

func (offlineLLMProvider) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, gerror.New("chat is disabled")
}

func (offlineLLMProvider) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	return nil, gerror.New("chat is disabled")
}
