package cmd

import (
	"fmt"
	"strings"
)

// ObservabilitySink 描述应用层观测输出目标。
//
// Phase 3 先把“是否会外连”这件事做成显式配置结果：默认 no-op，离线验证走
// local sink，真实平台必须配置完整后才允许 exporter 拨打外部 endpoint。
type ObservabilitySink string

const (
	ObservabilitySinkNoop     ObservabilitySink = "noop"
	ObservabilitySinkLocal    ObservabilitySink = "local"
	ObservabilitySinkPlatform ObservabilitySink = "platform"
)

// ObservabilityConfigInput 是应用配置到观测初始化之间的窄 DTO。
type ObservabilityConfigInput struct {
	Enabled  bool
	Sink     ObservabilitySink
	Platform ObservabilityPlatformConfig
}

// ObservabilityPlatformConfig 保存真实平台 exporter 的最小配置摘要。
//
// 这里不保存 secret 原值到解析结果里；ResolveObservabilityConfig 只判断是否具备
// 凭据，避免后续日志或测试快照把密钥带出去。
type ObservabilityPlatformConfig struct {
	Provider  string
	Endpoint  string
	PublicKey string
	SecretKey string
	APIKey    string
}

// ObservabilityConfig 是观测初始化可以直接消费的安全解析结果。
type ObservabilityConfig struct {
	Enabled               bool
	Sink                  ObservabilitySink
	LocalSinkEnabled      bool
	ExternalExportEnabled bool
	ExternalEndpoint      string
	ExternalSkipReason    string
}

// ResolveObservabilityConfig 解析应用观测配置，并确保默认路径不会访问外部平台。
func ResolveObservabilityConfig(input ObservabilityConfigInput) (ObservabilityConfig, error) {
	if !input.Enabled {
		return ObservabilityConfig{
			Enabled: false,
			Sink:    ObservabilitySinkNoop,
		}, nil
	}

	sink := input.Sink
	if sink == "" {
		sink = ObservabilitySinkLocal
	}

	switch sink {
	case ObservabilitySinkNoop:
		return ObservabilityConfig{
			Enabled: false,
			Sink:    ObservabilitySinkNoop,
		}, nil
	case ObservabilitySinkLocal:
		return ObservabilityConfig{
			Enabled:          true,
			Sink:             ObservabilitySinkLocal,
			LocalSinkEnabled: true,
		}, nil
	case ObservabilitySinkPlatform:
		return resolvePlatformObservabilityConfig(input.Platform), nil
	default:
		return ObservabilityConfig{}, fmt.Errorf("unsupported observability sink %q", sink)
	}
}

func resolvePlatformObservabilityConfig(platform ObservabilityPlatformConfig) ObservabilityConfig {
	endpoint := strings.TrimSpace(platform.Endpoint)
	if endpoint == "" {
		return skippedPlatformConfig("platform endpoint is not configured")
	}
	if !hasPlatformCredential(platform) {
		return skippedPlatformConfig("platform credentials are not configured")
	}

	return ObservabilityConfig{
		Enabled:               true,
		Sink:                  ObservabilitySinkPlatform,
		ExternalExportEnabled: true,
		ExternalEndpoint:      endpoint,
	}
}

func skippedPlatformConfig(reason string) ObservabilityConfig {
	return ObservabilityConfig{
		Enabled:            false,
		Sink:               ObservabilitySinkNoop,
		ExternalSkipReason: reason,
	}
}

func hasPlatformCredential(platform ObservabilityPlatformConfig) bool {
	return strings.TrimSpace(platform.PublicKey) != "" ||
		strings.TrimSpace(platform.SecretKey) != "" ||
		strings.TrimSpace(platform.APIKey) != ""
}
