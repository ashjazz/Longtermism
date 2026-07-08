package smoke

import (
	"context"
	"fmt"
	"time"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

// InfrastructureBusinessAction 是 smoke 中被保护的主业务路径。
type InfrastructureBusinessAction func(ctx context.Context) (string, error)

// InfrastructureSpanExporter 是基础设施平面 exporter 的最小接口。
type InfrastructureSpanExporter interface {
	ExportInfrastructureSpan(ctx context.Context, record InfrastructureSpanRecord) error
}

// InfrastructureExportFailureSmokeConfig 描述基础设施 exporter 失败验证。
type InfrastructureExportFailureSmokeConfig struct {
	RequestID      string
	ServiceTraceID string
	SpanID         string
	BusinessAction InfrastructureBusinessAction
	Exporter       InfrastructureSpanExporter
}

// InfrastructureExportFailureSmokeResult 是 exporter 失败保护的诊断结果。
type InfrastructureExportFailureSmokeResult struct {
	BusinessResult string
	RequestID      string
	ServiceTraceID string
	SpanID         string
	FailureStatus  string
	FailureMessage string
}

// RunInfrastructureExportFailureSmoke 验证基础设施 exporter 失败不会覆盖业务结果。
//
// 这个 smoke 的边界刻意收窄在“观测上报失败不能改写主业务语义”：业务成功时，
// exporter 错误只进入诊断字段；业务失败时，业务错误仍作为主错误返回。真实 HTTP
// middleware 中的 span 收尾应使用 defer/finally 语义，即使业务失败也尽量导出失败
// span；那属于完整请求生命周期观测，不是本 smoke 要覆盖的重点。
func RunInfrastructureExportFailureSmoke(ctx context.Context, config InfrastructureExportFailureSmokeConfig) (InfrastructureExportFailureSmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.BusinessAction == nil {
		return InfrastructureExportFailureSmokeResult{}, fmt.Errorf("infrastructure export failure business action is required")
	}

	businessResult, err := config.BusinessAction(ctx)
	if err != nil {
		return InfrastructureExportFailureSmokeResult{}, fmt.Errorf("run infrastructure smoke business action: %w", err)
	}

	result := InfrastructureExportFailureSmokeResult{
		BusinessResult: businessResult,
		RequestID:      config.RequestID,
		ServiceTraceID: config.ServiceTraceID,
		SpanID:         config.SpanID,
	}

	if err := exportInfrastructureSpan(ctx, config); err != nil {
		result.FailureStatus = string(obs.FailureTelemetryExportFailed)
		result.FailureMessage = err.Error()
	}

	return result, nil
}

func exportInfrastructureSpan(ctx context.Context, config InfrastructureExportFailureSmokeConfig) (err error) {
	if config.Exporter == nil {
		return nil
	}

	record, err := newInfrastructureSpanRecord(InfrastructureSpanSmokeConfig{
		Method:         "POST",
		Path:           "/observability/smoke",
		StatusCode:     200,
		Duration:       time.Millisecond,
		RequestID:      config.RequestID,
		ServiceTraceID: config.ServiceTraceID,
		SpanID:         config.SpanID,
	})
	if err != nil {
		return err
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()

	return config.Exporter.ExportInfrastructureSpan(ctx, record)
}
