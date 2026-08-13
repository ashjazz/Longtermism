package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

type LangfuseChatSmokeQueryConfig struct {
	BaseURL     string
	Credential  string
	Timeout     time.Duration
	ResolveHost HostResolver
}

type LangfuseChatSmokeQueryClient struct{ query *negativeSmokeQueryClient }

func NewLangfuseChatSmokeQueryClient(config LangfuseChatSmokeQueryConfig) (*LangfuseChatSmokeQueryClient, error) {
	// 仓库锁定的 self-hosted Langfuse v3 使用 legacy v1 observations endpoint；v2
	// 只在 self-hosted v4 可用。这里仍使用 v3 已支持的结构化 filter，避免宽查询。
	query, err := newNegativeSmokeQueryClient("langfuse", "/api/public/observations", config.BaseURL, config.Credential, config.Timeout, config.ResolveHost)
	if err != nil {
		return nil, err
	}
	return &LangfuseChatSmokeQueryClient{query: query}, nil
}

func (client *LangfuseChatSmokeQueryClient) Query(ctx context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	if client == nil || client.query == nil || !validChatSmokeTarget(target) {
		return nil, newBackendQueryError("langfuse", "invalid_query")
	}
	filter, err := langfuseChatFilter(target)
	if err != nil {
		return nil, newBackendQueryError("langfuse", "invalid_query")
	}
	body, err := client.query.get(ctx, url.Values{
		"limit": {strconv.Itoa(target.Limit)}, "page": {"1"}, "filter": {filter},
		"fromStartTime": {target.StartedAt.UTC().Format(time.RFC3339Nano)},
		"toStartTime":   {target.Deadline.UTC().Format(time.RFC3339Nano)},
	})
	if err != nil {
		return nil, err
	}
	return decodeLangfuseChatObservations(body, target)
}

func langfuseChatFilter(target smoke.ChatSmokeTarget) (string, error) {
	if !validChatSmokeTarget(target) {
		return "", errors.New("unsafe chat target")
	}
	filters := []map[string]string{
		{"type": "stringObject", "column": "metadata", "key": "longtermism.smoke.run_id", "operator": "=", "value": target.Marker},
		{"type": "stringObject", "column": "metadata", "key": "request_id", "operator": "=", "value": target.RequestID},
		{"type": "stringObject", "column": "metadata", "key": "ai_trace_id", "operator": "=", "value": target.AITraceID},
		{"type": "string", "column": "traceId", "operator": "=", "value": target.ServiceTraceID},
		{"type": "string", "column": "id", "operator": "=", "value": target.SpanID},
		{"type": "datetime", "column": "startTime", "operator": ">=", "value": target.StartedAt.UTC().Format(time.RFC3339Nano)},
		{"type": "datetime", "column": "startTime", "operator": "<=", "value": target.Deadline.UTC().Format(time.RFC3339Nano)},
	}
	encoded, err := json.Marshal(filters)
	return string(encoded), err
}

func decodeLangfuseChatObservations(body []byte, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	var response struct {
		Data []struct {
			ID        string                     `json:"id"`
			TraceID   string                     `json:"traceId"`
			StartTime string                     `json:"startTime"`
			Metadata  map[string]json.RawMessage `json:"metadata"`
		} `json:"data"`
		Meta struct {
			Page       int `json:"page"`
			TotalPages int `json:"totalPages"`
		} `json:"meta"`
	}
	if json.Unmarshal(body, &response) != nil || response.Meta.Page != 1 || response.Meta.TotalPages < 0 || response.Meta.TotalPages > 1 ||
		(len(response.Data) > 0 && response.Meta.TotalPages != 1) || len(response.Data) > target.Limit || len(response.Data) > 1 {
		return nil, newBackendQueryError("langfuse", "malformed_response")
	}
	if len(response.Data) == 0 {
		return nil, nil
	}
	item := response.Data[0]
	observedAt, err := time.Parse(time.RFC3339Nano, item.StartTime)
	marker, markerOK := requiredMetadataString(item.Metadata, "longtermism.smoke.run_id")
	requestID, requestOK := requiredMetadataString(item.Metadata, "request_id")
	aiTraceID, aiTraceOK := requiredMetadataString(item.Metadata, "ai_trace_id")
	if err != nil || !chatObservationInWindow(observedAt, target) || item.ID != target.SpanID || item.TraceID != target.ServiceTraceID ||
		!markerOK || marker != target.Marker || !requestOK || requestID != target.RequestID || !aiTraceOK || aiTraceID != target.AITraceID {
		return nil, newBackendQueryError("langfuse", "malformed_response")
	}
	return []smoke.ChatObservation{chatObservationFromTarget(target, observedAt.UTC())}, nil
}

func requiredMetadataString(metadata map[string]json.RawMessage, key string) (string, bool) {
	var value string
	err := json.Unmarshal(metadata[key], &value)
	return value, err == nil && value != ""
}

var _ chatObservationQuery = (*LangfuseChatSmokeQueryClient)(nil)
