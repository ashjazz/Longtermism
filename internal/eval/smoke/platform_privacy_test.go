package smoke

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

// === US5：平台 payload 隐私契约（T152 RED，T156 实现）===
//
// 这些测试固定 obs-platform-smoke 的隐私承诺：受控验证构造的 payload 在三种
// 生产 payload policy 下都不携带合成敏感原文，baggage 只经生产 allowlist 构造，
// 任何命中都以"立即失败 + 类别/计数"暴露且绝不回显原文。canary 值刻意设计成
// 能命中生产 scanner 的模式（sk-、JWT、手机号），保证"clean"结论不是假阴性。

// platformPrivacyCanaryFixture 返回一组合成敏感哨兵。它们只用于注入受控验证，
// 不是任何真实环境的凭据；每个值都必须命中生产 scanner 的判定模式或 marker 扫描。
func platformPrivacyCanaryFixture() PlatformPrivacyCanary {
	return PlatformPrivacyCanary{
		// 命中生产 sk- 前缀凭据模式。
		APIKey: "sk-platform-privacy-canary-a1b2c3",
		// 命中生产 JWT（eyJ...）模式。
		JWT: "eyJhbGciOi.platform.canary.payload",
		// 普通密码形态：由 marker 扫描兜底（生产模式不覆盖普通口令）。
		Password: "platform-privacy-canary-password-9f8e",
		// 命中生产手机号 PII 模式。
		MobilePII: "13800001234",
		// 业务原文：metadata-only 与 redacted 模式下都不得原样外发。
		RawQuery: "platform privacy canary raw query 原文",
		Prompt:   "platform privacy canary prompt 原文",
	}
}

// platformPrivacyCanaryValues 展开哨兵值集合，用于逐值断言"零原文回显"。
func platformPrivacyCanaryValues(canary PlatformPrivacyCanary) []string {
	return []string{
		canary.APIKey,
		canary.JWT,
		canary.Password,
		canary.MobilePII,
		canary.RawQuery,
		canary.Prompt,
	}
}

func platformPrivacyScanIdentity() obs.CorrelationIdentity {
	return obs.NewCorrelationIdentity(
		"req-platform-privacy-001",
		obs.WithServiceSpan("svc-trace-platform-privacy-001", "span-platform-privacy-001"),
		obs.WithAITraceID("ai-trace-platform-privacy-001"),
		obs.WithEvalRunID("eval-run-platform-privacy-001"),
	)
}

func TestPlatformSmokePrivacyScanCoversAllThreePayloadPolicies(t *testing.T) {
	// 三种生产 payload policy 下，受控验证的外发表面都必须对合成 canary 零命中。
	// 特别注意 content_raw：显式授权只解锁本地调试工件，不改变外发边界——
	// "raw 已开启"绝不能成为原文进入观测 payload 的理由（FR-006）。
	tests := []struct {
		name              string
		payloadMode       obs.PayloadMode
		environment       string
		rawContentEnabled bool
	}{
		{
			name:        "metadata only keeps payload free of canary content",
			payloadMode: obs.PayloadModeMetadataOnly,
			environment: "local",
		},
		{
			name:        "content redacted keeps payload free of raw canary",
			payloadMode: obs.PayloadModeContentRedacted,
			environment: "local",
		},
		{
			name:              "content raw still never emits raw canary externally",
			payloadMode:       obs.PayloadModeContentRaw,
			environment:       "local",
			rawContentEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := RunPlatformPayloadPrivacyScan(context.Background(), PlatformPrivacyScanConfig{
				PayloadMode:       tt.payloadMode,
				Environment:       tt.environment,
				RawContentEnabled: tt.rawContentEnabled,
				Identity:          platformPrivacyScanIdentity(),
				Canary:            platformPrivacyCanaryFixture(),
			})
			if err != nil {
				t.Fatalf("RunPlatformPayloadPrivacyScan() error = %v, want nil for a clean controlled payload", err)
			}
			if !result.Clean {
				t.Fatalf("Clean = false under policy %q (findings: %#v)", tt.payloadMode, result.Findings)
			}
			if result.PolicyMode != tt.payloadMode {
				t.Fatalf("PolicyMode = %q, want %q", result.PolicyMode, tt.payloadMode)
			}

			assertPlatformPrivacyOutputHasNoCanaryValues(t, fmt.Sprintf("%#v", result), platformPrivacyCanaryValues(platformPrivacyCanaryFixture()))
		})
	}
}

func TestPlatformSmokePrivacyScanFailsFastWithoutDeclaredPayloadMode(t *testing.T) {
	// payload policy 是调用方必须显式声明的业务事实：未声明时 fail-fast，
	// 不允许实现替调用方猜测一个"安全默认"然后继续扫描。
	result, err := RunPlatformPayloadPrivacyScan(context.Background(), PlatformPrivacyScanConfig{
		Identity: platformPrivacyScanIdentity(),
		Canary:   platformPrivacyCanaryFixture(),
	})
	if err == nil {
		t.Fatalf("RunPlatformPayloadPrivacyScan() error = nil, want fail-fast error for missing payload mode")
	}
	if !strings.Contains(err.Error(), "payload mode") {
		t.Fatalf("error = %q, want it to name the missing payload mode", err.Error())
	}
	if result.Clean {
		t.Fatalf("Clean = true alongside error, want non-clean result on failure")
	}
}

func TestPlatformSmokePrivacyScanBuildsBaggageThroughAllowlistOnly(t *testing.T) {
	// baggage 面必须由生产 BaggageFieldsFromCorrelationIdentity 构造：只含
	// allowlist 身份键。canary 注入受控验证后 baggage 面保持干净，证明实现
	// 没有绕过 allowlist 自行拼装 baggage。
	result, err := RunPlatformPayloadPrivacyScan(context.Background(), PlatformPrivacyScanConfig{
		PayloadMode: obs.PayloadModeContentRedacted,
		Environment: "local",
		Identity:    platformPrivacyScanIdentity(),
		Canary:      platformPrivacyCanaryFixture(),
	})
	if err != nil {
		t.Fatalf("RunPlatformPayloadPrivacyScan() error = %v, want nil for allowlist-built baggage", err)
	}
	if !result.Clean {
		t.Fatalf("Clean = false, want baggage surface free of canary values (findings: %#v)", result.Findings)
	}
	if !containsString(result.ScannedSurfaces, "baggage") {
		t.Fatalf("ScannedSurfaces = %v, want it to include the baggage surface", result.ScannedSurfaces)
	}

	// 锚点断言：canary JWT 必须真的会被生产 baggage 校验拒绝。这一行保护
	// 的是 canary 值本身的有效性——如果哨兵值不再命中生产模式，上面的
	// "clean"结论就会退化成假阴性。
	if err := obs.ValidateBaggageFieldSafety(obs.BaggageSessionID, platformPrivacyCanaryFixture().JWT); err == nil {
		t.Fatalf("ValidateBaggageFieldSafety() accepted the canary JWT; the canary must remain detectable by the production scanner")
	}
}

func TestPlatformSmokePrivacyScanIncludesDebugSurfaceAndNeverSkipsIt(t *testing.T) {
	// debug 视图是最容易被"顺手"绕过 scanner 的出口：正式 JSON 干净而
	// %#v/调试渲染带原文，是生产环境真实的泄露路径。扫描面必须同时覆盖
	// 正式序列化与 debug 渲染，且不允许以 debug 名义豁免任何注入面。
	result, err := RunPlatformPayloadPrivacyScan(context.Background(), PlatformPrivacyScanConfig{
		PayloadMode: obs.PayloadModeContentRedacted,
		Environment: "local",
		Identity:    platformPrivacyScanIdentity(),
		Canary:      platformPrivacyCanaryFixture(),
	})
	if err != nil {
		t.Fatalf("RunPlatformPayloadPrivacyScan() error = %v, want nil for clean payload", err)
	}
	for _, requiredSurface := range []string{"payload_json", "payload_debug", "baggage"} {
		if !containsString(result.ScannedSurfaces, requiredSurface) {
			t.Fatalf("ScannedSurfaces = %v, want it to include %q", result.ScannedSurfaces, requiredSurface)
		}
	}

	// 负向抽验：一个以 debug 为名的污染面必须同样命中并立即失败。
	canary := platformPrivacyCanaryFixture()
	result, err = RunPlatformPayloadPrivacyScan(context.Background(), PlatformPrivacyScanConfig{
		PayloadMode: obs.PayloadModeContentRedacted,
		Environment: "local",
		Identity:    platformPrivacyScanIdentity(),
		Canary:      canary,
		ExtraSurfaces: []PlatformPrivacySurface{
			{Name: "payload_debug", Payload: "debug view leaked: " + canary.JWT},
		},
	})
	if err == nil {
		t.Fatalf("RunPlatformPayloadPrivacyScan() error = nil, want immediate failure when the debug surface carries the canary")
	}
	if result.Clean {
		t.Fatalf("Clean = true alongside findings, want non-clean result")
	}
	if !hasPlatformPrivacyFindingOnSurface(result.Findings, "payload_debug") {
		t.Fatalf("Findings = %#v, want a hit on the payload_debug surface", result.Findings)
	}
}

func TestPlatformSmokePrivacyScanFailsFastOnSyntheticHitsWithoutEchoingValues(t *testing.T) {
	// 拦截力证明：注入一个携带 canary 的污染面，扫描必须立即失败。输出
	// （error 与 result 渲染）只允许出现 surface/类别/计数，任何 canary
	// 原文出现在错误消息里都是"检测报告再次泄露"。
	canary := platformPrivacyCanaryFixture()
	result, err := RunPlatformPayloadPrivacyScan(context.Background(), PlatformPrivacyScanConfig{
		PayloadMode: obs.PayloadModeContentRedacted,
		Environment: "local",
		Identity:    platformPrivacyScanIdentity(),
		Canary:      canary,
		ExtraSurfaces: []PlatformPrivacySurface{
			{Name: "injected_surface", Payload: "api_key=" + canary.APIKey + " mobile=" + canary.MobilePII},
		},
	})
	if err == nil {
		t.Fatalf("RunPlatformPayloadPrivacyScan() error = nil, want immediate failure on synthetic sensitive hits")
	}
	if result.Clean {
		t.Fatalf("Clean = true alongside injected hits, want non-clean result")
	}
	if !hasPlatformPrivacyFindingOnSurface(result.Findings, "injected_surface") {
		t.Fatalf("Findings = %#v, want a hit on the injected_surface", result.Findings)
	}
	for _, finding := range result.Findings {
		if finding.Surface == "" {
			t.Fatalf("finding has empty surface: %#v", finding)
		}
		if strings.TrimSpace(finding.Category) == "" {
			t.Fatalf("finding on %q has empty category; findings must classify hits", finding.Surface)
		}
		if finding.Count < 1 {
			t.Fatalf("finding on %q has count %d, want at least 1", finding.Surface, finding.Count)
		}
	}

	// 错误消息与结果渲染都不得携带任何 canary 原文。
	assertPlatformPrivacyOutputHasNoCanaryValues(t, err.Error(), platformPrivacyCanaryValues(canary))
	assertPlatformPrivacyOutputHasNoCanaryValues(t, fmt.Sprintf("%#v", result), platformPrivacyCanaryValues(canary))
}

func assertPlatformPrivacyOutputHasNoCanaryValues(t *testing.T, rendered string, canaryValues []string) {
	t.Helper()

	for _, value := range canaryValues {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if strings.Contains(rendered, value) {
			// 只报告值的位置与长度特征，不把原文再次带进失败输出。
			t.Fatalf("privacy scan output echoed a canary value of length %d; outputs must carry categories and counts only", len(value))
		}
	}
}

func hasPlatformPrivacyFindingOnSurface(findings []PlatformPrivacyFinding, surface string) bool {
	for _, finding := range findings {
		if finding.Surface == surface {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
