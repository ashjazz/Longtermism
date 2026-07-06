package smoke

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	obstestutil "github.com/jazzash/ashjazz-aiagent/pkg/ai/obs/testutil"
)

const (
	infrastructureHTTPSpanName = "http.server.request"
	infrastructureOutcomeOK    = "success"
	infrastructureOutcomeError = "error"
)

// InfrastructureSpanSmokeConfig 描述一次离线 HTTP/service span smoke。
type InfrastructureSpanSmokeConfig struct {
	Sink           *obstestutil.MemorySpanSink
	Method         string
	Path           string
	StatusCode     int
	Duration       time.Duration
	RequestID      string
	ServiceTraceID string
	SpanID         string
}

// InfrastructureSpanRecord 是基础设施平面的低敏 span 事实模型。
//
// 它只包含服务入口诊断所需的标准字段，不包含 header、body、query string 或用户
// 原文。后续真实 OTel adapter 可以把它映射为 span attributes。
type InfrastructureSpanRecord struct {
	Name           string
	Method         string
	Path           string
	StatusCode     int
	Duration       time.Duration
	RequestID      string
	ServiceTraceID string
	SpanID         string
	Outcome        string
}

// InfrastructureSpanSmokeResult 是基础设施 span smoke 的最小回执。
type InfrastructureSpanSmokeResult struct {
	RequestID      string
	ServiceTraceID string
	SpanID         string
}

// RunInfrastructureSpanSmoke 记录一条默认离线的 HTTP/service span。
func RunInfrastructureSpanSmoke(ctx context.Context, config InfrastructureSpanSmokeConfig) (InfrastructureSpanSmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	record, err := newInfrastructureSpanRecord(config)
	if err != nil {
		return InfrastructureSpanSmokeResult{}, err
	}

	if config.Sink != nil {
		config.Sink.Record(spanSnapshotFromInfrastructureRecord(record))
	}

	return InfrastructureSpanSmokeResult{
		RequestID:      record.RequestID,
		ServiceTraceID: record.ServiceTraceID,
		SpanID:         record.SpanID,
	}, nil
}

func newInfrastructureSpanRecord(config InfrastructureSpanSmokeConfig) (InfrastructureSpanRecord, error) {
	if strings.TrimSpace(config.RequestID) == "" {
		return InfrastructureSpanRecord{}, fmt.Errorf("infrastructure span request_id is required")
	}
	if strings.TrimSpace(config.ServiceTraceID) == "" {
		return InfrastructureSpanRecord{}, fmt.Errorf("infrastructure span service_trace_id is required")
	}
	if strings.TrimSpace(config.SpanID) == "" {
		return InfrastructureSpanRecord{}, fmt.Errorf("infrastructure span span_id is required")
	}

	method := strings.TrimSpace(config.Method)
	if method == "" {
		method = "GET"
	}
	path := strings.TrimSpace(config.Path)
	if path == "" {
		path = "/"
	}

	statusCode := config.StatusCode
	if statusCode == 0 {
		statusCode = 200
	}

	return InfrastructureSpanRecord{
		Name:           infrastructureHTTPSpanName,
		Method:         method,
		Path:           path,
		StatusCode:     statusCode,
		Duration:       config.Duration,
		RequestID:      strings.TrimSpace(config.RequestID),
		ServiceTraceID: strings.TrimSpace(config.ServiceTraceID),
		SpanID:         strings.TrimSpace(config.SpanID),
		Outcome:        outcomeFromHTTPStatus(statusCode),
	}, nil
}

func spanSnapshotFromInfrastructureRecord(record InfrastructureSpanRecord) obstestutil.SpanSnapshot {
	return obstestutil.SpanSnapshot{
		Name:           record.Name,
		RequestID:      record.RequestID,
		ServiceTraceID: record.ServiceTraceID,
		SpanID:         record.SpanID,
		Attributes: map[string]string{
			"http.request.method":       record.Method,
			"url.path":                  record.Path,
			"http.response.status_code": strconv.Itoa(record.StatusCode),
			"duration_ms":               strconv.FormatInt(record.Duration.Milliseconds(), 10),
			"outcome":                   record.Outcome,
		},
	}
}

func outcomeFromHTTPStatus(statusCode int) string {
	if statusCode >= 500 {
		return infrastructureOutcomeError
	}
	return infrastructureOutcomeOK
}
