package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

var (
	errGrafanaSmokeAdapterUnavailable = errors.New("grafana smoke evidence adapter is unavailable")
	errInvalidSmokeQueryTarget        = errors.New("invalid smoke query target")
	safeSmokeMarkerPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,127}$`)
)

// 单次 marker 查询窗口的上限。基础 smoke 场景（infra/chat/score）的 deadline
// 是 60s；persistent-queue 的 drain 窗口是 120s（smoke.PersistentQueueDrainWindow）。
// 上限取两者的最大值并留少量轮询余量，保持"拒绝大范围扫描"的原始边界意图。
const maximumSmokeQueryWindow = 150 * time.Second
const maximumSmokeMarkerObservations = 100

// GrafanaSmokeEvidenceAdapter is the only boundary allowed to decode T063 query documents for
// an infrastructure smoke. It deliberately projects just marker timestamps, counters and health
// booleans; raw traces, log lines and backend response documents cannot escape this package.
type GrafanaSmokeEvidenceAdapter struct {
	client *GrafanaQueryClient
}

// SmokeHTTPCountSelector identifies the one low-cardinality HTTP metric series that an infra
// smoke is allowed to compare. Identity labels such as request_id and trace_id are rejected from
// candidate series instead of being copied into smoke evidence.
type SmokeHTTPCountSelector struct {
	Route       string
	Method      string
	StatusClass string
}

// SmokeCountEvidence is safe to place in a report because it contains only an aggregate count.
type SmokeCountEvidence struct {
	Count int64 `json:"count"`
}

// SmokeDatasourceHealthEvidence records a datasource health decision without its diagnostic body.
type SmokeDatasourceHealthEvidence struct {
	Healthy bool `json:"healthy"`
}

func NewGrafanaSmokeEvidenceAdapter(client *GrafanaQueryClient) *GrafanaSmokeEvidenceAdapter {
	return &GrafanaSmokeEvidenceAdapter{client: client}
}

// QueryTempoMarker makes the marker predicate part of the query fact before interpreting the
// search summary. Tempo search summaries intentionally omit arbitrary resource attributes, so
// the adapter may project target.Marker only after its exact TraceQL query succeeds.
func (a *GrafanaSmokeEvidenceAdapter) QueryTempoMarker(ctx context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	if !isSafeSmokeQueryTarget(target) {
		return nil, errInvalidSmokeQueryTarget
	}
	if a.client == nil {
		return nil, errGrafanaSmokeAdapterUnavailable
	}
	result, err := a.client.QueryTempoSince(ctx, traceQLMarkerQuery(target.Marker), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, err
	}
	return decodeTempoMarkerObservations(result, target)
}

// QueryLokiMarker uses the same target window as Tempo. The structured-metadata predicate is
// intentionally exact and marker-only; raw log text is discarded after validating its type.
func (a *GrafanaSmokeEvidenceAdapter) QueryLokiMarker(ctx context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	if !isSafeSmokeQueryTarget(target) {
		return nil, errInvalidSmokeQueryTarget
	}
	if a.client == nil {
		return nil, errGrafanaSmokeAdapterUnavailable
	}
	result, err := a.client.QueryLokiSince(ctx, lokiMarkerQuery(target.Marker), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, err
	}
	return decodeLokiMarkerObservations(result, target)
}

func (a *GrafanaSmokeEvidenceAdapter) DecodePrometheusHTTPCount(result BackendQueryResult, selector SmokeHTTPCountSelector) (SmokeCountEvidence, error) {
	return decodePrometheusHTTPCount(result, selector)
}

// decodePrometheusHTTPCount 是纯解码逻辑：Grafana 与 SigNoz profile 的 instant query
// 都返回 Prometheus vector 格式，两条主线共享同一份计数语义。
func decodePrometheusHTTPCount(result BackendQueryResult, selector SmokeHTTPCountSelector) (SmokeCountEvidence, error) {
	var response prometheusVectorResponse
	if err := result.Decode(&response); err != nil || response.Status != "success" || response.Data.ResultType != "vector" {
		return SmokeCountEvidence{}, errMalformedSmokeEvidence
	}
	// 空成功向量是 counter 基线语义：该固定聚合 selector 没有任何匹配 series，等价于
	// “0 次已记录请求”。它是全新应用首个 smoke 的合法基线（trigger 前没有历史流量），
	// 与畸形文档严格区分；check 仍必须取得真实正向 delta 才通过。
	if len(response.Data.Result) == 0 {
		return SmokeCountEvidence{Count: 0}, nil
	}
	// infraHTTPCountQuery deliberately uses sum(...) after selecting the fixed route/method/status
	// series. Prometheus therefore returns exactly one aggregate sample with an empty label map;
	// it is safer than accepting extra labels and must not be decoded as an unaggregated series.
	if len(response.Data.Result) == 1 && len(response.Data.Result[0].Metric) == 0 {
		count, err := parsePrometheusSampleValue(response.Data.Result[0].Value)
		if err != nil {
			return SmokeCountEvidence{}, errMalformedSmokeEvidence
		}
		return SmokeCountEvidence{Count: count}, nil
	}

	var count *int64
	for _, sample := range response.Data.Result {
		if !matchesLowCardinalitySelector(sample.Metric, selector) {
			continue
		}
		value, err := parsePrometheusSampleValue(sample.Value)
		if err != nil || count != nil {
			return SmokeCountEvidence{}, errMalformedSmokeEvidence
		}
		count = &value
	}
	if count == nil {
		return SmokeCountEvidence{}, errMalformedSmokeEvidence
	}
	return SmokeCountEvidence{Count: *count}, nil
}

func (a *GrafanaSmokeEvidenceAdapter) DecodeGrafanaDatasourceHealth(result BackendQueryResult) (SmokeDatasourceHealthEvidence, error) {
	var response struct {
		Status string `json:"status"`
	}
	if err := result.Decode(&response); err != nil || response.Status == "" {
		return SmokeDatasourceHealthEvidence{}, errMalformedSmokeEvidence
	}
	return SmokeDatasourceHealthEvidence{Healthy: response.Status == "OK"}, nil
}

func (a *GrafanaSmokeEvidenceAdapter) DecodeNegativeCount(result BackendQueryResult) (SmokeCountEvidence, error) {
	var response struct {
		Data struct {
			Count json.Number `json:"count"`
		} `json:"data"`
	}
	if err := result.Decode(&response); err != nil || response.Data.Count == "" {
		return SmokeCountEvidence{}, errMalformedSmokeEvidence
	}
	count, err := strconv.ParseInt(response.Data.Count.String(), 10, 64)
	if err != nil || count < 0 {
		return SmokeCountEvidence{}, errMalformedSmokeEvidence
	}
	return SmokeCountEvidence{Count: count}, nil
}

// smokeReportErrorClass maps transport/query errors into the finite report vocabulary. Original
// backend messages never cross this boundary, preventing response bodies or credentials from
// becoming report data through an error path.
func smokeReportErrorClass(err error) string {
	if errors.Is(err, ErrStaleQueryWindow) {
		return "invalid_query"
	}
	if errors.Is(err, errMalformedSmokeEvidence) {
		return "malformed_response"
	}
	var queryError *BackendQueryError
	if errors.As(err, &queryError) && isReportErrorClass(queryError.Class()) {
		return queryError.Class()
	}
	return "query_failed"
}

var errMalformedSmokeEvidence = errors.New("malformed smoke backend evidence")

type tempoSearchResponse struct {
	Traces []struct {
		StartTimeUnixNano string `json:"startTimeUnixNano"`
	} `json:"traces"`
}

func decodeTempoMarkerObservations(result BackendQueryResult, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	var response tempoSearchResponse
	if err := result.Decode(&response); err != nil || response.Traces == nil || len(response.Traces) > maximumSmokeMarkerObservations {
		return nil, errMalformedSmokeEvidence
	}
	return markerObservationsFromNanoseconds(response.Traces, target)
}

type lokiRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			// Loki 3.x 的 query_range 把 native OTLP 的 log attributes 以 structured
			// metadata 形式放进 stream label map；旧契约假设的逐条三元素 metadata
			// 在真实 3.7.2 响应中不存在，labels 才是 marker 的权威来源。
			Stream map[string]string   `json:"stream"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func decodeLokiMarkerObservations(result BackendQueryResult, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	var response lokiRangeResponse
	if err := result.Decode(&response); err != nil || response.Status != "success" || response.Data.ResultType != "streams" {
		return nil, errMalformedSmokeEvidence
	}
	var observations []smoke.MarkerObservation
	entryCount := 0
	for _, stream := range response.Data.Result {
		for _, entry := range stream.Values {
			entryCount++
			if entryCount > maximumSmokeMarkerObservations {
				return nil, errMalformedSmokeEvidence
			}
			// 真实 Loki 3.7.2 形状是两元素 [ts, line]，marker 从 stream labels 核对；
			// 三元素 [ts, line, metadata] 作为兼容路径保留，但 marker 必须同样精确。
			if len(entry) != 2 && len(entry) != 3 {
				return nil, errMalformedSmokeEvidence
			}
			var timestamp string
			if err := json.Unmarshal(entry[0], &timestamp); err != nil {
				return nil, errMalformedSmokeEvidence
			}
			// Do not retain the log line, but require its normal string shape so a malformed
			// backend document cannot be converted into a positive marker observation.
			var line string
			if err := json.Unmarshal(entry[1], &line); err != nil {
				return nil, errMalformedSmokeEvidence
			}
			_ = line
			// marker 必须从后端响应再次核对：两元素形状看 stream labels，三元素形状
			// 看条目 metadata；两者都不能靠查询参数自报，labels 与 metadata 互相矛盾
			// 也按畸形文档拒绝。
			labelMarker := stream.Stream["smoke_run_id"]
			if labelMarker != "" && labelMarker != target.Marker {
				return nil, errMalformedSmokeEvidence
			}
			markerMatches := labelMarker == target.Marker
			if len(entry) == 3 {
				var metadata map[string]string
				if err := json.Unmarshal(entry[2], &metadata); err != nil || metadata["smoke_run_id"] != target.Marker {
					return nil, errMalformedSmokeEvidence
				}
				markerMatches = true
			}
			if !markerMatches {
				return nil, errMalformedSmokeEvidence
			}
			observedAt, err := parseUnixNanoseconds(timestamp)
			if err != nil {
				return nil, errMalformedSmokeEvidence
			}
			if isInsideSmokeWindow(observedAt, target) {
				observations = append(observations, smoke.MarkerObservation{Marker: target.Marker, ObservedAt: observedAt})
			}
		}
	}
	return observations, nil
}

func markerObservationsFromNanoseconds(traces []struct {
	StartTimeUnixNano string `json:"startTimeUnixNano"`
}, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	observations := make([]smoke.MarkerObservation, 0, len(traces))
	for _, trace := range traces {
		observedAt, err := parseUnixNanoseconds(trace.StartTimeUnixNano)
		if err != nil {
			return nil, errMalformedSmokeEvidence
		}
		if isInsideSmokeWindow(observedAt, target) {
			observations = append(observations, smoke.MarkerObservation{Marker: target.Marker, ObservedAt: observedAt})
		}
	}
	return observations, nil
}

func parseUnixNanoseconds(value string) (time.Time, error) {
	nanoseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, nanoseconds).UTC(), nil
}

func isInsideSmokeWindow(observedAt time.Time, target smoke.PollMarkerTarget) bool {
	return !observedAt.Before(target.StartedAt) && !observedAt.After(target.Deadline)
}

type prometheusVectorResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func matchesLowCardinalitySelector(labels map[string]string, selector SmokeHTTPCountSelector) bool {
	return len(labels) == 3 &&
		labels["http_route"] == selector.Route &&
		labels["http_request_method"] == selector.Method &&
		labels["http_response_status_class"] == selector.StatusClass
}

func parsePrometheusSampleValue(value []json.RawMessage) (int64, error) {
	if len(value) != 2 {
		return 0, errMalformedSmokeEvidence
	}
	var rendered string
	if err := json.Unmarshal(value[1], &rendered); err != nil {
		return 0, err
	}
	return strconv.ParseInt(rendered, 10, 64)
}

func traceQLMarkerQuery(marker string) string {
	// Tempo treats dots as TraceQL path separators. Quote the complete OTel attribute key and
	// scope it to spans so the smoke cannot silently query a different resource attribute.
	return fmt.Sprintf(`{ span."longtermism.smoke.run_id" = %q }`, marker)
}

func lokiMarkerQuery(marker string) string {
	// Native OTLP log attributes are Loki structured metadata, not JSON body fields.
	return fmt.Sprintf(`{service_name="longtermism"} | smoke_run_id = %q`, marker)
}

// The runner also validates targets, but this adapter is a separate external-query boundary.
// Repeating the bounded, low-sensitivity contract here prevents a direct caller from turning a
// diagnostic query into a large time-range scan or reflecting an arbitrary marker in a DTO.
func isSafeSmokeQueryTarget(target smoke.PollMarkerTarget) bool {
	if !safeSmokeMarkerPattern.MatchString(target.Marker) || target.StartedAt.IsZero() || target.Deadline.IsZero() || !target.Deadline.After(target.StartedAt) || target.Deadline.Sub(target.StartedAt) > maximumSmokeQueryWindow {
		return false
	}
	lowerMarker := strings.ToLower(target.Marker)
	for _, forbidden := range []string{"authorization", "bearer", "credential", "payload", "token", "secret"} {
		if strings.Contains(lowerMarker, forbidden) {
			return false
		}
	}
	// 墙钟边界只防"查未来"与"查远古"两件事；不得拒绝本次 run 自己的窗口：
	// deadline-bounded runner 合法地允许查询发生在窗口尾部（此时 StartedAt
	// 距 now 恰好 ≈ 窗口长度），窗口本身已在上方按长度上限校验过。
	now := time.Now()
	return !target.Deadline.After(now.Add(maximumSmokeQueryWindow)) && !target.Deadline.Before(now.Add(-2*maximumSmokeQueryWindow))
}

func isReportErrorClass(class string) bool {
	switch class {
	case "authentication_failed", "backend_timeout", "temporary_credential_revoke_failed", "backend_unavailable", "export_failed", "invalid_query", "malformed_response", "marker_missing", "query_failed", "metric_delta_missing", "unexpected_evidence", "storage_unavailable", "queue_full", "alert_not_firing":
		return true
	default:
		return false
	}
}
