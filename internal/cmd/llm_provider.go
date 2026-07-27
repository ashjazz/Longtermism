package cmd

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/llm/openai"
	"github.com/ashjazz/Longtermism/pkg/ai/resilience"
	"github.com/gogf/gf/v2/errors/gerror"
)

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
	return resilience.NewProviderWrapper(
		provider,
		resilience.NewCircuitBreaker(resilience.Config{}),
		resilience.WithExecutionPolicy(resilience.ProviderExecutionPolicy{
			Timeout:      timeout,
			RetryMax:     retryMax,
			RetryBackoff: retryBackoff,
		}),
	), snapshot, nil
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
	return dependencies
}

func resolveLLMResilience(input LLMProviderConfigInput) (time.Duration, int, time.Duration, error) {
	policy := resilience.DefaultProviderExecutionPolicy()
	timeout := policy.Timeout
	if rawTimeout := strings.TrimSpace(input.Timeout); rawTimeout != "" {
		parsed, err := time.ParseDuration(rawTimeout)
		if err != nil || parsed <= 0 {
			return 0, 0, 0, gerror.New("LLM provider timeout is invalid")
		}
		timeout = parsed
	}
	if input.RetryMax != 0 {
		policy.RetryMax = input.RetryMax
	}
	if input.RetryBackoff != 0 {
		policy.RetryBackoff = input.RetryBackoff
	}
	policy.Timeout = timeout
	if err := policy.Validate(); err != nil {
		return 0, 0, 0, gerror.New("LLM provider resilience configuration is invalid")
	}
	return policy.Timeout, policy.RetryMax, policy.RetryBackoff, nil
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
