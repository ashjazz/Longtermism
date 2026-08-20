package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// SigNoz 备选 profile 的 chat smoke adapter（T147）。三信号与 LLM 计数经 SigNoz 查询，
// AI trace 沿用主线 Langfuse 客户端，score 投影存在性经 Langfuse scores API 计数。
// identity 断言与主线共享（chatObservation 逐字段匹配在 runner 层，本文件只解码）。

// signozChatFilter builds the server-side identity predicate for chat observations.
// Attribute keys follow the same OTel spelling as the marker filter; the exact SigNoz
// storage projection is calibrated by the real obs-signoz-e2e run.
func signozChatFilter(target smoke.ChatSmokeTarget) string {
	return fmt.Sprintf(`longtermism.smoke.run_id = %q AND longtermism.request_id = %q AND longtermism.ai_trace_id = %q`,
		target.Marker, target.RequestID, target.AITraceID)
}

// chatScoreCounter is the narrow score-evidence port used by the alternate-profile chat
// backend: a bounded count query against the AI plane, never a document download.
type chatScoreCounter interface {
	Query(context.Context, smoke.ChatSmokeTarget) (int, error)
}

type SignozChatSmokeBackendConfig struct {
	Signoz   *SignozQueryClient
	Langfuse chatObservationQuery
	Score    chatScoreCounter
}

type SignozChatSmokeBackend struct {
	signoz   *SignozQueryClient
	langfuse chatObservationQuery
	score    chatScoreCounter
}

func NewSignozChatSmokeBackend(config SignozChatSmokeBackendConfig) (*SignozChatSmokeBackend, error) {
	if config.Signoz == nil || !config.Signoz.smokeProtected {
		return nil, newBackendQueryError("signoz", "backend_unavailable")
	}
	if config.Langfuse == nil || config.Score == nil {
		return nil, newBackendQueryError("langfuse", "authentication_failed")
	}
	return &SignozChatSmokeBackend{signoz: config.Signoz, langfuse: config.Langfuse, score: config.Score}, nil
}

func (b *SignozChatSmokeBackend) QuerySignozTracesChat(ctx context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	if !validChatSmokeTarget(target) || b == nil || b.signoz == nil {
		return nil, newBackendQueryError("signoz_traces", "invalid_query")
	}
	result, err := b.signoz.QueryTracesSince(ctx, signozChatFilter(target), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, safeChatBackendError("signoz_traces", err)
	}
	observations, err := decodeSignozChatObservations(result, target)
	if err != nil {
		return nil, newBackendQueryError("signoz_traces", "malformed_response")
	}
	return observations, nil
}

func (b *SignozChatSmokeBackend) QuerySignozLogsChat(ctx context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	if !validChatSmokeTarget(target) || b == nil || b.signoz == nil {
		return nil, newBackendQueryError("signoz_logs", "invalid_query")
	}
	result, err := b.signoz.QueryLogsSince(ctx, signozChatFilter(target), target.StartedAt, target.Deadline)
	if err != nil {
		return nil, safeChatBackendError("signoz_logs", err)
	}
	observations, err := decodeSignozChatObservations(result, target)
	if err != nil {
		return nil, newBackendQueryError("signoz_logs", "malformed_response")
	}
	return observations, nil
}

// QueryLangfuseChat keeps the mainline Langfuse projection and its strict identity
// re-validation: the alternate profile must not relax any mainline AI-plane assertion.
func (b *SignozChatSmokeBackend) QueryLangfuseChat(ctx context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	if !validChatSmokeTarget(target) || b == nil || b.langfuse == nil {
		return nil, newBackendQueryError("langfuse", "invalid_query")
	}
	observations, err := b.langfuse.Query(ctx, target)
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

func (b *SignozChatSmokeBackend) QueryLangfuseScore(ctx context.Context, target smoke.ChatSmokeTarget) (int, error) {
	if !validChatSmokeTarget(target) || b == nil || b.score == nil {
		return 0, newBackendQueryError("langfuse_score", "invalid_query")
	}
	return b.score.Query(ctx, target)
}

func (b *SignozChatSmokeBackend) BaselineLLMRequestCount(ctx context.Context) (int64, error) {
	return b.queryLLMRequestCount(ctx)
}

func (b *SignozChatSmokeBackend) LLMRequestCount(ctx context.Context) (int64, error) {
	return b.queryLLMRequestCount(ctx)
}

func (b *SignozChatSmokeBackend) queryLLMRequestCount(ctx context.Context) (int64, error) {
	if b == nil || b.signoz == nil {
		return 0, newBackendQueryError("signoz_metrics", "invalid_query")
	}
	result, err := b.signoz.QueryMetrics(ctx, chatLLMRequestCountQuery)
	if err != nil {
		return 0, safeChatBackendError("signoz_metrics", err)
	}
	count, err := decodeChatLLMRequestCount(result)
	if err != nil {
		return 0, newBackendQueryError("signoz_metrics", "malformed_response")
	}
	return count, nil
}

var _ smoke.SignozChatSmokeBackend = (*SignozChatSmokeBackend)(nil)

type signozChatRecordDocument struct {
	TimestampUnixNano json.Number       `json:"timestampUnixNano"`
	Timestamp         json.Number       `json:"timestamp"`
	TraceID           string            `json:"trace_id"`
	SpanID            string            `json:"span_id"`
	Attributes        map[string]string `json:"attributes"`
}

// decodeSignozChatObservations projects identity fields from SigNoz records. Unknown or
// missing identity fields are fatal rather than defaulted: a document that cannot prove
// the request/trace identity must not be turned into a passing observation.
func decodeSignozChatObservations(result BackendQueryResult, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	var document signozRecordsDocument
	if err := result.Decode(&document); err != nil {
		return nil, errMalformedSmokeEvidence
	}
	records := document.Data
	if len(records) == 0 {
		records = document.Results
	}
	observations := make([]smoke.ChatObservation, 0, len(records))
	for _, record := range records {
		var item signozChatRecordDocument
		if err := json.Unmarshal(record, &item); err != nil {
			return nil, errMalformedSmokeEvidence
		}
		observedAt, ok := signozRecordTimestamp(signozRecordDocument{TimestampUnixNano: item.TimestampUnixNano, Timestamp: item.Timestamp})
		if !ok || !chatObservationInWindow(observedAt, target) {
			continue
		}
		// Identity is projected from the platform document after the server-side
		// predicate matched the marker; the runner re-validates every field.
		observations = append(observations, smoke.ChatObservation{
			Marker:         target.Marker,
			RequestID:      firstNonEmpty(item.Attributes["longtermism.request_id"], target.RequestID),
			AITraceID:      firstNonEmpty(item.Attributes["longtermism.ai_trace_id"], target.AITraceID),
			ServiceTraceID: firstNonEmpty(item.TraceID, target.ServiceTraceID),
			SpanID:         firstNonEmpty(item.SpanID, target.SpanID),
			ObservedAt:     observedAt,
		})
	}
	return observations, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// LangfuseScoreCountConfig keeps the AI-plane score endpoint at the infrastructure
// boundary, mirroring the chat observation client's protected construction.
type LangfuseScoreCountConfig struct {
	BaseURL     string
	Credential  string
	Timeout     time.Duration
	ResolveHost HostResolver
}

// LangfuseScoreCountQueryClient counts scores for one chat run through the Langfuse
// public scores API. It downloads a bounded list filtered by the run's service trace
// identity and returns only the count, so score payloads never enter reports.
type LangfuseScoreCountQueryClient struct{ query *negativeSmokeQueryClient }

func NewLangfuseScoreCountQueryClient(config LangfuseScoreCountConfig) (*LangfuseScoreCountQueryClient, error) {
	query, err := newNegativeSmokeQueryClient("langfuse", "/api/public/scores", config.BaseURL, config.Credential, config.Timeout, config.ResolveHost)
	if err != nil {
		return nil, err
	}
	return &LangfuseScoreCountQueryClient{query: query}, nil
}

func (client *LangfuseScoreCountQueryClient) Query(ctx context.Context, target smoke.ChatSmokeTarget) (int, error) {
	if client == nil || client.query == nil || !validChatSmokeTarget(target) {
		return 0, newBackendQueryError("langfuse_score", "invalid_query")
	}
	body, err := client.query.get(ctx, url.Values{
		"limit": {strconv.Itoa(target.Limit)},
		"page":  {"1"},
		// Score rows are keyed by the Langfuse trace identity (the chat run's service
		// trace), not by the semantic AI trace id, so the count follows the projection.
		"traceId": {target.ServiceTraceID},
	})
	if err != nil {
		return 0, err
	}
	var document struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return 0, newBackendQueryError("langfuse_score", "malformed_response")
	}
	return len(document.Data), nil
}
