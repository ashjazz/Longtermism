package obs

import "time"

// FailureStatus 是 AI 生产故障的稳定状态词表。
//
// 这些值会进入 ordinary trace、eval 报告、journal 和后续观测平台维度。
// 因此它们不能由调用方随手拼字符串，否则同一类故障可能被写成 timeout、
// timed_out、deadline_exceeded 等多个维度，线上聚合和回归分析都会失真。
type FailureStatus string

const (
	// FailureTimeout 表示 provider、工具、检索或整体请求触达超时边界。
	FailureTimeout FailureStatus = "timeout"

	// FailureRateLimit 表示全局、用户、租户或 provider 维度被限流。
	FailureRateLimit FailureStatus = "rate_limit"

	// FailureRetrievalMiss 表示 RAG 检索路径没有拿到可用上下文。
	FailureRetrievalMiss FailureStatus = "retrieval_miss"

	// FailureLoopDetected 表示 Agent executor 检测到重复动作或不安全循环。
	FailureLoopDetected FailureStatus = "loop_detected"

	// FailureBudgetExceeded 表示 token、成本或步骤预算被耗尽。
	FailureBudgetExceeded FailureStatus = "budget_exceeded"

	// FailureTelemetryExportFailed 表示观测上报失败，但主业务流程不应受影响。
	FailureTelemetryExportFailed FailureStatus = "telemetry_export_failed"
)

// NewFailureTrace 创建带失败状态的 trace。
//
// 它只是 NewTrace 的窄化入口：先写入稳定失败状态，再应用调用方补充的模型、
// prompt、检索、延迟、租户等诊断字段。这样失败 trace 和成功 trace 使用同一个
// Trace 结构，日志、eval 和未来 LangFuse/OTEL adapter 都不需要维护两套 schema。
func NewFailureTrace(traceID, feature string, timestamp time.Time, status FailureStatus, options ...TraceOption) Trace {
	allOptions := make([]TraceOption, 0, len(options)+1)
	allOptions = append(allOptions, WithFailureStatus(status))
	allOptions = append(allOptions, options...)

	return NewTrace(traceID, feature, timestamp, allOptions...)
}

// WithFailureStatus 把稳定失败状态写入 Trace.OutcomeStatus。
//
// 继续复用 OutcomeStatus 是为了保持历史 logger/eval 兼容。Trace.FailureStatus
// 后续用于观测链路自身失败诊断，不能自动覆盖业务 outcome。
func WithFailureStatus(status FailureStatus) TraceOption {
	return WithOutcome(string(status))
}
