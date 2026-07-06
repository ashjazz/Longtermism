package obs

import (
	"fmt"
	"strconv"
	"strings"
)

// TraceSpanSnapshot 是 AI trace 映射到 OTel-style span 后的稳定核心快照。
//
// 这里仍然不暴露 OTel SDK 类型：核心包只负责把 obs.Trace 归一化为低敏 span
// 事实，真实 exporter 或测试 sink 可以再把快照转换为自己的平台对象。
type TraceSpanSnapshot struct {
	Name            string
	RequestID       string
	ServiceTraceID  string
	SpanID          string
	ParentSpanID    string
	AITraceID       string
	ObservationType ObservationType
	Attributes      map[string]string
	Summaries       map[string]SafeSummary
}

// MapTraceToSpanSnapshot 把 AI 语义 trace 映射成低敏 span 快照。
func MapTraceToSpanSnapshot(trace Trace) (TraceSpanSnapshot, error) {
	if err := ValidateObservationType(trace.ObservationType); err != nil {
		return TraceSpanSnapshot{}, err
	}

	snapshot := TraceSpanSnapshot{
		Name:            spanNameForObservationType(trace.ObservationType),
		RequestID:       trace.RequestID,
		ServiceTraceID:  trace.ServiceTraceID,
		SpanID:          spanIDForTrace(trace),
		ParentSpanID:    trace.SpanID,
		AITraceID:       trace.TraceID,
		ObservationType: trace.ObservationType,
		Attributes:      attributesForTrace(trace),
		Summaries:       summariesForTrace(trace),
	}
	return cloneTraceSpanSnapshot(snapshot), nil
}

func spanNameForObservationType(observationType ObservationType) string {
	return "ai." + observationType.String()
}

func spanIDForTrace(trace Trace) string {
	return fmt.Sprintf("span-%s-%s", trace.ObservationType, trace.TraceID)
}

func attributesForTrace(trace Trace) map[string]string {
	attributes := map[string]string{
		"ai.feature": trace.Feature,
		"ai.outcome": trace.OutcomeStatus,
	}
	putStringAttribute(attributes, "ai.trace_id", trace.TraceID)
	putStringAttribute(attributes, "ai.tenant_id", trace.TenantID)
	putStringAttribute(attributes, "ai.user_id", trace.UserID)
	putStringAttribute(attributes, "ai.session_id", trace.SessionID)
	putStringAttribute(attributes, "ai.timestamp", formatTraceTimestamp(trace.Timestamp))
	putStringAttribute(attributes, "ai.failure_status", trace.FailureStatus)
	putStringAttribute(attributes, "ai.query.hash", trace.QueryHash)
	putStringAttribute(attributes, "ai.query.lang", trace.QueryLang)
	putIntAttribute(attributes, "ai.query.length", trace.QueryLen)
	putStringAttribute(attributes, "ai.model", trace.Model)
	putStringAttribute(attributes, "ai.prompt.template_version", trace.PromptTemplateVer)
	putStringAttribute(attributes, "ai.prompt.hash", trace.PromptHash)
	putIntAttribute(attributes, "ai.usage.input_tokens", trace.InputTokens)
	putIntAttribute(attributes, "ai.usage.output_tokens", trace.OutputTokens)
	putIntAttribute(attributes, "ai.usage.reasoning_tokens", trace.ReasoningTokens)
	putIntAttribute(attributes, "ai.usage.cache_read_tokens", trace.CacheReadTokens)
	putIntAttribute(attributes, "ai.usage.cache_write_tokens", trace.CacheWriteTokens)
	putFloatAttribute(attributes, "ai.temperature", trace.Temperature)
	putInt64Attribute(attributes, "ai.latency.ttft_ms", trace.TTFTMs)
	putInt64Attribute(attributes, "ai.latency.total_ms", trace.TotalLatencyMs)
	putIntAttributeAllowZero(attributes, "ai.retrieval.chunks", trace.ChunksRetrieved, trace.ObservationType == ObservationTypeRetriever)
	putStringAttribute(attributes, "ai.retrieval.query_rewrite_hash", trace.QueryRewrittenHash)
	putFloatSliceAttribute(attributes, "ai.retrieval.top_scores", trace.TopScores)
	putInt64Attribute(attributes, "ai.latency.retrieval_ms", trace.RetrievalLatencyMs)
	putFloatAttribute(attributes, "ai.cost_usd", trace.CostUSD)
	putIntPointerAttribute(attributes, "ai.feedback.user_rating", trace.UserRating)
	putFloatPointerAttribute(attributes, "ai.feedback.auto_eval_score", trace.AutoEvalScore)
	return attributes
}

func putStringAttribute(attributes map[string]string, key, value string) {
	if value == "" {
		return
	}
	attributes[key] = value
}

func putIntAttribute(attributes map[string]string, key string, value int) {
	if value == 0 {
		return
	}
	attributes[key] = strconv.Itoa(value)
}

func putIntAttributeAllowZero(attributes map[string]string, key string, value int, include bool) {
	if !include {
		putIntAttribute(attributes, key, value)
		return
	}
	attributes[key] = strconv.Itoa(value)
}

func putInt64Attribute(attributes map[string]string, key string, value int64) {
	if value == 0 {
		return
	}
	attributes[key] = strconv.FormatInt(value, 10)
}

func putFloatAttribute(attributes map[string]string, key string, value float64) {
	if value == 0 {
		return
	}
	attributes[key] = strconv.FormatFloat(value, 'f', -1, 64)
}

func putFloatSliceAttribute(attributes map[string]string, key string, values []float64) {
	if len(values) == 0 {
		return
	}

	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.FormatFloat(value, 'f', -1, 64)
	}
	attributes[key] = strings.Join(parts, ",")
}

func putIntPointerAttribute(attributes map[string]string, key string, value *int) {
	if value == nil {
		return
	}
	attributes[key] = strconv.Itoa(*value)
}

func putFloatPointerAttribute(attributes map[string]string, key string, value *float64) {
	if value == nil {
		return
	}
	attributes[key] = strconv.FormatFloat(*value, 'f', -1, 64)
}

func summariesForTrace(trace Trace) map[string]SafeSummary {
	summaries := make(map[string]SafeSummary)
	putSafeSummary(summaries, "query", trace.QuerySummary)
	putSafeSummary(summaries, "prompt", trace.PromptSummary)
	putSafeSummary(summaries, "retrieval", trace.RetrievalSummary)
	putSafeSummary(summaries, "tool", trace.ToolSummary)
	return summaries
}

func putSafeSummary(summaries map[string]SafeSummary, key string, summary SafeSummary) {
	if isEmptySafeSummary(summary) {
		return
	}
	summaries[key] = cloneSafeSummary(summary)
}

func isEmptySafeSummary(summary SafeSummary) bool {
	return summary.Hash == "" &&
		summary.Length == 0 &&
		summary.Category == "" &&
		summary.Count == 0 &&
		summary.Score == nil &&
		summary.Status == "" &&
		summary.ErrorClass == ""
}

func cloneTraceSpanSnapshot(snapshot TraceSpanSnapshot) TraceSpanSnapshot {
	cloned := snapshot
	cloned.Attributes = cloneTraceSpanAttributeMap(snapshot.Attributes)
	cloned.Summaries = cloneTraceSpanSummaryMap(snapshot.Summaries)
	return cloned
}

func cloneTraceSpanAttributeMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneTraceSpanSummaryMap(values map[string]SafeSummary) map[string]SafeSummary {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]SafeSummary, len(values))
	for key, value := range values {
		cloned[key] = cloneSafeSummary(value)
	}
	return cloned
}
