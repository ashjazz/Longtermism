package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const (
	chatLLMRequestCountQuery = "sum(longtermism_llm_request_count_total)"
	maximumChatQueryWindow   = time.Minute
)

var (
	chatOpaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$`)
	chatTraceIDPattern  = regexp.MustCompile(`^[a-f0-9]{32}$`)
	chatSpanIDPattern   = regexp.MustCompile(`^[a-f0-9]{16}$`)
)

type chatObservationQuery interface {
	Query(context.Context, smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error)
}

type GrafanaChatSmokeBackendConfig struct {
	Grafana  *GrafanaQueryClient
	Langfuse chatObservationQuery
}

type GrafanaChatSmokeBackend struct {
	grafana  *GrafanaQueryClient
	langfuse chatObservationQuery
}

func NewGrafanaChatSmokeBackend(config GrafanaChatSmokeBackendConfig) *GrafanaChatSmokeBackend {
	return &GrafanaChatSmokeBackend{grafana: config.Grafana, langfuse: config.Langfuse}
}

func (backend *GrafanaChatSmokeBackend) QueryTempoChat(ctx context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	if !validChatSmokeTarget(target) || backend == nil || backend.grafana == nil || !backend.grafana.smokeProtected {
		return nil, newBackendQueryError("tempo", "invalid_query")
	}
	result, err := backend.grafana.QueryTempoSince(ctx, chatTempoQuery(target), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, safeChatBackendError("tempo", err)
	}
	observations, err := decodeTempoChatObservations(result, target)
	if err != nil {
		return nil, newBackendQueryError("tempo", "malformed_response")
	}
	return observations, nil
}

func (backend *GrafanaChatSmokeBackend) QueryLokiChat(ctx context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	if !validChatSmokeTarget(target) || backend == nil || backend.grafana == nil || !backend.grafana.smokeProtected {
		return nil, newBackendQueryError("loki", "invalid_query")
	}
	result, err := backend.grafana.QueryLokiSince(ctx, chatLokiQuery(target), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, safeChatBackendError("loki", err)
	}
	observations, err := decodeLokiChatObservations(result, target)
	if err != nil {
		return nil, newBackendQueryError("loki", "malformed_response")
	}
	return observations, nil
}

func (backend *GrafanaChatSmokeBackend) QueryLangfuseChat(ctx context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	if !validChatSmokeTarget(target) || backend == nil || backend.langfuse == nil {
		return nil, newBackendQueryError("langfuse", "invalid_query")
	}
	observations, err := backend.langfuse.Query(ctx, target)
	if err != nil {
		return nil, safeChatBackendError("langfuse", err)
	}
	if len(observations) > target.Limit || len(observations) > 1 {
		return nil, newBackendQueryError("langfuse", "malformed_response")
	}
	for _, observation := range observations {
		if observation.Marker != target.Marker || observation.RequestID != target.RequestID || observation.AITraceID != target.AITraceID ||
			observation.ServiceTraceID != target.ServiceTraceID || observation.SpanID != target.SpanID || !chatObservationInWindow(observation.ObservedAt, target) {
			return nil, newBackendQueryError("langfuse", "malformed_response")
		}
	}
	return append([]smoke.ChatObservation(nil), observations...), nil
}

func (backend *GrafanaChatSmokeBackend) BaselineLLMRequestCount(ctx context.Context) (int64, error) {
	return backend.queryLLMRequestCount(ctx)
}

func (backend *GrafanaChatSmokeBackend) LLMRequestCount(ctx context.Context) (int64, error) {
	return backend.queryLLMRequestCount(ctx)
}

func (backend *GrafanaChatSmokeBackend) queryLLMRequestCount(ctx context.Context) (int64, error) {
	if backend == nil || backend.grafana == nil || !backend.grafana.smokeProtected {
		return 0, newBackendQueryError("prometheus", "invalid_query")
	}
	result, err := backend.grafana.QueryPrometheus(ctx, chatLLMRequestCountQuery)
	if err != nil {
		return 0, safeChatBackendError("prometheus", err)
	}
	count, err := decodeChatLLMRequestCount(result)
	if err != nil {
		return 0, newBackendQueryError("prometheus", "malformed_response")
	}
	return count, nil
}

func validChatSmokeTarget(target smoke.ChatSmokeTarget) bool {
	return target.Limit > 0 && target.Limit <= 100 && !target.StartedAt.IsZero() && target.Deadline.After(target.StartedAt) &&
		target.Deadline.Sub(target.StartedAt) <= maximumChatQueryWindow && safeManifestIDValue(target.Marker) &&
		safeManifestIDValue(target.RequestID) && safeManifestIDValue(target.AITraceID) &&
		chatTraceIDPattern.MatchString(target.ServiceTraceID) && chatSpanIDPattern.MatchString(target.SpanID)
}

func safeManifestIDValue(value string) bool {
	return chatOpaqueIDPattern.MatchString(value)
}

func chatTempoQuery(target smoke.ChatSmokeTarget) string {
	return fmt.Sprintf(`{ span."longtermism.smoke.run_id" = %q && span."request.id" = %q && span."longtermism.ai.trace_id" = %q && trace:id = %q && span:id = %q }`, target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID)
}

func chatLokiQuery(target smoke.ChatSmokeTarget) string {
	return fmt.Sprintf(`{service_name="longtermism"} | smoke_run_id = %q | request_id = %q | ai_trace_id = %q | trace_id = %q`, target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID)
}

func decodeTempoChatObservations(result BackendQueryResult, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	var response struct {
		Traces []struct {
			TraceID           string `json:"traceID"`
			StartTimeUnixNano string `json:"startTimeUnixNano"`
		} `json:"traces"`
	}
	if result.Decode(&response) != nil || len(response.Traces) > target.Limit || len(response.Traces) > 1 {
		return nil, errors.New("malformed Tempo chat evidence")
	}
	if len(response.Traces) == 0 {
		return nil, nil
	}
	item := response.Traces[0]
	nanoseconds, err := strconv.ParseInt(item.StartTimeUnixNano, 10, 64)
	observedAt := time.Unix(0, nanoseconds).UTC()
	if err != nil || item.TraceID != target.ServiceTraceID || !chatObservationInWindow(observedAt, target) {
		return nil, errors.New("malformed Tempo chat evidence")
	}
	return []smoke.ChatObservation{chatObservationFromTarget(target, observedAt)}, nil
}

func decodeLokiChatObservations(result BackendQueryResult, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	var response struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if result.Decode(&response) != nil || response.Status != "success" || response.Data.ResultType != "streams" {
		return nil, errors.New("malformed Loki chat evidence")
	}
	observations := make([]smoke.ChatObservation, 0, 1)
	for _, stream := range response.Data.Result {
		for _, value := range stream.Values {
			if len(value) != 3 || len(observations) >= target.Limit {
				return nil, errors.New("malformed Loki chat evidence")
			}
			var timestamp, line string
			if json.Unmarshal(value[0], &timestamp) != nil || json.Unmarshal(value[1], &line) != nil || line == "" {
				return nil, errors.New("malformed Loki chat evidence")
			}
			nanoseconds, err := strconv.ParseInt(timestamp, 10, 64)
			observedAt := time.Unix(0, nanoseconds).UTC()
			var identity struct {
				Marker         string `json:"smoke_run_id"`
				RequestID      string `json:"request_id"`
				AITraceID      string `json:"ai_trace_id"`
				ServiceTraceID string `json:"trace_id"`
				SpanID         string `json:"span_id"`
			}
			if err != nil || json.Unmarshal(value[2], &identity) != nil || !chatObservationInWindow(observedAt, target) ||
				identity.Marker != target.Marker || identity.RequestID != target.RequestID || identity.AITraceID != target.AITraceID ||
				identity.ServiceTraceID != target.ServiceTraceID || !chatSpanIDPattern.MatchString(identity.SpanID) {
				return nil, errors.New("malformed Loki chat evidence")
			}
			observations = append(observations, smoke.ChatObservation{
				Marker: target.Marker, RequestID: target.RequestID, AITraceID: target.AITraceID,
				ServiceTraceID: identity.ServiceTraceID, SpanID: identity.SpanID, ObservedAt: observedAt,
			})
		}
	}
	if len(observations) > 1 {
		return nil, errors.New("ambiguous Loki chat evidence")
	}
	return observations, nil
}

func decodeChatLLMRequestCount(result BackendQueryResult) (int64, error) {
	var response struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if result.Decode(&response) != nil || response.Status != "success" || response.Data.ResultType != "vector" || len(response.Data.Result) != 1 || len(response.Data.Result[0].Metric) != 0 || len(response.Data.Result[0].Value) != 2 {
		return 0, errors.New("malformed Prometheus chat evidence")
	}
	var raw string
	if json.Unmarshal(response.Data.Result[0].Value[1], &raw) != nil {
		return 0, errors.New("malformed Prometheus chat evidence")
	}
	count, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || count < 0 {
		return 0, errors.New("malformed Prometheus chat evidence")
	}
	return count, nil
}

func chatObservationFromTarget(target smoke.ChatSmokeTarget, observedAt time.Time) smoke.ChatObservation {
	return smoke.ChatObservation{Marker: target.Marker, RequestID: target.RequestID, AITraceID: target.AITraceID, ServiceTraceID: target.ServiceTraceID, SpanID: target.SpanID, ObservedAt: observedAt}
}

func chatObservationInWindow(observedAt time.Time, target smoke.ChatSmokeTarget) bool {
	return !observedAt.Before(target.StartedAt) && !observedAt.After(target.Deadline)
}

func safeChatBackendError(backend string, err error) error {
	return newBackendQueryError(backend, smokeReportErrorClass(err))
}

var _ smoke.ChatSmokeBackend = (*GrafanaChatSmokeBackend)(nil)
