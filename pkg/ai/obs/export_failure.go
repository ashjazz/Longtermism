package obs

import "context"

// RecordWithExportFailureProtection 在观测上报失败时保护业务主流程。
//
// Tracer.Record 当前没有 error 返回值，不应把 exporter 故障传播成业务错误。
// 真实 exporter 若发生 panic，本 helper 会恢复并返回稳定失败状态，调用方可把该状态
// 写入本地诊断记录或 smoke 报告，但用户请求本身不应因此失败。
func RecordWithExportFailureProtection(ctx context.Context, tracer Tracer, trace Trace) (status FailureStatus) {
	if tracer == nil {
		return ""
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			status = FailureTelemetryExportFailed
		}
	}()

	tracer.Record(ctx, trace)
	return ""
}
