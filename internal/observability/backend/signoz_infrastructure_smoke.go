package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// SigNoz 备选 profile 的 infra smoke adapter（T147）。与 Grafana 版
// （grafana_infrastructure_smoke.go）同构：把平台文档解码限制在本文件，runner 只看到
// 平台无关的 marker observation 与稳定错误分类。
//
// 查询语言说明（学习点）：SigNoz 的 traces/logs 查询使用自带 filter 语法而非
// TraceQL/LogQL。OTel attribute key（longtermism.smoke.run_id）在 SigNoz 存储层的
// 展平规则以真实 obs-signoz-e2e 为准校准；过滤谓词必须包含 marker 精确匹配，
// 解码端只投影 target.Marker（与 Tempo 模式一致：服务端谓词成功后才允许投影）。

// signozMarkerFilter builds the server-side marker predicate. The marker is validated by
// isSafeSmokeQueryTarget before reaching this function, so the filter cannot reflect
// arbitrary strings into a DTO.
func signozMarkerFilter(marker string) string {
	return fmt.Sprintf(`longtermism.smoke.run_id = %q`, marker)
}

// SignozInfrastructureSmokeBackendConfig connects only bounded evidence ports: the
// already-protected SigNoz client plus the two shared negative-query ports. It carries no
// endpoint or credential strings, so reports cannot expose deployment configuration.
type SignozInfrastructureSmokeBackendConfig struct {
	Signoz   *SignozQueryClient
	Langfuse smokeNegativeCounter
	AIPlane  smokeNegativeCounter
}

type SignozInfrastructureSmokeBackend struct {
	signoz   *SignozQueryClient
	langfuse smokeNegativeCounter
	aiPlane  smokeNegativeCounter
}

func NewSignozInfrastructureSmokeBackend(config SignozInfrastructureSmokeBackendConfig) (*SignozInfrastructureSmokeBackend, error) {
	if config.Signoz == nil || !config.Signoz.smokeProtected {
		return nil, newBackendQueryError("signoz", "backend_unavailable")
	}
	if config.Langfuse == nil || config.AIPlane == nil {
		return nil, newBackendQueryError("negative_query", "authentication_failed")
	}
	return &SignozInfrastructureSmokeBackend{signoz: config.Signoz, langfuse: config.Langfuse, aiPlane: config.AIPlane}, nil
}

func (b *SignozInfrastructureSmokeBackend) QuerySignozTraces(ctx context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	if !isSafeSmokeQueryTarget(target) || b == nil || b.signoz == nil {
		return nil, newBackendQueryError("signoz_traces", "invalid_query")
	}
	result, err := b.signoz.QueryTracesSince(ctx, signozMarkerFilter(target.Marker), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, safeChatBackendError("signoz_traces", err)
	}
	observations, err := decodeSignozMarkerObservations(result, target)
	if err != nil {
		return nil, newBackendQueryError("signoz_traces", "malformed_response")
	}
	return observations, nil
}

func (b *SignozInfrastructureSmokeBackend) QuerySignozLogs(ctx context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	if !isSafeSmokeQueryTarget(target) || b == nil || b.signoz == nil {
		return nil, newBackendQueryError("signoz_logs", "invalid_query")
	}
	result, err := b.signoz.QueryLogsSince(ctx, signozMarkerFilter(target.Marker), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, safeChatBackendError("signoz_logs", err)
	}
	observations, err := decodeSignozMarkerObservations(result, target)
	if err != nil {
		return nil, newBackendQueryError("signoz_logs", "malformed_response")
	}
	return observations, nil
}

// HTTPRequestCount：SigNoz 的 instant query 与 Prometheus vector 格式兼容，
// 因此三信号 profile 复用主线的固定聚合 selector 与计数解码语义。
func (b *SignozInfrastructureSmokeBackend) BaselineHTTPRequestCount(ctx context.Context) (int64, error) {
	return b.httpRequestCount(ctx)
}

func (b *SignozInfrastructureSmokeBackend) HTTPRequestCount(ctx context.Context) (int64, error) {
	return b.httpRequestCount(ctx)
}

func (b *SignozInfrastructureSmokeBackend) httpRequestCount(ctx context.Context) (int64, error) {
	if b == nil || b.signoz == nil {
		return 0, newBackendQueryError("signoz_metrics", "invalid_query")
	}
	result, err := b.signoz.QueryMetrics(ctx, infraHTTPCountQuery)
	if err != nil {
		return 0, safeChatBackendError("signoz_metrics", err)
	}
	evidence, err := decodePrometheusHTTPCount(result, defaultInfraHTTPCountSelector)
	if err != nil {
		return 0, newBackendQueryError("signoz_metrics", smokeReportErrorClass(err))
	}
	return evidence.Count, nil
}

func (b *SignozInfrastructureSmokeBackend) QueryLangfuse(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	return b.langfuse.Query(ctx, target)
}

func (b *SignozInfrastructureSmokeBackend) QueryAIPlane(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	return b.aiPlane.Query(ctx, target)
}

var _ smoke.SignozInfrastructureSmokeBackend = (*SignozInfrastructureSmokeBackend)(nil)

func (b *SignozInfrastructureSmokeBackend) String() string {
	return "SignozInfrastructureSmokeBackend"
}

// signozRecordsDocument is the bounded, version-tolerant view of a SigNoz list response.
// Field names differ across SigNoz releases (data vs results, timestamp units), so decoding
// walks the known shapes and fails closed on anything unrecognized.
type signozRecordsDocument struct {
	Data    []json.RawMessage `json:"data"`
	Results []json.RawMessage `json:"results"`
}

type signozRecordDocument struct {
	TimestampUnixNano json.Number `json:"timestampUnixNano"`
	Timestamp         json.Number `json:"timestamp"`
	// attributes may be flat or nested depending on the SigNoz storage projection;
	// marker equality is already guaranteed server-side, so decoding only needs the time.
}

func decodeSignozMarkerObservations(result BackendQueryResult, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	var document signozRecordsDocument
	if err := result.Decode(&document); err != nil {
		return nil, errMalformedSmokeEvidence
	}
	records := document.Data
	if len(records) == 0 {
		records = document.Results
	}
	observations := make([]smoke.MarkerObservation, 0, len(records))
	for _, record := range records {
		var item signozRecordDocument
		if err := json.Unmarshal(record, &item); err != nil {
			return nil, errMalformedSmokeEvidence
		}
		observedAt, ok := signozRecordTimestamp(item)
		if !ok || !isInsideSmokeWindow(observedAt, target) {
			continue
		}
		// The server-side filter already pinned the marker; projecting target.Marker here
		// mirrors the Tempo adapter rule: projection is allowed only after the exact
		// predicate query succeeded.
		observations = append(observations, smoke.MarkerObservation{Marker: target.Marker, ObservedAt: observedAt})
	}
	return observations, nil
}

// signozRecordTimestamp accepts second/millisecond/nanosecond epochs and rejects zero
// values: a missing timestamp cannot be placed inside the run window.
func signozRecordTimestamp(item signozRecordDocument) (time.Time, bool) {
	for _, candidate := range []json.Number{item.TimestampUnixNano, item.Timestamp} {
		text := candidate.String()
		if text == "" {
			continue
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value <= 0 {
			continue
		}
		switch {
		case value > 1e18:
			return time.Unix(0, value), true
		case value > 1e15:
			return time.UnixMilli(value / 1e6), true
		case value > 1e12:
			return time.UnixMicro(value / 1e3), true
		default:
			return time.Unix(value, 0), true
		}
	}
	return time.Time{}, false
}
