package obs

import (
	"testing"
	"time"
)

func TestFailureTraceStatusCoversProductionFailureModes(t *testing.T) {
	timestamp := time.Date(2026, time.June, 29, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name       string
		status     FailureStatus
		wantStatus string
	}{
		{
			name:       "provider timeout",
			status:     FailureTimeout,
			wantStatus: "timeout",
		},
		{
			name:       "provider or tenant rate limit",
			status:     FailureRateLimit,
			wantStatus: "rate_limit",
		},
		{
			name:       "rag retrieval miss",
			status:     FailureRetrievalMiss,
			wantStatus: "retrieval_miss",
		},
		{
			name:       "agent loop detected",
			status:     FailureLoopDetected,
			wantStatus: "loop_detected",
		},
		{
			name:       "token or cost budget exceeded",
			status:     FailureBudgetExceeded,
			wantStatus: "budget_exceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 失败 trace 的状态值会进入日志、eval 报告和 journal 复盘。
			// 这里用表驱动测试把 P2 阶段最重要的故障词表先固定下来，避免不同模块
			// 分别写成 rate_limit/rate_limited/too_many_requests 这类不可聚合的字符串。
			trace := NewFailureTrace(
				"trace-"+tt.wantStatus,
				"failure_diagnostics",
				timestamp,
				tt.status,
				WithModel("gpt-diagnostic"),
				WithPrompt("failure-v1", "prompt-hash"),
			)

			if trace.TraceID != "trace-"+tt.wantStatus {
				t.Fatalf("TraceID = %q, want trace-%s", trace.TraceID, tt.wantStatus)
			}
			if trace.Feature != "failure_diagnostics" {
				t.Fatalf("Feature = %q, want failure_diagnostics", trace.Feature)
			}
			if trace.Timestamp != timestamp {
				t.Fatalf("Timestamp = %v, want %v", trace.Timestamp, timestamp)
			}
			if trace.OutcomeStatus != tt.wantStatus {
				t.Fatalf("OutcomeStatus = %q, want %q", trace.OutcomeStatus, tt.wantStatus)
			}
			if trace.Model != "gpt-diagnostic" {
				t.Fatalf("Model = %q, want gpt-diagnostic", trace.Model)
			}
			if trace.PromptTemplateVer != "failure-v1" || trace.PromptHash != "prompt-hash" {
				t.Fatalf("prompt identity = (%q, %q), want (failure-v1, prompt-hash)", trace.PromptTemplateVer, trace.PromptHash)
			}
		})
	}
}

func TestFailureTraceHelpersKeepExistingTraceFields(t *testing.T) {
	base := NewTrace(
		"trace-retrieval-miss",
		"rag_qa",
		time.Date(2026, time.June, 29, 11, 0, 0, 0, time.UTC),
		WithTenant("tenant-001", "user-001", "session-001"),
		WithQuery("safe-query-hash", "zh-CN", 42),
		WithRetrieval(0, "rewritten-query-hash", nil, 18),
		WithLatency(0, 120),
	)

	trace := ApplyTraceOptions(base, WithFailureStatus(FailureRetrievalMiss))

	if trace.OutcomeStatus != "retrieval_miss" {
		t.Fatalf("OutcomeStatus = %q, want retrieval_miss", trace.OutcomeStatus)
	}
	if trace.TenantID != "tenant-001" || trace.UserID != "user-001" || trace.SessionID != "session-001" {
		t.Fatalf("tenant context = (%q, %q, %q), want preserved", trace.TenantID, trace.UserID, trace.SessionID)
	}
	if trace.QueryHash != "safe-query-hash" || trace.QueryLen != 42 {
		t.Fatalf("query diagnostics = (%q, %d), want safe-query-hash and 42", trace.QueryHash, trace.QueryLen)
	}
	if trace.ChunksRetrieved != 0 || trace.RetrievalLatencyMs != 18 {
		t.Fatalf("retrieval diagnostics = (%d, %d), want 0 chunks and 18ms", trace.ChunksRetrieved, trace.RetrievalLatencyMs)
	}
	if trace.TotalLatencyMs != 120 {
		t.Fatalf("TotalLatencyMs = %d, want 120", trace.TotalLatencyMs)
	}
}

func TestFailureTraceHelpersDoNotMutateBaseTrace(t *testing.T) {
	base := NewTrace(
		"trace-immutable-failure",
		"agent_run",
		time.Date(2026, time.June, 29, 11, 30, 0, 0, time.UTC),
		WithOutcome("success"),
	)

	trace := ApplyTraceOptions(base, WithFailureStatus(FailureLoopDetected))

	if base.OutcomeStatus != "success" {
		t.Fatalf("base OutcomeStatus = %q, want unchanged success", base.OutcomeStatus)
	}
	if trace.OutcomeStatus != "loop_detected" {
		t.Fatalf("derived OutcomeStatus = %q, want loop_detected", trace.OutcomeStatus)
	}
}
