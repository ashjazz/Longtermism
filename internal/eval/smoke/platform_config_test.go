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

// === US5：local controlled-sender 配置契约（T150 RED，T154 实现）===
//
// 与上方真实平台 resolver 的关键语义差异：真实平台 smoke 是可选的外部验证，
// 配置缺失时静默 skip 是合法的宽容；而 local controlled-sender 是 obs-platform-smoke
// 门禁本身的一部分——一旦显式启用，配置残缺必须立即 fail-fast，否则 CI 会在
// "看似通过"的状态下漏掉配置错误。另一个不可妥协点：local 输入结构上不持有
// endpoint 或 credential 字段，"local 模式绝不加载真实凭据"由编译期结构保证，
// 而不是靠运行时校验兜底。

func TestPlatformSmokeLocalConfigDefaultsToDisabled(t *testing.T) {
	// 生产风险：开发者 shell 或 CI 里残留外部服务变量时，轻量验证不得因此
	// 隐式启用任何发送路径。默认关闭是合法的工程安全默认，必须是无错误的
	// skip，而不是把"未配置"误报为失败。
	got, err := ResolvePlatformSmokeLocalConfig(PlatformSmokeLocalInput{})
	if err != nil {
		t.Fatalf("ResolvePlatformSmokeLocalConfig() error = %v, want nil for default disabled input", err)
	}
	if got.Ready {
		t.Fatalf("Ready = true, want false for default disabled input")
	}
	if !strings.Contains(got.SkipReason, "not enabled") {
		t.Fatalf("SkipReason = %q, want it to explain the smoke is not enabled", got.SkipReason)
	}
	if strings.TrimSpace(got.Provider) != "" {
		t.Fatalf("Provider = %q, want empty for disabled config", got.Provider)
	}
}

func TestPlatformSmokeLocalConfigResolvesCompleteLocalTestConfig(t *testing.T) {
	// 完整 local 测试配置只需显式 provider：受控发送方标识是调用方必须给出的
	// 业务事实，解析器不得替它猜测默认值（语义优先约束）。结果不携带 endpoint、
	// 不携带 credential presence——local 模式与真实平台投递在结构上互斥。
	got, err := ResolvePlatformSmokeLocalConfig(PlatformSmokeLocalInput{
		Enabled:  true,
		Provider: "local",
	})
	if err != nil {
		t.Fatalf("ResolvePlatformSmokeLocalConfig() error = %v, want nil for complete local config", err)
	}
	if !got.Ready {
		t.Fatalf("Ready = false, want true for complete local config (SkipReason = %q)", got.SkipReason)
	}
	if got.Provider != "local" {
		t.Fatalf("Provider = %q, want %q", got.Provider, "local")
	}
	if got.SkipReason != "" {
		t.Fatalf("SkipReason = %q, want empty for ready config", got.SkipReason)
	}

	assertPlatformSmokeLocalConfigDoesNotEchoSecrets(t, got)
}

func TestPlatformSmokeLocalConfigFailsFastWhenEnabledButIncomplete(t *testing.T) {
	tests := []struct {
		name   string
		input  PlatformSmokeLocalInput
		wantErrPart string
	}{
		{
			name:         "enabled without provider fails fast instead of skipping",
			input:        PlatformSmokeLocalInput{Enabled: true},
			wantErrPart:  "provider",
		},
		{
			name:         "enabled with blank provider fails fast",
			input:        PlatformSmokeLocalInput{Enabled: true, Provider: "   "},
			wantErrPart:  "provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 显式启用后的半配置必须报错：静默 skip 会让门禁在配置错误时
			// 仍然"绿"，这与真实平台 resolver 的宽容语义刻意不同。
			got, err := ResolvePlatformSmokeLocalConfig(tt.input)
			if err == nil {
				t.Fatalf("ResolvePlatformSmokeLocalConfig() error = nil, want fail-fast error for incomplete enabled config (got %#v)", got)
			}
			if !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Fatalf("error = %q, want it to name the missing %q field", err.Error(), tt.wantErrPart)
			}
			if got.Ready {
				t.Fatalf("Ready = true alongside error, want disabled result on failure")
			}
		})
	}
}

func TestPlatformSmokeLocalEnvLoadRequiresExplicitOptInAndNeverReadsCredentials(t *testing.T) {
	t.Run("production credential variables never enable or leak into local input", func(t *testing.T) {
		// 模拟一个残留了生产凭据的 shell：env 加载器在没有显式 opt-in 时
		// 必须保持 disabled，且读取面只允许 platform smoke 自己的 allowlist
		// 变量——绝不触碰 LANGFUSE_*、OPENAI_API_KEY 或 OTLP endpoint。
		lookup := newRecordingPlatformEnvLookup(withProductionCredentialLeftovers())

		input, err := LoadPlatformSmokeLocalInputFromEnv(lookup.Getenv)
		if err != nil {
			t.Fatalf("LoadPlatformSmokeLocalInputFromEnv() error = %v, want nil when opt-in is absent", err)
		}
		if input.Enabled {
			t.Fatalf("Enabled = true, want false without explicit opt-in")
		}
		lookup.assertOnlyLookedUpPlatformSmokeVariables(t)

		assertPlatformSmokeLocalInputDoesNotEchoSecrets(t, input, withProductionCredentialLeftovers())
	})

	t.Run("explicit opt-in with provider resolves complete local input", func(t *testing.T) {
		lookup := newRecordingPlatformEnvLookup(withProductionCredentialLeftovers())
		lookup.values[EnvPlatformSmokeEnabled] = "1"
		lookup.values[EnvPlatformSmokeProvider] = "local"

		input, err := LoadPlatformSmokeLocalInputFromEnv(lookup.Getenv)
		if err != nil {
			t.Fatalf("LoadPlatformSmokeLocalInputFromEnv() error = %v, want nil for explicit opt-in", err)
		}
		if !input.Enabled {
			t.Fatalf("Enabled = false, want true after explicit opt-in")
		}
		if input.Provider != "local" {
			t.Fatalf("Provider = %q, want %q", input.Provider, "local")
		}
		lookup.assertOnlyLookedUpPlatformSmokeVariables(t)
	})

	t.Run("explicit opt-in without provider fails fast", func(t *testing.T) {
		lookup := newRecordingPlatformEnvLookup(withProductionCredentialLeftovers())
		lookup.values[EnvPlatformSmokeEnabled] = "1"

		input, err := LoadPlatformSmokeLocalInputFromEnv(lookup.Getenv)
		if err == nil {
			t.Fatalf("LoadPlatformSmokeLocalInputFromEnv() error = nil, want fail-fast error for missing provider")
		}
		if !strings.Contains(err.Error(), EnvPlatformSmokeProvider) {
			t.Fatalf("error = %q, want it to name %q without echoing any value", err.Error(), EnvPlatformSmokeProvider)
		}
		if input.Enabled {
			t.Fatalf("Enabled = true alongside error, want disabled input on failure")
		}
		lookup.assertOnlyLookedUpPlatformSmokeVariables(t)
	})

	t.Run("unrecognized opt-in value fails fast instead of guessing", func(t *testing.T) {
		// opt-in 只接受明确的 1/true 与 0/false。模糊值必须报错：把 "maybe"
		// 猜成启用会静默外连，猜成关闭会掩盖配置拼写错误，两者都不可接受。
		lookup := newRecordingPlatformEnvLookup(nil)
		lookup.values[EnvPlatformSmokeEnabled] = "maybe"

		input, err := LoadPlatformSmokeLocalInputFromEnv(lookup.Getenv)
		if err == nil {
			t.Fatalf("LoadPlatformSmokeLocalInputFromEnv() error = nil, want fail-fast error for unrecognized opt-in value")
		}
		if !strings.Contains(err.Error(), EnvPlatformSmokeEnabled) {
			t.Fatalf("error = %q, want it to name %q", err.Error(), EnvPlatformSmokeEnabled)
		}
		if input.Enabled {
			t.Fatalf("Enabled = true alongside error, want disabled input on failure")
		}
	})

	t.Run("explicit opt-out stays disabled without error", func(t *testing.T) {
		lookup := newRecordingPlatformEnvLookup(withProductionCredentialLeftovers())
		lookup.values[EnvPlatformSmokeEnabled] = "0"

		input, err := LoadPlatformSmokeLocalInputFromEnv(lookup.Getenv)
		if err != nil {
			t.Fatalf("LoadPlatformSmokeLocalInputFromEnv() error = %v, want nil for explicit opt-out", err)
		}
		if input.Enabled {
			t.Fatalf("Enabled = true, want false after explicit opt-out")
		}
		lookup.assertOnlyLookedUpPlatformSmokeVariables(t)
	})
}

// withProductionCredentialLeftovers 模拟生产 shell 中常见的凭据残留。它们
// 只作为"绝不能被读取或泄漏"的哨兵值存在，不是任何真实环境的凭据。
func withProductionCredentialLeftovers() map[string]string {
	return map[string]string{
		"LANGFUSE_PUBLIC_KEY":                        "pk-production-leftover",
		"LANGFUSE_SECRET_KEY":                        "sk-production-leftover",
		"OPENAI_API_KEY":                             "sk-openai-production-leftover",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL": "basic-production-leftover",
		"OTEL_EXPORTER_OTLP_ENDPOINT":                "https://collector.production.example.test",
	}
}

type recordingPlatformEnvLookup struct {
	values  map[string]string
	lookups []string
}

func newRecordingPlatformEnvLookup(values map[string]string) *recordingPlatformEnvLookup {
	if values == nil {
		values = map[string]string{}
	}
	return &recordingPlatformEnvLookup{values: values}
}

func (lookup *recordingPlatformEnvLookup) Getenv(name string) string {
	lookup.lookups = append(lookup.lookups, name)
	return lookup.values[name]
}

// assertOnlyLookedUpPlatformSmokeVariables 审计 env 加载器的读取面：任何一次
// 读取都不得越过 platform smoke 自己的变量 allowlist。这是"真实 endpoint 与
// credential 默认不加载"的结构性证明——靠记录读取行为，而不是靠相信实现自觉。
func (lookup *recordingPlatformEnvLookup) assertOnlyLookedUpPlatformSmokeVariables(t *testing.T) {
	t.Helper()

	allowed := map[string]bool{
		EnvPlatformSmokeEnabled:  true,
		EnvPlatformSmokeProvider: true,
	}
	for _, name := range lookup.lookups {
		if !allowed[name] {
			t.Fatalf("env loader read non-allowlisted variable %q (lookups: %v); local platform smoke must never read production credential variables", name, lookup.lookups)
		}
	}
}

func assertPlatformSmokeLocalConfigDoesNotEchoSecrets(t *testing.T, config PlatformSmokeLocalConfig) {
	t.Helper()

	rendered := fmt.Sprintf("%#v", config)
	for _, secret := range withProductionCredentialLeftovers() {
		if strings.Contains(rendered, secret) {
			t.Fatalf("PlatformSmokeLocalConfig echoed credential leftover %q: %s", secret, rendered)
		}
	}
}

func assertPlatformSmokeLocalInputDoesNotEchoSecrets(t *testing.T, input PlatformSmokeLocalInput, secrets map[string]string) {
	t.Helper()

	rendered := fmt.Sprintf("%#v", input)
	for _, secret := range secrets {
		if strings.Contains(rendered, secret) {
			t.Fatalf("PlatformSmokeLocalInput echoed credential leftover %q: %s", secret, rendered)
		}
	}
}
