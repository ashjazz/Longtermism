package smoke

import "strings"

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
