package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type sensitiveContextKey string

func TestLoggerTracerDoesNotExposeSensitiveContent(t *testing.T) {
	const (
		rawQuery     = "请查询身份证号 110101199001011234 的账户余额"
		rawPrompt    = "系统指令：不得向用户展示内部风控规则和密钥片段"
		rawToolArgs  = `{"account_id":"acct-private-001","access_token":"token-private-001"}`
		queryHash    = "query-hash-safe-for-logs"
		promptHash   = "prompt-hash-safe-for-logs"
		traceID      = "trace-privacy-boundary"
		outcomeState = "success"
	)

	// context 可能被上层中间件用于传递请求信息，但日志型 Tracer 不能遍历或序列化
	// context 中的任意值。敏感原文若需要留存，必须进入独立的加密审计存储。
	ctx := context.WithValue(context.Background(), sensitiveContextKey("query"), rawQuery)
	ctx = context.WithValue(ctx, sensitiveContextKey("prompt"), rawPrompt)
	ctx = context.WithValue(ctx, sensitiveContextKey("tool_arguments"), rawToolArgs)

	var output bytes.Buffer
	tracer := NewLogger(&output)
	tracer.Record(ctx, Trace{
		TraceID:           traceID,
		Feature:           "p0_smoke",
		Timestamp:         time.Date(2026, time.June, 25, 9, 0, 0, 0, time.UTC),
		QueryHash:         queryHash,
		QueryLang:         "zh-CN",
		QueryLen:          len([]rune(rawQuery)),
		Model:             "fake-model",
		PromptTemplateVer: "v1",
		PromptHash:        promptHash,
		InputTokens:       24,
		OutputTokens:      12,
		OutcomeStatus:     outcomeState,
	})

	logLine := strings.TrimSpace(output.String())
	if logLine == "" {
		t.Fatal("Record() wrote no log entry")
	}

	for label, sensitiveValue := range map[string]string{
		"raw query":          rawQuery,
		"complete prompt":    rawPrompt,
		"raw tool arguments": rawToolArgs,
		"access token":       "token-private-001",
	} {
		if strings.Contains(logLine, sensitiveValue) {
			t.Errorf("Record() leaked %s in ordinary trace output", label)
		}
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(logLine), &payload); err != nil {
		t.Fatalf("Record() output is not valid JSON: %v; output = %q", err, logLine)
	}

	// 不仅要检查本次原文未泄露，还要禁止普通日志 schema 出现可承载原文的字段。
	// 否则未来调用方可能在不改变 tracer 实现的情况下开始写入敏感数据。
	for _, forbiddenKey := range []string{
		"query",
		"raw_query",
		"prompt",
		"prompt_content",
		"tool_arguments",
		"tool_args",
	} {
		if _, exists := payload[forbiddenKey]; exists {
			t.Errorf("ordinary trace contains forbidden sensitive field %q", forbiddenKey)
		}
	}

	for key, want := range map[string]any{
		"trace_id":       traceID,
		"query_hash":     queryHash,
		"query_len":      float64(len([]rune(rawQuery))),
		"prompt_hash":    promptHash,
		"outcome_status": outcomeState,
	} {
		if got := payload[key]; got != want {
			t.Errorf("safe diagnostic field %q = %#v, want %#v", key, got, want)
		}
	}
}
