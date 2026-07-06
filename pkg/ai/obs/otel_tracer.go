package obs

import "context"

// TraceSpanSink 是 OTel-style tracer 的最小输出端口。
//
// 真实实现可以把 TraceSpanSnapshot 转为 OTel span；默认测试可以使用内存 sink。
// 这个接口刻意放在 obs 核心包内，并且只暴露本项目稳定快照，避免把 OTel SDK
// 类型泄漏到 obs.Tracer 或业务调用方。
type TraceSpanSink interface {
	RecordTraceSpan(ctx context.Context, snapshot TraceSpanSnapshot)
}

// OTelTracer 是基于 TraceSpanSnapshot 的 OTel-style Tracer adapter 壳层。
type OTelTracer struct {
	sink TraceSpanSink
}

// NewOTelTracer 创建 OTel-style tracer adapter。
func NewOTelTracer(sink TraceSpanSink) *OTelTracer {
	return &OTelTracer{sink: sink}
}

// Record 将 obs.Trace 映射为 span snapshot 并交给 sink。
//
// Tracer.Record 没有 error 返回值；mapper 或 sink 失败不应把业务请求带崩。真实
// exporter 失败的可诊断状态由外层 RecordWithExportFailureProtection 或生命周期
// 管理器记录。
func (t *OTelTracer) Record(ctx context.Context, trace Trace) {
	if t == nil || t.sink == nil {
		return
	}

	snapshot, err := MapTraceToSpanSnapshot(trace)
	if err != nil {
		return
	}

	defer func() {
		_ = recover()
	}()
	t.sink.RecordTraceSpan(ctx, snapshot)
}
