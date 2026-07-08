package obs

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Logger 是 P0 阶段的本地日志型 Tracer。
//
// 它输出一行一个 JSON 对象（JSON Lines），便于后续被文件、stdout、日志平台或
// 测试 buffer 统一消费。这里不绑定 glog、OTEL 或 LangFuse，是为了让核心 obs
// 契约先稳定下来，真实观测后端后续只需要实现同一个 Tracer 接口。
type Logger struct {
	mu     sync.Mutex
	writer io.Writer
}

// NewLogger 创建日志型 trace 记录器。
//
// writer 允许注入 bytes.Buffer、文件或 stdout；nil writer 会退化为 io.Discard。
// 这个降级符合“观测失败不得影响主流程”的质量门控：AI 请求的业务结果不应因为
// 本地日志目标缺失而失败，真正的生产告警会在后续观测 adapter 中处理。
func NewLogger(writer io.Writer) *Logger {
	if writer == nil {
		writer = io.Discard
	}
	return &Logger{writer: writer}
}

// Record 写入一条普通 trace 日志。
//
// 注意这里没有读取 ctx 中的任意值。context 经常会被中间件塞入请求对象、用户输入、
// tool 参数或认证信息；普通 trace 只能记录 hash、长度、状态和成本字段，原文留存
// 必须走独立的加密审计链路。
func (l *Logger) Record(_ context.Context, trace Trace) {
	if l == nil {
		return
	}

	entry := newLogEntry(trace)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	// 本地日志 sink 失败不能反向影响 AI 请求主流程；后续真实观测 adapter 可另行告警。
	_, _ = l.writer.Write(data)
}

// logEntry 是日志输出的稳定 schema。
//
// Trace 结构体为了 Go 代码阅读保留 PascalCase 字段和 camelCase JSON tag；日志平台
// 侧则统一使用 snake_case。显式 DTO 比直接 json.Marshal(Trace) 更啰嗦，但能避免
// 不小心把后续新增的 raw prompt/tool args 一并写入普通日志。
type logEntry struct {
	TraceID   string `json:"trace_id"`
	TenantID  string `json:"tenant_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Feature   string `json:"feature"`
	Timestamp string `json:"timestamp"`

	RequestID       string          `json:"request_id,omitempty"`
	ServiceTraceID  string          `json:"service_trace_id,omitempty"`
	SpanID          string          `json:"span_id,omitempty"`
	ObservationType ObservationType `json:"observation_type,omitempty"`
	FailureStatus   string          `json:"failure_status,omitempty"`

	QueryHash string `json:"query_hash,omitempty"`
	QueryLang string `json:"query_lang,omitempty"`
	QueryLen  int    `json:"query_len,omitempty"`

	Model                 string  `json:"model,omitempty"`
	PromptTemplateVersion string  `json:"prompt_template_version,omitempty"`
	PromptHash            string  `json:"prompt_hash,omitempty"`
	InputTokens           int     `json:"input_tokens"`
	OutputTokens          int     `json:"output_tokens"`
	ReasoningTokens       int     `json:"reasoning_tokens"`
	CacheReadTokens       int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens      int     `json:"cache_write_tokens,omitempty"`
	Temperature           float64 `json:"temperature,omitempty"`
	TTFTMs                int64   `json:"ttft_ms,omitempty"`
	TotalLatencyMs        int64   `json:"total_latency_ms,omitempty"`

	ChunksRetrieved    int       `json:"chunks_retrieved,omitempty"`
	QueryRewrittenHash string    `json:"query_rewritten_hash,omitempty"`
	TopScores          []float64 `json:"top_scores,omitempty"`
	RetrievalLatencyMs int64     `json:"retrieval_latency_ms,omitempty"`

	QuerySummary     SafeSummary `json:"query_summary,omitempty"`
	PromptSummary    SafeSummary `json:"prompt_summary,omitempty"`
	RetrievalSummary SafeSummary `json:"retrieval_summary,omitempty"`
	ToolSummary      SafeSummary `json:"tool_summary,omitempty"`

	AgentStepIndex    int    `json:"agent_step_index,omitempty"`
	ToolCallID        string `json:"tool_call_id,omitempty"`
	ToolName          string `json:"tool_name,omitempty"`
	TerminationReason string `json:"termination_reason,omitempty"`
	LoopDetected      bool   `json:"loop_detected,omitempty"`
	BudgetExceeded    bool   `json:"budget_exceeded,omitempty"`

	ProviderName   string `json:"provider_name,omitempty"`
	RequestedModel string `json:"requested_model,omitempty"`
	CircuitState   string `json:"circuit_state,omitempty"`
	Degraded       bool   `json:"degraded,omitempty"`
	RateLimited    bool   `json:"rate_limited,omitempty"`

	CostUSD       float64  `json:"cost_usd,omitempty"`
	OutcomeStatus string   `json:"outcome_status"`
	UserRating    *int     `json:"user_rating,omitempty"`
	AutoEvalScore *float64 `json:"auto_eval_score,omitempty"`
}

func newLogEntry(trace Trace) logEntry {
	entry := logEntry{
		TraceID:   trace.TraceID,
		TenantID:  trace.TenantID,
		UserID:    trace.UserID,
		SessionID: trace.SessionID,
		Feature:   trace.Feature,
		Timestamp: formatTraceTimestamp(trace.Timestamp),

		RequestID:       trace.RequestID,
		ServiceTraceID:  trace.ServiceTraceID,
		SpanID:          trace.SpanID,
		ObservationType: trace.ObservationType,
		FailureStatus:   trace.FailureStatus,

		QueryHash: trace.QueryHash,
		QueryLang: trace.QueryLang,
		QueryLen:  trace.QueryLen,

		Model:                 trace.Model,
		PromptTemplateVersion: trace.PromptTemplateVer,
		PromptHash:            trace.PromptHash,
		InputTokens:           trace.InputTokens,
		OutputTokens:          trace.OutputTokens,
		ReasoningTokens:       trace.ReasoningTokens,
		CacheReadTokens:       trace.CacheReadTokens,
		CacheWriteTokens:      trace.CacheWriteTokens,
		Temperature:           trace.Temperature,
		TTFTMs:                trace.TTFTMs,
		TotalLatencyMs:        trace.TotalLatencyMs,

		ChunksRetrieved:    trace.ChunksRetrieved,
		QueryRewrittenHash: trace.QueryRewrittenHash,
		TopScores:          cloneFloat64s(trace.TopScores),
		RetrievalLatencyMs: trace.RetrievalLatencyMs,

		QuerySummary:     cloneSafeSummary(trace.QuerySummary),
		PromptSummary:    cloneSafeSummary(trace.PromptSummary),
		RetrievalSummary: cloneSafeSummary(trace.RetrievalSummary),
		ToolSummary:      cloneSafeSummary(trace.ToolSummary),

		AgentStepIndex:    trace.AgentStepIndex,
		ToolCallID:        trace.ToolCallID,
		ToolName:          trace.ToolName,
		TerminationReason: trace.TerminationReason,
		LoopDetected:      trace.LoopDetected,
		BudgetExceeded:    trace.BudgetExceeded,

		ProviderName:   trace.ProviderName,
		RequestedModel: trace.RequestedModel,
		CircuitState:   trace.CircuitState,
		Degraded:       trace.Degraded,
		RateLimited:    trace.RateLimited,

		CostUSD:       trace.CostUSD,
		OutcomeStatus: trace.OutcomeStatus,
		UserRating:    cloneIntPointer(trace.UserRating),
		AutoEvalScore: cloneFloat64Pointer(trace.AutoEvalScore),
	}
	return sanitizeLogEntry(entry)
}

func sanitizeLogEntry(entry logEntry) logEntry {
	entry.TraceID = safeLogString("trace_id", entry.TraceID)
	entry.TenantID = safeLogString("tenant_id", entry.TenantID)
	entry.UserID = safeLogString("user_id", entry.UserID)
	entry.SessionID = safeLogString("session_id", entry.SessionID)
	entry.Feature = safeLogString("feature", entry.Feature)
	entry.Timestamp = safeLogString("timestamp", entry.Timestamp)
	entry.RequestID = safeLogString("request_id", entry.RequestID)
	entry.ServiceTraceID = safeLogString("service_trace_id", entry.ServiceTraceID)
	entry.SpanID = safeLogString("span_id", entry.SpanID)
	entry.FailureStatus = safeLogString("failure_status", entry.FailureStatus)
	entry.QueryHash = safeLogString("query_hash", entry.QueryHash)
	entry.QueryLang = safeLogString("query_lang", entry.QueryLang)
	entry.Model = safeLogString("model", entry.Model)
	entry.PromptTemplateVersion = safeLogString("prompt_template_version", entry.PromptTemplateVersion)
	entry.PromptHash = safeLogString("prompt_hash", entry.PromptHash)
	entry.QueryRewrittenHash = safeLogString("query_rewritten_hash", entry.QueryRewrittenHash)
	entry.ToolCallID = safeLogString("tool_call_id", entry.ToolCallID)
	entry.ToolName = safeLogString("tool_name", entry.ToolName)
	entry.TerminationReason = safeLogString("termination_reason", entry.TerminationReason)
	entry.ProviderName = safeLogString("provider_name", entry.ProviderName)
	entry.RequestedModel = safeLogString("requested_model", entry.RequestedModel)
	entry.CircuitState = safeLogString("circuit_state", entry.CircuitState)
	entry.OutcomeStatus = safeLogString("outcome_status", entry.OutcomeStatus)
	return entry
}

func safeLogString(key, value string) string {
	if value == "" {
		return ""
	}
	if len(ScanForbiddenPayloadFields(map[string]string{key: value})) > 0 {
		return ""
	}
	return value
}

func formatTraceTimestamp(timestamp time.Time) string {
	if timestamp.IsZero() {
		return time.Time{}.UTC().Format(time.RFC3339Nano)
	}
	return timestamp.UTC().Format(time.RFC3339Nano)
}

func cloneFloat64s(values []float64) []float64 {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]float64, len(values))
	copy(cloned, values)
	return cloned
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
