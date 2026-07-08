package smoke

import (
	"context"
	"fmt"
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
