package smoke

import (
	"fmt"
	"strings"
)

// PlatformSmokeConfigInput 描述真实平台 smoke 的原始配置输入。
//
// 这里允许接收 secret 字段，是因为 smoke 需要判断“是否具备外连条件”；但解析
// 结果不会保存 secret 原值，避免测试快照、日志或错误信息把平台凭据带出去。
type PlatformSmokeConfigInput struct {
	Enabled   bool
	Provider  string
	Endpoint  string
	PublicKey string
	SecretKey string
	APIKey    string
}

// PlatformSmokeConfig 是真实平台 smoke 可以直接消费的低敏配置快照。
type PlatformSmokeConfig struct {
	Ready             bool
	Provider          string
	Endpoint          string
	CredentialPresent bool
	SkipReason        string
}

// ResolvePlatformSmokeConfig 解析真实平台 smoke 配置，并保护默认离线路径。
func ResolvePlatformSmokeConfig(input PlatformSmokeConfigInput) (PlatformSmokeConfig, error) {
	if !input.Enabled {
		return skippedPlatformSmokeConfig("platform smoke is not enabled"), nil
	}

	endpoint := strings.TrimSpace(input.Endpoint)
	if endpoint == "" {
		return skippedPlatformSmokeConfig("platform smoke endpoint is not configured"), nil
	}

	if !hasPlatformSmokeCredential(input) {
		return skippedPlatformSmokeConfig("platform smoke credentials are not configured"), nil
	}

	return PlatformSmokeConfig{
		Ready:             true,
		Provider:          strings.TrimSpace(input.Provider),
		Endpoint:          endpoint,
		CredentialPresent: true,
	}, nil
}

func skippedPlatformSmokeConfig(reason string) PlatformSmokeConfig {
	return PlatformSmokeConfig{
		Ready:      false,
		SkipReason: reason,
	}
}

func hasPlatformSmokeCredential(input PlatformSmokeConfigInput) bool {
	return strings.TrimSpace(input.PublicKey) != "" ||
		strings.TrimSpace(input.SecretKey) != "" ||
		strings.TrimSpace(input.APIKey) != ""
}

// === US5：local controlled-sender 配置（T154，使 T150 GREEN）===

// local platform smoke 的显式 env 开关与 provider 变量。它们是本模式唯一允许
// 读取的环境变量：生产凭据变量（LANGFUSE_*、OPENAI_API_KEY、OTLP endpoint 等）
// 无论是否存在都不会被读取——这是"真实 endpoint/credential 默认不加载"的
// 读取面边界，由 T150 的 recording lookup 审计守护。
const (
	EnvPlatformSmokeEnabled  = "LONGTERMISM_PLATFORM_SMOKE_ENABLED"
	EnvPlatformSmokeProvider = "LONGTERMISM_PLATFORM_SMOKE_PROVIDER"
)

// PlatformSmokeLocalInput 是 local controlled-sender 模式的显式配置输入。
//
// 结构上不设 endpoint 或 credential 字段：local 模式验证的是 payload、identity
// 与 privacy 契约本身，与真实平台投递在编译期互斥。真实接收/查询能力只由
// Grafana/SigNoz 的真实 E2E 证明（US1-US4），本模式不伪造该证据。
type PlatformSmokeLocalInput struct {
	Enabled  bool
	Provider string
}

// PlatformSmokeLocalConfig 是 local 模式的低敏解析结果。
//
// 与真实平台的 PlatformSmokeConfig 不同，这里没有 CredentialPresent 字段：
// local 模式根本不持有凭据概念，避免下游代码从"凭据存在"推断出任何外连语义。
type PlatformSmokeLocalConfig struct {
	Ready      bool
	Provider   string
	SkipReason string
}

// ResolvePlatformSmokeLocalConfig 解析 local controlled-sender 配置。
//
// 语义与真实平台 resolver（ResolvePlatformSmokeConfig）刻意不同：后者是可选的
// 外部验证，配置缺失时静默 skip 是合法宽容；而 local 模式是 obs-platform-smoke
// 门禁本身的一部分——一旦显式启用，provider 缺失必须立即 fail-fast。静默 skip
// 会让 CI 在配置残缺时仍然"绿"，把装配错误伪装成通过。
func ResolvePlatformSmokeLocalConfig(input PlatformSmokeLocalInput) (PlatformSmokeLocalConfig, error) {
	if !input.Enabled {
		return PlatformSmokeLocalConfig{
			Ready:      false,
			SkipReason: "local platform smoke is not enabled",
		}, nil
	}

	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		return PlatformSmokeLocalConfig{}, fmt.Errorf("local platform smoke requires an explicit provider; refusing to guess a controlled sender")
	}

	return PlatformSmokeLocalConfig{
		Ready:    true,
		Provider: provider,
	}, nil
}

// LoadPlatformSmokeLocalInputFromEnv 从环境变量构造 local 模式输入。
//
// 只在显式 opt-in（LONGTERMISM_PLATFORM_SMOKE_ENABLED=1/true）后才读取 provider
// 变量；未 opt-in 时即使 shell 里残留全部生产凭据，本函数也只发生一次 allowlist
// 变量的读取。opt-in 值必须是明确的 1/true 或 0/false——模糊值（如 "maybe"）
// 报错而不是猜测：猜成启用会静默引入发送路径，猜成关闭会掩盖拼写错误。
// 错误只提及变量名，不回显任何读到的值。
func LoadPlatformSmokeLocalInputFromEnv(getenv func(string) string) (PlatformSmokeLocalInput, error) {
	enabled, err := parsePlatformSmokeEnvSwitch(getenv(EnvPlatformSmokeEnabled))
	if err != nil {
		return PlatformSmokeLocalInput{}, err
	}
	if !enabled {
		return PlatformSmokeLocalInput{}, nil
	}

	provider := strings.TrimSpace(getenv(EnvPlatformSmokeProvider))
	if provider == "" {
		return PlatformSmokeLocalInput{}, fmt.Errorf("%s is required when %s is enabled", EnvPlatformSmokeProvider, EnvPlatformSmokeEnabled)
	}
	return PlatformSmokeLocalInput{Enabled: true, Provider: provider}, nil
}

func parsePlatformSmokeEnvSwitch(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 1/true or 0/false; refusing to guess an unrecognized switch value", EnvPlatformSmokeEnabled)
	}
}
