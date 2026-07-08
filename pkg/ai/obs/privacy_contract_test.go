package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCrossAdapterPrivacyContractRejectsRawPayload(t *testing.T) {
	trace := sensitivePrivacyContractTrace()

	tests := []struct {
		name      string
		rawOutput func(t *testing.T) string
	}{
		{
			name: "logger",
			rawOutput: func(t *testing.T) string {
				t.Helper()

				var output bytes.Buffer
				NewLogger(&output).Record(sensitivePrivacyContractContext(), trace)
				return output.String()
			},
		},
		{
			name: "otel tracer sink",
			rawOutput: func(t *testing.T) string {
				t.Helper()

				sink := newOTelContractSink()
				NewOTelTracer(sink).Record(sensitivePrivacyContractContext(), trace)
				return sink.RawPayload(t)
			},
		},
		{
			name: "otel mapper span snapshot",
			rawOutput: func(t *testing.T) string {
				t.Helper()

				snapshot, err := MapTraceToSpanSnapshot(trace)
				if err != nil {
					t.Fatalf("MapTraceToSpanSnapshot() error = %v", err)
				}
				payload, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatalf("marshal span snapshot: %v", err)
				}
				return string(payload)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.rawOutput(t)
			if strings.TrimSpace(raw) == "" {
				t.Fatalf("%s raw output is empty, want payload to scan", tt.name)
			}

			assertPrivacyContractPayload(t, raw)
		})
	}
}

type privacyContractContextKey string

func sensitivePrivacyContractContext() context.Context {
	ctx := context.WithValue(context.Background(), privacyContractContextKey("raw_query"), privacyContractRawQuery)
	ctx = context.WithValue(ctx, privacyContractContextKey("prompt_content"), privacyContractPrompt)
	ctx = context.WithValue(ctx, privacyContractContextKey("tool_args"), privacyContractToolArgs)
	return ctx
}

func sensitivePrivacyContractTrace() Trace {
	trace := NewTrace(
		"trace-privacy-contract",
		"privacy_contract",
		time.Date(2026, time.July, 7, 10, 0, 0, 0, time.UTC),
		WithCorrelationIdentity(NewCorrelationIdentity(
			"req-privacy-contract",
			WithServiceSpan("svc-trace-privacy-contract", "span-privacy-contract"),
			WithAITraceID("trace-privacy-contract"),
			WithSessionID("session-privacy-contract"),
		)),
		WithObservationType(ObservationTypeAgent),
		WithTenant("tenant-privacy", privacyContractJWT, "session-privacy-contract"),
		WithQuery(privacyContractRawQuery, "zh-CN", len([]rune(privacyContractRawQuery))),
		WithModel(privacyContractAPIKey),
		WithPrompt("prompt-v1", privacyContractPrompt),
		WithUsage(17, 9, 0),
		WithSafeSummaries(
			NewSafeSummary(WithSummaryHash("query-hash-safe"), WithSummaryLength(len([]rune(privacyContractRawQuery)))),
			NewSafeSummary(WithSummaryHash("prompt-hash-safe"), WithSummaryLength(len([]rune(privacyContractPrompt)))),
			NewSafeSummary(WithSummaryCount(1), WithSummaryStatus("success")),
			NewSafeSummary(WithSummaryCategory("tool.search"), WithSummaryStatus("success")),
		),
		WithOutcome("success"),
	)
	trace.AgentStepIndex = 1
	trace.ToolCallID = privacyContractToolArgs
	trace.ToolName = privacyContractExternalResponse
	trace.TerminationReason = "finished"
	trace.ProviderName = "provider"
	trace.RequestedModel = privacyContractExternalResponse
	trace.CircuitState = "closed"
	return trace
}

func assertPrivacyContractPayload(t *testing.T, raw string) {
	t.Helper()

	for _, forbidden := range []string{
		privacyContractRawQuery,
		privacyContractPrompt,
		privacyContractToolArgs,
		privacyContractAPIKey,
		privacyContractJWT,
		privacyContractPassword,
		privacyContractExternalResponse,
		"raw_query",
		"prompt_content",
		"tool_args",
		"api_key",
		"authorization",
		"external_response",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("privacy contract payload leaked forbidden marker %q: %s", forbidden, raw)
		}
	}

	for _, required := range []string{
		"trace-privacy-contract",
		"req-privacy-contract",
		"svc-trace-privacy-contract",
		"span-privacy-contract",
		"privacy_contract",
		"query-hash-safe",
		"prompt-hash-safe",
	} {
		if !strings.Contains(raw, required) {
			t.Fatalf("privacy contract payload missing safe diagnostic marker %q: %s", required, raw)
		}
	}
}

const (
	privacyContractRawQuery         = "用户原文: 身份证 110101199001011234 查询余额"
	privacyContractPrompt           = "完整 prompt: system=内部风控规则不可外泄"
	privacyContractToolArgs         = `{"account_id":"acct-sensitive","password":"p@ssw0rd!"}`
	privacyContractAPIKey           = "sk-privacy-contract-api-key"
	privacyContractJWT              = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature"
	privacyContractPassword         = "p@ssw0rd!"
	privacyContractExternalResponse = "external_response: upstream returned private payload"
)
