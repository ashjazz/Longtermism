package cmd

import (
	"context"
	"errors"
	"time"

	appeval "github.com/ashjazz/Longtermism/internal/eval"
	logicchat "github.com/ashjazz/Longtermism/internal/logic/chat"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

var errChatScoreProjectionUnavailable = errors.New("chat score projection is unavailable")

type notConfiguredChatProjectionQueue struct{}

func (notConfiguredChatProjectionQueue) TryEnqueue(context.Context, logicchat.ChatScoreProjectionInput) error {
	return nil
}

// chatScoreProjectionQueue is the composition adapter between platform-neutral
// chat facts and the durable Langfuse lifecycle. It derives platform provenance
// only through the reviewed trace mapper; it never treats ai_trace_id as a native ID.
type chatScoreProjectionQueue struct {
	lifecycle   *LangfuseScoreLifecycle
	maxAttempts int
	now         func() time.Time
}

func newChatScoreProjectionQueue(lifecycle *LangfuseScoreLifecycle, maxAttempts int) logicchat.ChatScoreProjectionQueue {
	if lifecycle == nil || maxAttempts <= 0 {
		return nil
	}
	return &chatScoreProjectionQueue{lifecycle: lifecycle, maxAttempts: maxAttempts, now: time.Now}
}

func (queue *chatScoreProjectionQueue) TryEnqueue(ctx context.Context, input logicchat.ChatScoreProjectionInput) error {
	if queue == nil || queue.lifecycle == nil || ctx == nil || ctx.Err() != nil || !input.Generation.CanProject() {
		return errChatScoreProjectionUnavailable
	}
	trace, err := langfuse.MapTraceToProjection(langfuse.TraceMapperInput{
		Span: langfuse.OTLPSpanSnapshot{
			TraceID: input.Generation.TraceID, SpanID: input.Generation.SpanID,
			Name: "ai.generation", ObservationType: obs.ObservationTypeGeneration,
			Attributes: map[string]string{
				"ai.feature": "chat", "longtermism.ai.trace_id": input.Evidence.AITraceID,
				"request.id": input.Evidence.RequestID,
			},
		},
		PayloadMode: obs.PayloadModeMetadataOnly,
	})
	if err != nil {
		return errChatScoreProjectionUnavailable
	}
	target, err := langfuse.NewScoreTarget(trace, langfuse.ScoreTargetKindObservation)
	if err != nil {
		return errChatScoreProjectionUnavailable
	}
	projection, err := langfuse.NewScoreProjection(langfuse.ScoreProjectionInput{
		Target: target, Evidence: input.Evidence, MaxAttempts: queue.maxAttempts, CreatedAt: queue.now().UTC(),
	})
	if err != nil {
		return errChatScoreProjectionUnavailable
	}
	// A protected smoke marker is the external lookup key. Ordinary chats use the
	// deterministic ProjectionID as an internal-only durable key, avoiding any
	// inference from EvalRunID, names or timestamps.
	runID := input.RunID
	if runID == "" {
		runID = projection.ProjectionID
	}
	result, err := queue.lifecycle.EnqueueForRun(ctx, runID, projection)
	if err != nil {
		return errChatScoreProjectionUnavailable
	}
	if result.Status != langfuse.ScoreProjectionStatusQueued {
		return errChatScoreProjectionUnavailable
	}
	return nil
}

type chatProjectionStoreAdapter struct{ store *appeval.ScoreProjectionStore }

func (adapter chatProjectionStoreAdapter) SaveInitial(ctx context.Context, runID string, projection langfuse.ScoreProjection, maxAttempts int) error {
	return adapter.store.SaveInitial(ctx, runID, projection, maxAttempts)
}

func (adapter chatProjectionStoreAdapter) Update(ctx context.Context, runID string, projection langfuse.ScoreProjection) error {
	return adapter.store.Update(ctx, runID, projection)
}

func (adapter chatProjectionStoreAdapter) LoadPending(ctx context.Context) ([]LangfuseStoredProjection, error) {
	records, err := adapter.store.LoadPending(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]LangfuseStoredProjection, 0, len(records))
	for _, record := range records {
		result = append(result, LangfuseStoredProjection{RunID: record.RunID, Snapshot: LangfuseProjectionRecoverySnapshot{
			ProjectionID: record.ProjectionID, Evidence: record.Evidence, TargetKind: record.TargetKind,
			PlatformTraceID: record.PlatformTraceID, PlatformObservationID: record.PlatformObservationID,
			Status: record.Status, Attempt: record.Attempt, CreatedAt: record.CreatedAt, MaxAttempts: record.MaxAttempts,
		}})
	}
	return result, nil
}
