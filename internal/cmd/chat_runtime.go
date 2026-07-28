package cmd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	controllerchat "github.com/ashjazz/Longtermism/internal/controller/chat"
	appeval "github.com/ashjazz/Longtermism/internal/eval"
	logicchat "github.com/ashjazz/Longtermism/internal/logic/chat"
	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"go.opentelemetry.io/otel"
)

const (
	chatPromptTemplateVersion = "direct-user-message-v1"
	chatPromptContract        = "messages[0].role=user;messages[0].content=request.message"
	chatTracerName            = "github.com/ashjazz/Longtermism/internal/logic/chat"
)

// ChatRuntimeConfig 是启用 chat 后的已解析应用配置。它只保存非敏感事实；
// endpoint 与 API key 仍由 BuildLLMProvider 在临时构造边界读取。
type ChatRuntimeConfig struct {
	Enabled               bool
	DebugEnabled          bool
	Provider              LLMProviderConfigInput
	RateLimit             ChatRateLimitConfig
	PromptTemplateVersion string
	PayloadMode           obs.PayloadMode
	Dataset               aieval.DatasetIdentity
	SampleID              string
	MetricName            string
	EvalThreshold         float64
	Evidence              appeval.LocalEvidenceStoreConfig
}

type chatRuntimeEvidenceStore interface {
	logicchat.ChatEvidenceStore
	Close() error
}

type ChatRuntimeDependencies struct {
	BuildProvider    func(context.Context, LLMProviderConfigInput, LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error)
	Provider         LLMProviderDependencies
	OpenEvidence     func(appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error)
	NewAITraceID     func() string
	NewEvalRunID     func() string
	TracerName       string
	RequestIDFromCtx func(context.Context) string
}

// ChatRuntime 持有 chat 独占资源。ObservabilityBootstrap 不在此生命周期内：
// 它由 Main 创建一次并最后关闭，防止 chat 重装或提前关闭全局 provider。
type ChatRuntime struct {
	Enabled bool
	Handler ghttp.HandlerFunc
	Limit   ChatRateLimitConfig
	close   func() error
}

func (runtime *ChatRuntime) Close() error {
	if runtime == nil || runtime.close == nil {
		return nil
	}
	return runtime.close()
}

// BuildChatRuntime 将已经存在的 observability bootstrap、单个 LLM provider、
// evaluator 与本地 evidence store 组合成一条 chat handler。禁用路径在任何 secret
// lookup、文件打开或 provider construction 之前返回。
func BuildChatRuntime(
	ctx context.Context,
	config ChatRuntimeConfig,
	bootstrap *ObservabilityBootstrap,
	dependencies ChatRuntimeDependencies,
) (*ChatRuntime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("build chat runtime: context is required")
	}
	if !config.Enabled {
		return &ChatRuntime{Enabled: false, Limit: config.RateLimit}, nil
	}
	if bootstrap == nil {
		return nil, fmt.Errorf("build chat runtime: observability bootstrap is required")
	}
	if !hasActiveChatTelemetryLifecycle(bootstrap) {
		return nil, fmt.Errorf("build chat runtime: active telemetry lifecycle is required")
	}
	if err := validateChatRuntimeConfig(config); err != nil {
		return nil, err
	}

	dependencies = defaultChatRuntimeDependencies(dependencies)
	evaluator, err := logicchat.NewCompletionContractEvaluator(logicchat.CompletionContractEvaluatorConfig{
		Dataset:    config.Dataset,
		SampleID:   config.SampleID,
		MetricName: config.MetricName,
		Threshold:  cloneChatThreshold(config.EvalThreshold),
	})
	if err != nil {
		return nil, fmt.Errorf("build chat runtime: evaluator configuration is invalid")
	}
	providerConfig := config.Provider
	providerConfig.ChatEnabled = true
	provider, snapshot, err := dependencies.BuildProvider(ctx, providerConfig, dependencies.Provider)
	if err != nil {
		return nil, fmt.Errorf("build chat runtime: provider unavailable")
	}
	if provider == nil ||
		!snapshot.Enabled ||
		strings.TrimSpace(snapshot.Provider) == "" ||
		strings.TrimSpace(snapshot.DefaultModel) == "" {
		return nil, fmt.Errorf("build chat runtime: provider snapshot is invalid")
	}
	evidenceStore, err := dependencies.OpenEvidence(config.Evidence)
	if err != nil {
		return nil, fmt.Errorf("build chat runtime: evidence storage unavailable")
	}
	if evidenceStore == nil {
		return nil, fmt.Errorf("build chat runtime: evidence storage unavailable")
	}

	tracer := otel.Tracer(dependencies.TracerName)
	usecase := logicchat.NewChatUsecase(logicchat.ChatUsecaseDependencies{
		Provider:                provider,
		RequestedModel:          snapshot.DefaultModel,
		ProviderName:            snapshot.Provider,
		PromptTemplateVersion:   config.PromptTemplateVersion,
		PromptHash:              chatPromptContractHash(),
		PayloadMode:             exportedChatPayloadMode(config.PayloadMode),
		PayloadRedacted:         config.PayloadMode != obs.PayloadModeMetadataOnly,
		NewAITraceID:            dependencies.NewAITraceID,
		NewEvalRunID:            dependencies.NewEvalRunID,
		CanonicalizeActualModel: exactConfiguredModel(snapshot.DefaultModel),
		Bridge:                  appobservability.NewChatAIExecutionBoundary(tracer),
		GenerationObserver:      appobservability.NewGenerationSpanAdapter(tracer),
		Evaluator:               evaluator,
		EvaluatorObserver:       appobservability.NewEvaluatorSpanAdapter(tracer),
		EvidenceStore:           evidenceStore,
		// T101 owns the bounded Langfuse score projection worker. Local evidence remains
		// the source of truth now; a nil queue records no platform status and never blocks chat.
		ProjectionQueue: nil,
	})
	controller := controllerchat.NewV1(controllerchat.ChatControllerDependencies{
		Usecase:              usecase,
		RequestIDFromContext: dependencies.RequestIDFromCtx,
		DebugEnabled:         config.DebugEnabled,
	})
	return &ChatRuntime{
		Enabled: config.Enabled,
		Handler: newChatHTTPHandler(controllerchat.NewHTTPHandler(controller)),
		Limit:   config.RateLimit,
		close:   evidenceStore.Close,
	}, nil
}

func buildDefaultChatRuntime(ctx context.Context, bootstrap *ObservabilityBootstrap) (*ChatRuntime, error) {
	if bootstrap == nil {
		return nil, fmt.Errorf("build default chat runtime: observability bootstrap is required")
	}
	enabled := g.Cfg().MustGet(ctx, "ai.chat.enabled", false).Bool()
	config := ChatRuntimeConfig{
		Enabled:      enabled,
		DebugEnabled: g.Cfg().MustGet(ctx, "app.is_debug", false).Bool(),
		Provider: LLMProviderConfigInput{
			ChatEnabled:     enabled,
			DefaultProvider: g.Cfg().MustGet(ctx, "ai.llm.default_provider", "").String(),
			BaseURLEnvName:  g.Cfg().MustGet(ctx, "ai.llm.providers.openai.base_url_env", "").String(),
			APIKeyEnvName:   g.Cfg().MustGet(ctx, "ai.llm.providers.openai.api_key_env", "").String(),
			DefaultModel:    g.Cfg().MustGet(ctx, "ai.llm.providers.openai.default_model", "").String(),
			Timeout:         g.Cfg().MustGet(ctx, "ai.llm.timeout", "60s").String(),
			RetryMax:        g.Cfg().MustGet(ctx, "ai.llm.retry.max", 2).Int(),
			RetryBackoff:    time.Duration(g.Cfg().MustGet(ctx, "ai.llm.retry.backoffMs", 1000).Int()) * time.Millisecond,
			Environment:     g.Cfg().MustGet(ctx, "app.environment", "local").String(),
		},
		RateLimit: ChatRateLimitConfig{
			Rate:   g.Cfg().MustGet(ctx, "ai.chat.rate_limit.rate", defaultChatRate).Int(),
			Period: g.Cfg().MustGet(ctx, "ai.chat.rate_limit.period", defaultChatPeriod.String()).Duration(),
		},
		PromptTemplateVersion: g.Cfg().MustGet(ctx, "ai.chat.prompt.template_version", "").String(),
		PayloadMode:           bootstrap.Runtime.Payload.Mode(),
		Dataset: aieval.DatasetIdentity{
			Name:    g.Cfg().MustGet(ctx, "ai.chat.eval.dataset.name", "").String(),
			Version: g.Cfg().MustGet(ctx, "ai.chat.eval.dataset.version", "").String(),
		},
		SampleID:   g.Cfg().MustGet(ctx, "ai.chat.eval.sample_id", "").String(),
		MetricName: g.Cfg().MustGet(ctx, "ai.chat.eval.metric_name", "").String(),
		EvalThreshold: g.Cfg().
			MustGet(ctx, "ai.chat.eval.threshold", 1.0).
			Float64(),
		Evidence: appeval.LocalEvidenceStoreConfig{
			Path:      g.Cfg().MustGet(ctx, "ai.chat.eval.evidence.path", "").String(),
			Retention: g.Cfg().MustGet(ctx, "ai.chat.eval.evidence.retention", appeval.DefaultLocalEvidenceRetention.String()).Duration(),
		},
	}
	return BuildChatRuntime(ctx, config, bootstrap, ChatRuntimeDependencies{
		Provider: LLMProviderDependencies{LookupEnv: os.Getenv},
	})
}

func defaultChatRuntimeDependencies(dependencies ChatRuntimeDependencies) ChatRuntimeDependencies {
	if dependencies.BuildProvider == nil {
		dependencies.BuildProvider = BuildLLMProvider
	}
	if dependencies.OpenEvidence == nil {
		dependencies.OpenEvidence = func(config appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error) {
			return appeval.OpenLocalEvidenceStore(config)
		}
	}
	if dependencies.NewAITraceID == nil {
		dependencies.NewAITraceID = newChatExecutionID
	}
	if dependencies.NewEvalRunID == nil {
		dependencies.NewEvalRunID = newChatExecutionID
	}
	if dependencies.TracerName == "" {
		dependencies.TracerName = chatTracerName
	}
	if dependencies.RequestIDFromCtx == nil {
		dependencies.RequestIDFromCtx = RequestIDFromContext
	}
	return dependencies
}

func validateChatRuntimeConfig(config ChatRuntimeConfig) error {
	if strings.TrimSpace(config.PromptTemplateVersion) != chatPromptTemplateVersion ||
		config.RateLimit.Rate <= 0 ||
		config.RateLimit.Period <= 0 ||
		config.EvalThreshold <= 0 ||
		config.EvalThreshold > 1 {
		return fmt.Errorf("build chat runtime: configuration is invalid")
	}
	switch config.PayloadMode {
	case obs.PayloadModeMetadataOnly, obs.PayloadModeContentRedacted, obs.PayloadModeContentRaw:
	default:
		return fmt.Errorf("build chat runtime: payload policy is invalid")
	}
	return nil
}

func hasActiveChatTelemetryLifecycle(bootstrap *ObservabilityBootstrap) bool {
	if bootstrap == nil || bootstrap.Lifecycle == nil {
		return false
	}
	status := bootstrap.Lifecycle.Status()
	return status.Initialized &&
		!status.InitializationFailed &&
		!status.Shutdown
}

func cloneChatThreshold(value float64) *float64 {
	cloned := value
	return &cloned
}

func exactConfiguredModel(model string) logicchat.CanonicalizeActualModel {
	return func(actual string) (string, bool) {
		if actual != model {
			return "", false
		}
		return model, true
	}
}

func exportedChatPayloadMode(mode obs.PayloadMode) obs.PayloadMode {
	if mode == obs.PayloadModeContentRaw {
		// raw 只存在于受限本地工件；任何可外发 generation 仍声明为已脱敏内容。
		return obs.PayloadModeContentRedacted
	}
	return mode
}

func chatPromptContractHash() string {
	digest := sha256.Sum256([]byte(chatPromptContract))
	return fmt.Sprintf("sha256:%x", digest)
}

func newChatExecutionID() string {
	identifier, err := newOpaqueRequestID()
	if err != nil {
		return ""
	}
	return identifier
}

func newChatHTTPHandler(handler http.Handler) ghttp.HandlerFunc {
	return func(request *ghttp.Request) {
		if handler == nil {
			request.Response.WriteStatus(http.StatusInternalServerError)
			return
		}
		request.Request = request.Request.WithContext(request.GetCtx())
		handler.ServeHTTP(request.Response.BufferWriter, request.Request)
	}
}
