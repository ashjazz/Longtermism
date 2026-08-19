package obs

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	aiPlaneMarkerKey   = "longtermism.observability.plane"
	aiPlaneMarkerValue = "ai"
	aiTraceIDKey       = "longtermism.ai.trace_id"
	// aiDesignatedKey 与 Collector tail_sampling 的 designated-ai 保留策略
	// 对齐：策略要求 AI span 同时携带 plane=ai 与 designated=true 才免于
	// 概率采样。AI 角色的 span 本身就是受保护证据，缺少该标记会让真实
	// AI 流量在窗口尾部被 not_sampled 丢弃（collector-grafana.yaml）。
	aiDesignatedKey  = "longtermism.ai.designated"
	aiDesignatedFlag = "true"
)

// SpanRoutingRole 是调用方显式声明的 span 路由职责。
//
// 角色不能从 span name、HTTP route 或 feature 猜测，否则普通 HTTP/DB 子 span
// 可能被误送入 AI 平面，污染成本、质量和评估数据。
type SpanRoutingRole string

const (
	SpanRoutingRoleAIChatRoot    SpanRoutingRole = "ai_chat_root"
	SpanRoutingRoleAIChatBridge  SpanRoutingRole = "ai_chat_bridge"
	SpanRoutingRoleAIGeneration  SpanRoutingRole = "ai_generation"
	SpanRoutingRoleAIRetriever   SpanRoutingRole = "ai_retriever"
	SpanRoutingRoleAITool        SpanRoutingRole = "ai_tool"
	SpanRoutingRoleAIAgent       SpanRoutingRole = "ai_agent"
	SpanRoutingRoleAIEvaluator   SpanRoutingRole = "ai_evaluator"
	SpanRoutingRoleHTTPChild     SpanRoutingRole = "http_child"
	SpanRoutingRoleDatabaseChild SpanRoutingRole = "database_child"
	SpanRoutingRoleRedisChild    SpanRoutingRole = "redis_child"
)

// SpanRoutingInput 汇集路由 span 所需的显式领域事实。
type SpanRoutingInput struct {
	Role     SpanRoutingRole
	Identity CorrelationIdentity
	Feature  string
}

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
	if err := validateTraceSpanIdentitySafety(trace); err != nil {
		return TraceSpanSnapshot{}, err
	}

	routingAttributes, err := MapSpanRoutingAttributes(SpanRoutingInput{
		Role: roleForObservationType(trace.ObservationType),
		Identity: CorrelationIdentity{
			RequestID: trace.RequestID,
			AITraceID: trace.TraceID,
		},
		Feature: trace.Feature,
	})
	if err != nil {
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
		Attributes:      mergeTraceSpanAttributes(attributesForTrace(trace), routingAttributes),
		Summaries:       summariesForTrace(trace),
	}
	return cloneTraceSpanSnapshot(snapshot), nil
}

func validateTraceSpanIdentitySafety(trace Trace) error {
	// Identity 同时存在于快照顶层和 attributes。只扫描 attributes 会让 exporter
	// 仍可从顶层序列化 JWT/API key，因此必须在构造任何部分快照前统一 fail closed。
	findings := ScanForbiddenPayloadFields(map[string]string{
		"request.id":              trace.RequestID,
		"service.trace_id":        trace.ServiceTraceID,
		"service.parent_span_id":  trace.SpanID,
		"longtermism.ai.trace_id": trace.TraceID,
	})
	if len(findings) > 0 {
		return fmt.Errorf("trace span identity contains a sensitive value")
	}
	return nil
}

// MapSpanRoutingAttributes 只为显式 AI 职责生成 AI 平面路由属性。
//
// AI trace ID 与 feature 是关联和解释 AI span 的必需事实。缺失或命中隐私规则时
// 整体拒绝，避免输出有 marker 却无法可靠关联的半成品 span。
func MapSpanRoutingAttributes(input SpanRoutingInput) (map[string]string, error) {
	switch {
	case isInfrastructureSpanRoutingRole(input.Role):
		return nil, nil
	case !isAISpanRoutingRole(input.Role):
		return nil, fmt.Errorf("unsupported span routing role")
	}

	if !isSafeRequiredRoutingFact(aiTraceIDKey, input.Identity.AITraceID) {
		return nil, fmt.Errorf("AI span routing requires a safe AI trace identity")
	}
	if !isSafeRequiredRoutingFact("ai.feature", input.Feature) {
		return nil, fmt.Errorf("AI span routing requires a safe feature")
	}

	attributes := make(map[string]string, 5)
	putStringAttribute(attributes, aiPlaneMarkerKey, aiPlaneMarkerValue)
	putStringAttribute(attributes, aiDesignatedKey, aiDesignatedFlag)
	putStringAttribute(attributes, aiTraceIDKey, input.Identity.AITraceID)
	putStringAttribute(attributes, "ai.feature", input.Feature)
	putStringAttribute(attributes, "request.id", input.Identity.RequestID)
	return attributes, nil
}

func roleForObservationType(observationType ObservationType) SpanRoutingRole {
	switch observationType {
	case ObservationTypeGeneration:
		return SpanRoutingRoleAIGeneration
	case ObservationTypeRetriever:
		return SpanRoutingRoleAIRetriever
	case ObservationTypeTool:
		return SpanRoutingRoleAITool
	case ObservationTypeAgent:
		return SpanRoutingRoleAIAgent
	case ObservationTypeEvaluator:
		return SpanRoutingRoleAIEvaluator
	default:
		return ""
	}
}

func isAISpanRoutingRole(role SpanRoutingRole) bool {
	switch role {
	case SpanRoutingRoleAIChatRoot,
		SpanRoutingRoleAIChatBridge,
		SpanRoutingRoleAIGeneration,
		SpanRoutingRoleAIRetriever,
		SpanRoutingRoleAITool,
		SpanRoutingRoleAIAgent,
		SpanRoutingRoleAIEvaluator:
		return true
	default:
		return false
	}
}

func isInfrastructureSpanRoutingRole(role SpanRoutingRole) bool {
	switch role {
	case SpanRoutingRoleHTTPChild, SpanRoutingRoleDatabaseChild, SpanRoutingRoleRedisChild:
		return true
	default:
		return false
	}
}

func isSafeRequiredRoutingFact(key, value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	return len(ScanForbiddenPayloadFields(map[string]string{key: value})) == 0
}

func spanNameForObservationType(observationType ObservationType) string {
	return "ai." + observationType.String()
}

func spanIDForTrace(trace Trace) string {
	return fmt.Sprintf("span-%s-%s", trace.ObservationType, trace.TraceID)
}

func attributesForTrace(trace Trace) map[string]string {
	attributes := make(map[string]string)
	putStringAttribute(attributes, "ai.feature", trace.Feature)
	putStringAttribute(attributes, "ai.outcome", trace.OutcomeStatus)
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
	putIntAttribute(attributes, "ai.agent.step_index", trace.AgentStepIndex)
	putStringAttribute(attributes, "ai.agent.tool_call_id", trace.ToolCallID)
	putStringAttribute(attributes, "ai.agent.tool_name", trace.ToolName)
	putStringAttribute(attributes, "ai.agent.termination_reason", trace.TerminationReason)
	putBoolAttribute(attributes, "ai.agent.loop_detected", trace.LoopDetected)
	putBoolAttribute(attributes, "ai.agent.budget_exceeded", trace.BudgetExceeded)
	putStringAttribute(attributes, "ai.provider.name", trace.ProviderName)
	putStringAttribute(attributes, "ai.provider.requested_model", trace.RequestedModel)
	putStringAttribute(attributes, "ai.provider.circuit_state", trace.CircuitState)
	putBoolAttribute(attributes, "ai.provider.degraded", trace.Degraded)
	putBoolAttribute(attributes, "ai.provider.rate_limited", trace.RateLimited)
	putFloatAttribute(attributes, "ai.cost_usd", trace.CostUSD)
	putIntPointerAttribute(attributes, "ai.feedback.user_rating", trace.UserRating)
	putFloatPointerAttribute(attributes, "ai.feedback.auto_eval_score", trace.AutoEvalScore)
	if trace.ObservationType == ObservationTypeGeneration {
		putGenerationAttributes(attributes, trace)
	}
	return attributes
}

func putGenerationAttributes(attributes map[string]string, trace Trace) {
	// 标准 GenAI key 只是已有领域事实的别名；不得从 feature、span name 或 outcome
	// 推断 operation、finish reason 等当前 Trace 尚未建模的语义。
	putStringAttribute(attributes, "gen_ai.provider.name", trace.ProviderName)
	putStringAttribute(attributes, "gen_ai.request.model", trace.RequestedModel)
	putStringAttribute(attributes, "gen_ai.response.model", trace.Model)
	putIntAttribute(attributes, "gen_ai.usage.input_tokens", trace.InputTokens)
	putIntAttribute(attributes, "gen_ai.usage.output_tokens", trace.OutputTokens)
	putIntAttribute(attributes, "gen_ai.usage.reasoning.output_tokens", trace.ReasoningTokens)
	putFloatAttribute(attributes, "gen_ai.request.temperature", trace.Temperature)
}

func mergeTraceSpanAttributes(base, additional map[string]string) map[string]string {
	if len(base) == 0 && len(additional) == 0 {
		return nil
	}

	merged := make(map[string]string, len(base)+len(additional))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range additional {
		merged[key] = value
	}
	return merged
}

func putStringAttribute(attributes map[string]string, key, value string) {
	if value == "" {
		return
	}
	// OTel span 的字符串属性是最容易误带 query/prompt/tool args/API key 的出口。
	// 数值和布尔属性只承载计数、延迟、分数和状态位；若未来新增字符串属性，必须
	// 继续走这里的扫描，而不是直接写入 attributes。
	if len(ScanForbiddenPayloadFields(map[string]string{key: value})) > 0 {
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

func putBoolAttribute(attributes map[string]string, key string, value bool) {
	if !value {
		return
	}
	attributes[key] = strconv.FormatBool(value)
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
