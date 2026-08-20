package smoke

import (
	"context"
	"fmt"
	"strings"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

const (
	defaultPlatformSmokeScenario       = ObservabilityChainScenarioSuccess
	defaultPlatformSmokeRequestID      = "req-platform-smoke-default"
	defaultPlatformSmokeServiceTraceID = "svc-trace-platform-smoke-default"
	defaultPlatformSmokeSpanID         = "span-platform-smoke-default"
	defaultPlatformSmokeAITraceID      = "ai-trace-platform-smoke-default"
	defaultPlatformSmokeEvalRunID      = "eval-run-platform-smoke-default"
	defaultPlatformSmokeSampleID       = "sample-platform-smoke-default"
)

// PlatformSmokeSender 是真实平台 smoke 的最小发送端口。
//
// Phase 8 先把 smoke runner 与具体平台 SDK 解耦：测试使用内存 sender，后续
// Langfuse/OTel adapter 只需要实现这个端口，不能把平台 SDK 类型泄漏进 smoke 契约。
type PlatformSmokeSender interface {
	SendPlatformSmoke(ctx context.Context, payload PlatformSmokePayload) error
}

// PlatformSmokeRunConfig 描述一次真实平台 opt-in smoke 运行。
type PlatformSmokeRunConfig struct {
	Config         PlatformSmokeConfigInput
	Sender         PlatformSmokeSender
	Scenario       ObservabilityChainScenario
	RequestID      string
	ServiceTraceID string
	SpanID         string
	AITraceID      string
	EvalRunID      string
	SampleID       string
}

// PlatformSmokePayload 是发送给真实平台 adapter 的低敏最小链路。
type PlatformSmokePayload struct {
	Platform       PlatformSmokeConfig
	RequestID      string
	ServiceTraceID string
	RootSpanID     string
	RootAITraceID  string
	EvalRunID      string
	ServiceStages  []ObservabilityChainServiceStage
	AIObservations []ObservabilityChainAIObservation
	EvalEvidence   []ObservabilityChainEvalEvidence
}

// PlatformSmokeResult 描述一次平台 smoke 的安全执行结果。
type PlatformSmokeResult struct {
	Ready       bool
	Skipped     bool
	SkipReason  string
	PayloadSent bool
}

// RunPlatformSmoke 执行真实平台 opt-in smoke 的最小发送路径。
//
// 默认配置必须只返回 skipped，不触发 sender。只有显式启用、endpoint 和凭据齐备
// 时才构造一条低敏双平面链路并交给 sender。这里不直接依赖任何真实平台 SDK，
// 因为 SDK 接入属于 adapter 责任，smoke runner 只验证发送边界和 payload 语义。
func RunPlatformSmoke(ctx context.Context, config PlatformSmokeRunConfig) (PlatformSmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	platform, err := ResolvePlatformSmokeConfig(config.Config)
	if err != nil {
		return PlatformSmokeResult{}, err
	}
	if !platform.Ready {
		return PlatformSmokeResult{
			Ready:      false,
			Skipped:    true,
			SkipReason: platform.SkipReason,
		}, nil
	}
	if config.Sender == nil {
		return PlatformSmokeResult{}, fmt.Errorf("platform smoke sender is required when config is ready")
	}

	chain, err := RunObservabilityChainSmoke(ctx, ObservabilityChainSmokeConfig{
		Scenario:       platformSmokeScenarioOrDefault(config.Scenario),
		RequestID:      stringOrDefault(config.RequestID, defaultPlatformSmokeRequestID),
		ServiceTraceID: stringOrDefault(config.ServiceTraceID, defaultPlatformSmokeServiceTraceID),
		SpanID:         stringOrDefault(config.SpanID, defaultPlatformSmokeSpanID),
		AITraceID:      stringOrDefault(config.AITraceID, defaultPlatformSmokeAITraceID),
		EvalRunID:      stringOrDefault(config.EvalRunID, defaultPlatformSmokeEvalRunID),
		SampleID:       stringOrDefault(config.SampleID, defaultPlatformSmokeSampleID),
	})
	if err != nil {
		return PlatformSmokeResult{}, err
	}

	if err := config.Sender.SendPlatformSmoke(ctx, platformSmokePayload(platform, chain)); err != nil {
		return PlatformSmokeResult{}, fmt.Errorf("failed to send platform smoke payload: %w", err)
	}

	return PlatformSmokeResult{
		Ready:       true,
		PayloadSent: true,
	}, nil
}

func platformSmokePayload(platform PlatformSmokeConfig, chain ObservabilityChainSmokeResult) PlatformSmokePayload {
	return PlatformSmokePayload{
		Platform:       platform,
		RequestID:      chain.RequestID,
		ServiceTraceID: chain.ServiceTraceID,
		RootSpanID:     chain.RootSpanID,
		RootAITraceID:  chain.RootAITraceID,
		EvalRunID:      chain.EvalRunID,
		ServiceStages:  append([]ObservabilityChainServiceStage(nil), chain.ServiceStages...),
		AIObservations: append([]ObservabilityChainAIObservation(nil), chain.AIObservations...),
		EvalEvidence:   append([]ObservabilityChainEvalEvidence(nil), chain.EvalEvidence...),
	}
}

func platformSmokeScenarioOrDefault(scenario ObservabilityChainScenario) ObservabilityChainScenario {
	if scenario == "" {
		return defaultPlatformSmokeScenario
	}
	return scenario
}

func stringOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// === US5：local controlled-sender runner（T155，使 T151 GREEN）===

// localPlatformSmokeFeature 是受控链路的 feature 标识。它会进入 AI span 的
// ai.feature 路由属性，因此保持低敏、稳定，不携带任何请求内容。
const localPlatformSmokeFeature = "local_platform_contract_smoke"

// LocalPlatformSmokeTransport 是 local platform smoke 全部潜在外连出口的审计端口。
//
// 受控实现只计数不外连：Attempts() > 0 即证明 local 路径被错误地接入了真实
// 出口。当前 runner 没有任何合法的外连路径，transport 在这里是架构哨兵——未来
// 若有人为 local 模式接入真实 exporter，必须经过本接口并立即被 T151 的零外连
// 断言暴露。
type LocalPlatformSmokeTransport interface {
	Dial(network, address string) error
	Attempts() int
}

// LocalPlatformSmokeRunConfig 描述一次 local controlled-sender 受控运行。
//
// identity 六项是调用方必须显式给出的业务事实：runner 不用默认常量填充缺失
// 身份（与真实平台 runner 的 defaultPlatformSmoke* 行为刻意不同），否则
// "identity 分离"会退化成"identity 碰巧不冲突"，掩盖装配错误。
type LocalPlatformSmokeRunConfig struct {
	Config         PlatformSmokeLocalInput
	Transport      LocalPlatformSmokeTransport
	Scenario       ObservabilityChainScenario
	RequestID      string
	ServiceTraceID string
	SpanID         string
	AITraceID      string
	EvalRunID      string
	SampleID       string
}

// LocalPlatformSmokeInfraSpan 是基础设施平面的 span 快照。
//
// Attributes 由生产 MapSpanRoutingAttributes 对 http_child 角色生成（返回
// nil）：infra 平面不携带任何 AI marker 是生产路由决策，不是本包的判定。
type LocalPlatformSmokeInfraSpan struct {
	Name           string
	RequestID      string
	ServiceTraceID string
	SpanID         string
	Attributes     map[string]string
}

// LocalPlatformSmokeAISpan 是 AI 平面的 span 快照。
//
// 全部字段（名称、semantic 类型、身份、路由 marker attributes）都投影自生产
// MapTraceToSpanSnapshot：AI marker 语义的单一来源在生产 mapper，本包维护
// 第二套 marker 判定是被 T155 门控明确禁止的。
type LocalPlatformSmokeAISpan struct {
	Name            string
	ObservationType obs.ObservationType
	RequestID       string
	ServiceTraceID  string
	ParentSpanID    string
	AITraceID       string
	Attributes      map[string]string
}

// LocalPlatformSmokePayload 是受控运行构造的双平面最小链路证据。
//
// 与 infra/AI 平面分离同时保持可关联：AI span 通过 ServiceTraceID +
// ParentSpanID 指回 infra 平面，而不是借用 infra 身份字段；eval evidence
// 以自己的 EvalRunID 回查 request 与 AI 身份。
type LocalPlatformSmokePayload struct {
	RequestID      string
	ServiceTraceID string
	RootSpanID     string
	RootAITraceID  string
	EvalRunID      string
	InfraStages    []LocalPlatformSmokeInfraSpan
	AISpans        []LocalPlatformSmokeAISpan
	EvalEvidence   []ObservabilityChainEvalEvidence
}

// LocalPlatformSmokeResult 是一次 local 受控运行的安全执行结果。
type LocalPlatformSmokeResult struct {
	Ready       bool
	Skipped     bool
	SkipReason  string
	PayloadSent bool
	Payload     LocalPlatformSmokePayload
}

// RunLocalPlatformSmoke 执行 local controlled-sender 受控链路。
//
// 它不连接任何真实平台：marker、identity 与隐私契约由生产 mapper 与 chain
// 构造器在进程内证明；真实接收/查询只由 Grafana/SigNoz E2E 验收。运行全程
// 不调用 Transport.Dial——transport 只作为外连审计哨兵存在。
func RunLocalPlatformSmoke(ctx context.Context, config LocalPlatformSmokeRunConfig) (LocalPlatformSmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// transport 是外连审计的强制通道：缺失即拒绝运行，不允许构造一个
	// "看不见外连"的受控验证（哪怕配置是 disabled 的 skip 路径）。
	if config.Transport == nil {
		return LocalPlatformSmokeResult{}, fmt.Errorf("local platform smoke transport is required; a controlled run must never execute unaudited")
	}

	platform, err := ResolvePlatformSmokeLocalConfig(config.Config)
	if err != nil {
		return LocalPlatformSmokeResult{}, err
	}
	if !platform.Ready {
		return LocalPlatformSmokeResult{
			Skipped:    true,
			SkipReason: platform.SkipReason,
		}, nil
	}

	if err := validateLocalPlatformSmokeIdentity(config); err != nil {
		return LocalPlatformSmokeResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return LocalPlatformSmokeResult{}, err
	}

	chain, err := RunObservabilityChainSmoke(ctx, ObservabilityChainSmokeConfig{
		Scenario:       platformSmokeScenarioOrDefault(config.Scenario),
		RequestID:      strings.TrimSpace(config.RequestID),
		ServiceTraceID: strings.TrimSpace(config.ServiceTraceID),
		SpanID:         strings.TrimSpace(config.SpanID),
		AITraceID:      strings.TrimSpace(config.AITraceID),
		EvalRunID:      strings.TrimSpace(config.EvalRunID),
		SampleID:       strings.TrimSpace(config.SampleID),
	})
	if err != nil {
		return LocalPlatformSmokeResult{}, err
	}

	payload, err := buildLocalPlatformSmokePayload(chain)
	if err != nil {
		return LocalPlatformSmokeResult{}, err
	}

	return LocalPlatformSmokeResult{
		Ready:       true,
		PayloadSent: true,
		Payload:     payload,
	}, nil
}

// validateLocalPlatformSmokeIdentity 强制六项身份全部显式存在。
//
// 错误只点名缺失的身份类别，不回显任何已提供的值。
func validateLocalPlatformSmokeIdentity(config LocalPlatformSmokeRunConfig) error {
	required := []struct {
		value string
		name  string
	}{
		{config.RequestID, "request id"},
		{config.ServiceTraceID, "service trace id"},
		{config.SpanID, "root span id"},
		{config.AITraceID, "AI trace id"},
		{config.EvalRunID, "eval run id"},
		{config.SampleID, "eval sample id"},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("local platform smoke requires an explicit %s; identity facts must not be defaulted", field.name)
		}
	}
	return nil
}

func buildLocalPlatformSmokePayload(chain ObservabilityChainSmokeResult) (LocalPlatformSmokePayload, error) {
	identity := obs.NewCorrelationIdentity(
		chain.RequestID,
		obs.WithServiceSpan(chain.ServiceTraceID, chain.RootSpanID),
		obs.WithAITraceID(chain.RootAITraceID),
		obs.WithEvalRunID(chain.EvalRunID),
	)

	infraStages := make([]LocalPlatformSmokeInfraSpan, 0, len(chain.ServiceStages))
	for _, stage := range chain.ServiceStages {
		// 生产路由决策：http_child 属于基础设施角色，MapSpanRoutingAttributes
		// 对其返回 nil——infra 平面无 AI marker 是生产语义而非本地约定。
		attributes, err := obs.MapSpanRoutingAttributes(obs.SpanRoutingInput{
			Role:     obs.SpanRoutingRoleHTTPChild,
			Identity: identity,
			Feature:  localPlatformSmokeFeature,
		})
		if err != nil {
			return LocalPlatformSmokePayload{}, fmt.Errorf("route local platform infra stage %q: %w", stage.Name, err)
		}
		infraStages = append(infraStages, LocalPlatformSmokeInfraSpan{
			Name:           stage.Name,
			RequestID:      stage.RequestID,
			ServiceTraceID: stage.ServiceTraceID,
			SpanID:         stage.SpanID,
			Attributes:     attributes,
		})
	}

	aiSpans := make([]LocalPlatformSmokeAISpan, 0, len(chain.AIObservations))
	for _, observation := range chain.AIObservations {
		span, err := localPlatformSmokeAISpan(observation, identity)
		if err != nil {
			return LocalPlatformSmokePayload{}, err
		}
		aiSpans = append(aiSpans, span)
	}

	return LocalPlatformSmokePayload{
		RequestID:      chain.RequestID,
		ServiceTraceID: chain.ServiceTraceID,
		RootSpanID:     chain.RootSpanID,
		RootAITraceID:  chain.RootAITraceID,
		EvalRunID:      chain.EvalRunID,
		InfraStages:    infraStages,
		AISpans:        aiSpans,
		EvalEvidence:   append([]ObservabilityChainEvalEvidence(nil), chain.EvalEvidence...),
	}, nil
}

// localPlatformSmokeAISpan 用生产 mapper 把一个 AI 语义阶段映射成 span 快照。
//
// span 名称（ai.<observation_type>）、AI 平面路由 marker（plane/designated/
// ai.trace_id/request.id）与身份安全校验全部来自 MapTraceToSpanSnapshot；
// 本函数只做字段投影，杜绝第二套 marker 语义。
func localPlatformSmokeAISpan(observation ObservabilityChainAIObservation, identity obs.CorrelationIdentity) (LocalPlatformSmokeAISpan, error) {
	trace := obs.NewTrace(
		identity.AITraceID,
		localPlatformSmokeFeature,
		mustObservabilityChainTime(observabilityChainStartedAt),
		obs.WithCorrelationIdentity(identity),
		obs.WithObservationType(observation.ObservationType),
		obs.WithOutcome(observation.OutcomeStatus),
	)
	snapshot, err := obs.MapTraceToSpanSnapshot(trace)
	if err != nil {
		return LocalPlatformSmokeAISpan{}, fmt.Errorf("map local platform AI observation %q to a span snapshot: %w", observation.ObservationType, err)
	}
	return LocalPlatformSmokeAISpan{
		Name:            snapshot.Name,
		ObservationType: snapshot.ObservationType,
		RequestID:       snapshot.RequestID,
		ServiceTraceID:  snapshot.ServiceTraceID,
		ParentSpanID:    snapshot.ParentSpanID,
		AITraceID:       snapshot.AITraceID,
		Attributes:      snapshot.Attributes,
	}, nil
}
