package smoke

import (
	"fmt"
	"strings"
	"testing"
)

func TestResolvePlatformSmokeConfigRequiresOptInAndCompleteSettings(t *testing.T) {
	tests := []struct {
		name                  string
		input                 PlatformSmokeConfigInput
		wantReady             bool
		wantProvider          string
		wantEndpoint          string
		wantSkipReasonPart    string
		wantCredentialPresent bool
	}{
		{
			name: "disabled switch skips even when endpoint and credentials are present",
			input: PlatformSmokeConfigInput{
				Enabled:   false,
				Provider:  "otlp",
				Endpoint:  "https://collector.example.test",
				PublicKey: "pk-platform-smoke",
				SecretKey: "sk-platform-smoke-secret",
			},
			wantReady:          false,
			wantSkipReasonPart: "not enabled",
		},
		{
			name: "missing endpoint skips without contacting platform",
			input: PlatformSmokeConfigInput{
				Enabled:   true,
				Provider:  "otlp",
				SecretKey: "sk-platform-smoke-secret",
			},
			wantReady:          false,
			wantSkipReasonPart: "endpoint",
		},
		{
			name: "missing credentials skips without contacting platform",
			input: PlatformSmokeConfigInput{
				Enabled:  true,
				Provider: "otlp",
				Endpoint: "https://collector.example.test",
			},
			wantReady:          false,
			wantSkipReasonPart: "credentials",
		},
		{
			name: "complete opt-in config is ready and keeps only credential presence",
			input: PlatformSmokeConfigInput{
				Enabled:   true,
				Provider:  "otlp",
				Endpoint:  " https://collector.example.test ",
				SecretKey: "sk-platform-smoke-secret",
			},
			wantReady:             true,
			wantProvider:          "otlp",
			wantEndpoint:          "https://collector.example.test",
			wantCredentialPresent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 真实平台 smoke 必须显式 opt-in。配置缺失时返回可读 skip reason，
			// 配置完整时也只暴露“凭据存在”这个低敏事实，避免后续日志或测试快照泄露 secret。
			got, err := ResolvePlatformSmokeConfig(tt.input)
			if err != nil {
				t.Fatalf("ResolvePlatformSmokeConfig() error = %v", err)
			}

			if got.Ready != tt.wantReady {
				t.Fatalf("Ready = %v, want %v", got.Ready, tt.wantReady)
			}
			if got.Provider != tt.wantProvider {
				t.Fatalf("Provider = %q, want %q", got.Provider, tt.wantProvider)
			}
			if got.Endpoint != tt.wantEndpoint {
				t.Fatalf("Endpoint = %q, want %q", got.Endpoint, tt.wantEndpoint)
			}
			if got.CredentialPresent != tt.wantCredentialPresent {
				t.Fatalf("CredentialPresent = %v, want %v", got.CredentialPresent, tt.wantCredentialPresent)
			}
			if tt.wantSkipReasonPart != "" && !strings.Contains(got.SkipReason, tt.wantSkipReasonPart) {
				t.Fatalf("SkipReason = %q, want to contain %q", got.SkipReason, tt.wantSkipReasonPart)
			}
			if tt.wantReady && got.SkipReason != "" {
				t.Fatalf("SkipReason = %q, want empty for ready config", got.SkipReason)
			}

			assertPlatformSmokeConfigDoesNotEchoSecrets(t, got, []string{
				tt.input.PublicKey,
				tt.input.SecretKey,
				tt.input.APIKey,
			})
		})
	}
}

func assertPlatformSmokeConfigDoesNotEchoSecrets(t *testing.T, config PlatformSmokeConfig, secrets []string) {
	t.Helper()

	rendered := fmt.Sprintf("%#v", config)
	for _, secret := range secrets {
		if strings.TrimSpace(secret) == "" {
			continue
		}
		if strings.Contains(rendered, secret) {
			t.Fatalf("PlatformSmokeConfig echoed secret value %q: %#v", secret, config)
		}
	}
}
