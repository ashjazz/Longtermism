package smoke

import (
	"context"
	"strings"
	"testing"
)

func TestObservabilityPrivacySmokeRedactsSensitiveInputsFromAllOutputs(t *testing.T) {
	// 故意注入敏感原文；smoke 的目标不是证明输入无敏感内容，而是证明这些内容
	// 不会出现在 logger、span sink、OTel mapper、baggage 或 smoke payload 等普通观测输出面。
	result, err := RunObservabilityPrivacySmoke(context.Background(), ObservabilityPrivacySmokeConfig{
		RequestID:      "req-observability-privacy",
		ServiceTraceID: "svc-trace-observability-privacy",
		SpanID:         "span-observability-privacy",
		AITraceID:      "ai-trace-observability-privacy",
		EvalRunID:      "eval-run-observability-privacy",
		SampleID:       "sample-observability-privacy",
		RawQuery:       privacySmokeRawQuery,
		PromptContent:  privacySmokePrompt,
		ToolArguments:  privacySmokeToolArgs,
		APIKey:         privacySmokeAPIKey,
		JWT:            privacySmokeJWT,
		Password:       privacySmokePassword,
		ExternalResult: privacySmokeExternalResponse,
	})
	if err != nil {
		t.Fatalf("RunObservabilityPrivacySmoke() error = %v", err)
	}

	if result.LeakCount != 0 {
		t.Fatalf("LeakCount = %d, want 0; leaks = %#v", result.LeakCount, result.Leaks)
	}
	if len(result.ScannedSurfaces) < 5 {
		t.Fatalf("ScannedSurfaces length = %d, want logger/span/mapper/baggage/smoke surfaces: %#v", len(result.ScannedSurfaces), result.ScannedSurfaces)
	}
	assertObservabilityPrivacySurfaces(t, result.ScannedSurfaces, []string{
		"logger",
		"span_sink",
		"otel_mapper",
		"baggage",
		"smoke_payload",
	})
	assertObservabilityPrivacyResultDoesNotEchoSensitiveValues(t, result)
}

func TestObservabilityPrivacySmokeReportsLeaksWithoutEchoingRawValues(t *testing.T) {
	result := ObservabilityPrivacySmokeResult{
		LeakCount:       1,
		ScannedSurfaces: []string{"logger"},
		Leaks: []ObservabilityPrivacyLeak{
			{
				Surface: "logger",
				Field:   "query_hash",
				Reason:  "sensitive_value",
			},
		},
	}

	assertObservabilityPrivacyResultDoesNotEchoSensitiveValues(t, result)
}

func assertObservabilityPrivacySurfaces(t *testing.T, got []string, want []string) {
	t.Helper()

	seen := make(map[string]bool, len(got))
	for _, surface := range got {
		seen[surface] = true
	}
	for _, surface := range want {
		if !seen[surface] {
			t.Fatalf("ScannedSurfaces missing %q in %#v", surface, got)
		}
	}
}

func assertObservabilityPrivacyResultDoesNotEchoSensitiveValues(t *testing.T, result ObservabilityPrivacySmokeResult) {
	t.Helper()

	rendered := strings.Join([]string{
		strings.Join(result.ScannedSurfaces, " "),
		renderObservabilityPrivacyLeaksForTest(result.Leaks),
	}, " ")
	for _, forbidden := range []string{
		privacySmokeRawQuery,
		privacySmokePrompt,
		privacySmokeToolArgs,
		privacySmokeAPIKey,
		privacySmokeJWT,
		privacySmokePassword,
		privacySmokeExternalResponse,
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("privacy smoke result echoed sensitive value %q: %#v", forbidden, result)
		}
	}
}

func renderObservabilityPrivacyLeaksForTest(leaks []ObservabilityPrivacyLeak) string {
	var builder strings.Builder
	for _, leak := range leaks {
		builder.WriteString(leak.Surface)
		builder.WriteString(" ")
		builder.WriteString(leak.Field)
		builder.WriteString(" ")
		builder.WriteString(leak.Reason)
		builder.WriteString(" ")
	}
	return builder.String()
}

const (
	privacySmokeRawQuery         = "用户问题: 查询身份证 110101199001011234 的账户余额"
	privacySmokePrompt           = "完整 prompt: system=内部风控规则不可外泄"
	privacySmokeToolArgs         = `{"account_id":"acct-sensitive","password":"p@ssw0rd!"}`
	privacySmokeAPIKey           = "sk-observability-privacy-api-key"
	privacySmokeJWT              = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature"
	privacySmokePassword         = "p@ssw0rd!"
	privacySmokeExternalResponse = "external_response: upstream returned private payload"
)
