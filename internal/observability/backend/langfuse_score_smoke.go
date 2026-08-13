package backend

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const maximumScoreQueryWindow = 2 * time.Minute

// scoreProjectionLookup exposes only the current local projection snapshot. The platform is
// deliberately queried after this lookup proves one exact identity; it must never repair or
// invent a missing local fact.
type scoreProjectionLookup interface {
	FindByRunID(context.Context, string) ([]localeval.ScoreProjectionSnapshot, error)
}

type LangfuseScoreSmokeBackendConfig struct {
	BaseURL         string
	Credential      string
	Timeout         time.Duration
	ResolveHost     HostResolver
	ProjectionStore scoreProjectionLookup
}

type LangfuseScoreSmokeBackend struct {
	query *negativeSmokeQueryClient
	store scoreProjectionLookup
}

func NewLangfuseScoreSmokeBackend(config LangfuseScoreSmokeBackendConfig) (*LangfuseScoreSmokeBackend, error) {
	if config.ProjectionStore == nil {
		return nil, newBackendQueryError("langfuse_score", "storage_unavailable")
	}
	query, err := newNegativeSmokeQueryClient("langfuse_score", "/api/public/v3/scores", config.BaseURL, config.Credential, config.Timeout, config.ResolveHost)
	if err != nil {
		return nil, err
	}
	return &LangfuseScoreSmokeBackend{query: query, store: config.ProjectionStore}, nil
}

func (backend *LangfuseScoreSmokeBackend) IsConfigured(ctx context.Context) bool {
	return backend != nil && backend.query != nil && backend.store != nil && ctx != nil && ctx.Err() == nil
}

func (backend *LangfuseScoreSmokeBackend) ProjectionStates(ctx context.Context, target smoke.ScoreSmokeProjectionTarget) ([]smoke.ScoreSmokeProjectionObservation, error) {
	if backend == nil || backend.query == nil || backend.store == nil || ctx == nil || ctx.Err() != nil || !validScoreSmokeProjectionTarget(target) {
		return nil, newBackendQueryError("langfuse_score", "invalid_query")
	}
	records, err := backend.store.FindByRunID(ctx, target.RunID)
	if err != nil {
		return nil, newBackendQueryError("langfuse_score", "storage_unavailable")
	}
	if len(records) != 1 || !scoreSnapshotMatchesTarget(records[0], target) {
		return nil, newBackendQueryError("langfuse_score", "unexpected_evidence")
	}
	snapshot := records[0]
	if snapshot.Status != langfuse.ScoreProjectionStatusSent {
		return []smoke.ScoreSmokeProjectionObservation{scoreObservationFromSnapshot(snapshot, snapshot.ObservedAt)}, nil
	}

	body, err := backend.query.get(ctx, url.Values{
		"id":            {target.ProjectionID},
		"traceId":       {target.PlatformTraceID},
		"observationId": {target.PlatformObservationID},
		"fields":        {"subject"},
		"limit":         {strconv.Itoa(target.Limit)},
		"fromTimestamp": {target.StartedAt.UTC().Format(time.RFC3339Nano)},
		"toTimestamp":   {target.Deadline.UTC().Format(time.RFC3339Nano)},
	})
	if err != nil {
		return nil, err
	}
	return decodeLangfuseScoreObservation(body, target, snapshot)
}

func validScoreSmokeProjectionTarget(target smoke.ScoreSmokeProjectionTarget) bool {
	return safeManifestIDValue(target.RunID) && safeManifestIDValue(target.Marker) &&
		safeManifestIDValue(target.ProjectionID) && safeManifestIDValue(target.EvalRunID) &&
		safeManifestIDValue(target.RequestID) && safeManifestIDValue(target.AITraceID) &&
		chatTraceIDPattern.MatchString(target.PlatformTraceID) && chatSpanIDPattern.MatchString(target.PlatformObservationID) &&
		target.Limit > 0 && target.Limit <= 100 && !target.StartedAt.IsZero() && target.Deadline.After(target.StartedAt) &&
		target.Deadline.Sub(target.StartedAt) <= maximumScoreQueryWindow
}

func scoreSnapshotMatchesTarget(snapshot localeval.ScoreProjectionSnapshot, target smoke.ScoreSmokeProjectionTarget) bool {
	return snapshot.RunID == target.RunID && snapshot.EvalRunID == target.EvalRunID && snapshot.ProjectionID == target.ProjectionID &&
		snapshot.RequestID == target.RequestID && snapshot.AITraceID == target.AITraceID &&
		snapshot.PlatformTraceID == target.PlatformTraceID && snapshot.PlatformObservationID == target.PlatformObservationID &&
		snapshot.Attempt >= 0 && validStoredScoreProjectionStatus(snapshot.Status) && !snapshot.CreatedAt.IsZero() && !snapshot.ObservedAt.IsZero()
}

func validStoredScoreProjectionStatus(status langfuse.ScoreProjectionStatus) bool {
	switch status {
	case langfuse.ScoreProjectionStatusQueued, langfuse.ScoreProjectionStatusSending,
		langfuse.ScoreProjectionStatusRetryWait, langfuse.ScoreProjectionStatusSent,
		langfuse.ScoreProjectionStatusDroppedQueueFull, langfuse.ScoreProjectionStatusFailedPermanent,
		langfuse.ScoreProjectionStatusFailedShutdownTimeout, langfuse.ScoreProjectionStatusNotConfigured:
		return true
	default:
		return false
	}
}

func scoreObservationFromSnapshot(snapshot localeval.ScoreProjectionSnapshot, observedAt time.Time) smoke.ScoreSmokeProjectionObservation {
	return smoke.ScoreSmokeProjectionObservation{ProjectionID: snapshot.ProjectionID, Status: string(snapshot.Status), Attempt: snapshot.Attempt, ObservedAt: observedAt.UTC()}
}

func decodeLangfuseScoreObservation(body []byte, target smoke.ScoreSmokeProjectionTarget, snapshot localeval.ScoreProjectionSnapshot) ([]smoke.ScoreSmokeProjectionObservation, error) {
	var response struct {
		Data []struct {
			ID        string `json:"id"`
			Timestamp string `json:"timestamp"`
			Subject   struct {
				Kind    string `json:"kind"`
				ID      string `json:"id"`
				TraceID string `json:"traceId"`
			} `json:"subject"`
		} `json:"data"`
		Meta struct {
			Cursor *string `json:"cursor"`
		} `json:"meta"`
	}
	if json.Unmarshal(body, &response) != nil || response.Meta.Cursor != nil || len(response.Data) > target.Limit {
		return nil, newBackendQueryError("langfuse_score", "malformed_response")
	}
	if len(response.Data) != 1 {
		return nil, newBackendQueryError("langfuse_score", "unexpected_evidence")
	}
	item := response.Data[0]
	observedAt, err := time.Parse(time.RFC3339Nano, item.Timestamp)
	if err != nil || observedAt.Before(target.StartedAt) || observedAt.After(target.Deadline) ||
		item.ID != target.ProjectionID || item.Subject.Kind != "observation" ||
		item.Subject.ID != target.PlatformObservationID || item.Subject.TraceID != target.PlatformTraceID {
		return nil, newBackendQueryError("langfuse_score", "unexpected_evidence")
	}
	return []smoke.ScoreSmokeProjectionObservation{scoreObservationFromSnapshot(snapshot, observedAt)}, nil
}

var _ smoke.ScoreSmokeBackend = (*LangfuseScoreSmokeBackend)(nil)
