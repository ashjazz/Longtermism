package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

const langfuseScoreBackend = "langfuse"

const (
	maxLangfuseScoreQueueCapacity = 4096
	// MaxAttempts means retry count in the T100 worker. Two retries keep the
	// application-wide external-call policy at three total attempts.
	maxLangfuseScoreAttempts = 2
	maxLangfuseScoreBackoff  = 60 * time.Second
	maxLangfuseScoreTimeout  = 60 * time.Second
)

var (
	errInvalidLangfuseScoreLifecycle = errors.New("langfuse score lifecycle configuration is invalid")
	ErrLangfuseProjectionPersistence = errors.New("langfuse projection persistence failed")
	ErrLangfuseEvidenceNotPersisted  = errors.New("langfuse evidence is not uniquely persisted")
)

type LangfuseScoreLifecycleState string

const (
	LangfuseScoreLifecycleStateNotConfigured LangfuseScoreLifecycleState = "not_configured"
	LangfuseScoreLifecycleStateRunning       LangfuseScoreLifecycleState = "running"
	LangfuseScoreLifecycleStateShutdown      LangfuseScoreLifecycleState = "shutdown"
)

type LangfuseScoreLifecycleInput struct {
	BaseURL        string
	PublicKey      string
	SecretKey      string
	QueueCapacity  int
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	RequestTimeout time.Duration
}

// LangfuseScoreWorker is the app-layer lifecycle surface. Enqueue must retain
// T100's synchronous, bounded and immediately returning admission contract.
type LangfuseScoreWorker interface {
	Start()
	Enqueue(langfuse.ScoreProjection) langfuse.ScoreProjection
	Shutdown(context.Context) error
	QueueDepth() int
}

type LangfuseScoreMetrics interface {
	// Implementations must be local, non-blocking instruments. Network export
	// belongs to the OTel SDK/Collector and must never run on the chat path.
	RecordScoreProjection(context.Context, appobservability.ScoreProjectionMetric) error
	RecordScoreQueue(context.Context, appobservability.ScoreQueueMetric) error
}

type LangfuseScoreLifecycleDependencies struct {
	NewClient       func(langfuse.ScoreClientConfig) (langfuse.ScoreSender, error)
	NewWorker       func(langfuse.ScoreWorkerConfig) (LangfuseScoreWorker, error)
	Metrics         LangfuseScoreMetrics
	ProjectionStore LangfuseProjectionStore
	EvidenceStore   LangfuseEvidenceLookup
	state           *langfuseScoreLifecycleState
}

type LangfuseEvidenceLookup interface {
	Find(context.Context, string) ([]aieval.EvaluationEvidence, error)
}

type LangfuseProjectionStore interface {
	SaveInitial(context.Context, string, langfuse.ScoreProjection, int) error
	Update(context.Context, string, langfuse.ScoreProjection) error
	LoadPending(context.Context) ([]LangfuseStoredProjection, error)
}

type LangfuseProjectionRecoverySnapshot struct {
	ProjectionID, PlatformTraceID, PlatformObservationID string
	Evidence                                             aieval.EvaluationEvidence
	TargetKind                                           langfuse.ScoreTargetKind
	Status                                               langfuse.ScoreProjectionStatus
	Attempt, MaxAttempts                                 int
	CreatedAt                                            time.Time
}

type LangfuseStoredProjection struct {
	RunID    string
	Snapshot LangfuseProjectionRecoverySnapshot
}

type LangfuseScoreLifecycleStatus struct {
	State         LangfuseScoreLifecycleState
	Started       bool
	Shutdown      bool
	QueueCapacity int
	QueueDepth    int
}

type LangfuseScoreLifecycle struct {
	mu              sync.Mutex
	worker          LangfuseScoreWorker
	metrics         LangfuseScoreMetrics
	projectionStore LangfuseProjectionStore
	evidenceStore   LangfuseEvidenceLookup
	maxAttempts     int
	projectionRuns  sync.Map
	status          LangfuseScoreLifecycleStatus
	shutdownOnce    sync.Once
	shutdownErr     error
	ownerState      *langfuseScoreLifecycleState
}

type langfuseScoreLifecycleState struct {
	mu        sync.Mutex
	lifecycle *LangfuseScoreLifecycle
}

type lifecycleProjectionRecorder struct {
	store LangfuseProjectionStore
	runs  *sync.Map
}

func (recorder *lifecycleProjectionRecorder) Record(ctx context.Context, projection langfuse.ScoreProjection) error {
	if recorder == nil || recorder.store == nil || recorder.runs == nil {
		return ErrLangfuseProjectionPersistence
	}
	runID, ok := recorder.runs.Load(projection.ProjectionID)
	if !ok {
		return ErrLangfuseProjectionPersistence
	}
	value, ok := runID.(string)
	if !ok || value == "" {
		return ErrLangfuseProjectionPersistence
	}
	if err := recorder.store.Update(ctx, value, projection.Snapshot()); err != nil {
		return err
	}
	if isTerminalScoreProjectionStatus(projection.Status) {
		recorder.runs.Delete(projection.ProjectionID)
	}
	return nil
}

var processLangfuseScoreLifecycleState langfuseScoreLifecycleState

func BuildLangfuseScoreLifecycle(
	ctx context.Context,
	input LangfuseScoreLifecycleInput,
	dependencies LangfuseScoreLifecycleDependencies,
) (*LangfuseScoreLifecycle, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	dependencies = defaultLangfuseScoreLifecycleDependencies(dependencies)
	dependencies.state.mu.Lock()
	defer dependencies.state.mu.Unlock()
	if dependencies.state.lifecycle != nil {
		return dependencies.state.lifecycle, nil
	}

	lifecycle, err := newLangfuseScoreLifecycle(ctx, input, dependencies)
	if err != nil {
		return nil, err
	}
	dependencies.state.lifecycle = lifecycle
	lifecycle.ownerState = dependencies.state
	return lifecycle, nil
}

func newLangfuseScoreLifecycle(
	ctx context.Context,
	input LangfuseScoreLifecycleInput,
	dependencies LangfuseScoreLifecycleDependencies,
) (*LangfuseScoreLifecycle, error) {
	if isEmptyLangfuseScoreLifecycleInput(input) {
		return &LangfuseScoreLifecycle{
			metrics:         dependencies.Metrics,
			projectionStore: dependencies.ProjectionStore,
			evidenceStore:   dependencies.EvidenceStore,
			maxAttempts:     maxLangfuseScoreAttempts,
			status:          LangfuseScoreLifecycleStatus{State: LangfuseScoreLifecycleStateNotConfigured},
		}, nil
	}
	if !isCompleteLangfuseScoreLifecycleInput(input) {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	if dependencies.ProjectionStore == nil || dependencies.EvidenceStore == nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}

	sender, err := dependencies.NewClient(langfuse.ScoreClientConfig{
		BaseURL: input.BaseURL, PublicKey: input.PublicKey, SecretKey: input.SecretKey, Timeout: input.RequestTimeout,
	})
	if err != nil || sender == nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	if ctx.Err() != nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	var worker LangfuseScoreWorker
	var recorder langfuse.ScoreProjectionRecorder
	if dependencies.ProjectionStore != nil {
		recorder = &lifecycleProjectionRecorder{store: dependencies.ProjectionStore}
	}
	transitionRecorder := scoreTransitionRecorder(dependencies.Metrics, func() int {
		if worker == nil {
			return 0
		}
		return worker.QueueDepth()
	})
	worker, err = dependencies.NewWorker(langfuse.ScoreWorkerConfig{
		QueueCapacity:      input.QueueCapacity,
		MaxAttempts:        input.MaxAttempts,
		InitialBackoff:     input.InitialBackoff,
		MaxBackoff:         input.MaxBackoff,
		Sender:             sender,
		ProjectionRecorder: recorder,
		OnTransition:       transitionRecorder,
	})
	if err != nil || worker == nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	if ctx.Err() != nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	lifecycle := &LangfuseScoreLifecycle{
		worker:          worker,
		metrics:         dependencies.Metrics,
		projectionStore: dependencies.ProjectionStore,
		evidenceStore:   dependencies.EvidenceStore,
		maxAttempts:     input.MaxAttempts,
		status: LangfuseScoreLifecycleStatus{
			State: LangfuseScoreLifecycleStateRunning, Started: true, QueueCapacity: input.QueueCapacity,
		},
	}
	if durable, ok := recorder.(*lifecycleProjectionRecorder); ok {
		durable.runs = &lifecycle.projectionRuns
	}
	if err := lifecycle.recoverPending(ctx); err != nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	worker.Start()
	return lifecycle, nil
}

func (lifecycle *LangfuseScoreLifecycle) Enqueue(projection langfuse.ScoreProjection) langfuse.ScoreProjection {
	if lifecycle == nil {
		return transitionLifecycleProjection(projection, langfuse.ScoreProjectionStatusNotConfigured)
	}
	lifecycle.mu.Lock()
	worker, metrics, state := lifecycle.worker, lifecycle.metrics, lifecycle.status.State
	lifecycle.mu.Unlock()

	if state == LangfuseScoreLifecycleStateNotConfigured || worker == nil {
		result := transitionLifecycleProjection(projection, langfuse.ScoreProjectionStatusNotConfigured)
		recordLifecycleProjection(metrics, result.Status)
		return result
	}
	if state == LangfuseScoreLifecycleStateShutdown {
		result := transitionLifecycleProjection(projection, langfuse.ScoreProjectionStatusFailedShutdownTimeout)
		recordLifecycleProjection(metrics, result.Status)
		return result
	}
	if _, durable := lifecycle.projectionRuns.Load(projection.ProjectionID); !durable {
		result := transitionLifecycleProjection(projection, langfuse.ScoreProjectionStatusFailedPermanent)
		recordLifecycleProjection(metrics, result.Status)
		return result
	}

	result := worker.Enqueue(projection.Snapshot())
	if result.Status == langfuse.ScoreProjectionStatusQueued {
		recordLifecycleProjection(metrics, result.Status)
	}
	recordLifecycleQueue(metrics, worker.QueueDepth())
	return result
}

// EnqueueForRun binds a platform projection to an already durable evidence record.
// The initial queued snapshot is persisted before queue admission, so a process crash
// cannot leave an unindexed external score side effect.
func (lifecycle *LangfuseScoreLifecycle) EnqueueForRun(ctx context.Context, runID string, projection langfuse.ScoreProjection) (langfuse.ScoreProjection, error) {
	if lifecycle == nil || ctx == nil || ctx.Err() != nil || runID == "" {
		return projection.Snapshot(), ErrLangfuseEvidenceNotPersisted
	}
	if !lifecycle.hasUniqueEvidence(ctx, projection) {
		return projection.Snapshot(), ErrLangfuseEvidenceNotPersisted
	}
	if lifecycle.projectionStore == nil || lifecycle.projectionStore.SaveInitial(ctx, runID, projection.Snapshot(), lifecycle.maxAttempts) != nil {
		return projection.Snapshot(), ErrLangfuseProjectionPersistence
	}
	lifecycle.projectionRuns.Store(projection.ProjectionID, runID)
	result := lifecycle.Enqueue(projection.Snapshot())
	if result.Status != langfuse.ScoreProjectionStatusQueued {
		if err := lifecycle.projectionStore.Update(ctx, runID, result.Snapshot()); err != nil {
			return result, ErrLangfuseProjectionPersistence
		}
		lifecycle.projectionRuns.Delete(projection.ProjectionID)
	}
	return result, nil
}

func (lifecycle *LangfuseScoreLifecycle) hasUniqueEvidence(ctx context.Context, projection langfuse.ScoreProjection) bool {
	if lifecycle.evidenceStore == nil {
		return false
	}
	records, err := lifecycle.evidenceStore.Find(ctx, projection.Evidence.EvalRunID)
	return err == nil && len(records) == 1 && reflect.DeepEqual(records[0], projection.Evidence)
}

func (lifecycle *LangfuseScoreLifecycle) recoverPending(ctx context.Context) error {
	if lifecycle.projectionStore == nil {
		return nil
	}
	stored, err := lifecycle.projectionStore.LoadPending(ctx)
	if err != nil {
		return err
	}
	for _, record := range stored {
		recoveryStatus := record.Snapshot.Status
		if recoveryStatus == langfuse.ScoreProjectionStatusSending {
			// A crash leaves delivery outcome unknown. Stable ProjectionID makes replay
			// idempotent; model it as retry_wait so the public state machine can return
			// to queued without inventing a new transition edge.
			recoveryStatus = langfuse.ScoreProjectionStatusRetryWait
		}
		projection, recoverErr := langfuse.RecoverScoreProjection(langfuse.ScoreProjectionRecoveryInput{
			ProjectionID: record.Snapshot.ProjectionID, TargetKind: record.Snapshot.TargetKind,
			PlatformTraceID: record.Snapshot.PlatformTraceID, PlatformObservationID: record.Snapshot.PlatformObservationID,
			Evidence: record.Snapshot.Evidence, Status: recoveryStatus, Attempt: record.Snapshot.Attempt,
			CreatedAt: record.Snapshot.CreatedAt, MaxAttempts: record.Snapshot.MaxAttempts,
		})
		if recoverErr != nil {
			return recoverErr
		}
		queued := projection.Snapshot()
		var transitionErr error
		if queued.Status != langfuse.ScoreProjectionStatusQueued {
			queued, transitionErr = projection.Transition(langfuse.ScoreProjectionStatusQueued)
		}
		if transitionErr != nil {
			return ErrLangfuseProjectionPersistence
		}
		// queued already represents a durable SaveInitial snapshot. Rewriting it as
		// queued->queued is neither a domain transition nor needed before admission.
		if record.Snapshot.Status != langfuse.ScoreProjectionStatusQueued && lifecycle.projectionStore.Update(ctx, record.RunID, queued) != nil {
			return ErrLangfuseProjectionPersistence
		}
		lifecycle.projectionRuns.Store(queued.ProjectionID, record.RunID)
		if admitted := lifecycle.worker.Enqueue(queued); admitted.Status != langfuse.ScoreProjectionStatusQueued {
			if lifecycle.projectionStore.Update(ctx, record.RunID, admitted) != nil {
				return ErrLangfuseProjectionPersistence
			}
			lifecycle.projectionRuns.Delete(queued.ProjectionID)
		}
	}
	return nil
}

func isTerminalScoreProjectionStatus(status langfuse.ScoreProjectionStatus) bool {
	switch status {
	case langfuse.ScoreProjectionStatusSent,
		langfuse.ScoreProjectionStatusDroppedQueueFull,
		langfuse.ScoreProjectionStatusFailedPermanent,
		langfuse.ScoreProjectionStatusFailedShutdownTimeout,
		langfuse.ScoreProjectionStatusNotConfigured:
		return true
	default:
		return false
	}
}

func (lifecycle *LangfuseScoreLifecycle) Shutdown(ctx context.Context) error {
	if lifecycle == nil {
		return nil
	}
	if ctx == nil {
		return context.Canceled
	}
	lifecycle.shutdownOnce.Do(func() {
		if lifecycle.worker != nil {
			lifecycle.shutdownErr = lifecycle.worker.Shutdown(ctx)
			recordLifecycleQueue(lifecycle.metrics, lifecycle.worker.QueueDepth())
		}
		lifecycle.mu.Lock()
		lifecycle.status.State = LangfuseScoreLifecycleStateShutdown
		lifecycle.status.Shutdown = true
		lifecycle.mu.Unlock()
		if lifecycle.ownerState != nil {
			lifecycle.ownerState.mu.Lock()
			if lifecycle.ownerState.lifecycle == lifecycle {
				lifecycle.ownerState.lifecycle = nil
			}
			lifecycle.ownerState.mu.Unlock()
		}
	})
	return lifecycle.shutdownErr
}

func (lifecycle *LangfuseScoreLifecycle) Status() LangfuseScoreLifecycleStatus {
	if lifecycle == nil {
		return LangfuseScoreLifecycleStatus{State: LangfuseScoreLifecycleStateNotConfigured}
	}
	lifecycle.mu.Lock()
	status, worker := lifecycle.status, lifecycle.worker
	lifecycle.mu.Unlock()
	if worker != nil {
		status.QueueDepth = worker.QueueDepth()
	}
	return status
}

func defaultLangfuseScoreLifecycleDependencies(dependencies LangfuseScoreLifecycleDependencies) LangfuseScoreLifecycleDependencies {
	if dependencies.NewClient == nil {
		dependencies.NewClient = func(config langfuse.ScoreClientConfig) (langfuse.ScoreSender, error) {
			return langfuse.NewScoreClient(config)
		}
	}
	if dependencies.NewWorker == nil {
		dependencies.NewWorker = func(config langfuse.ScoreWorkerConfig) (LangfuseScoreWorker, error) {
			return langfuse.NewScoreWorker(config)
		}
	}
	if dependencies.state == nil {
		dependencies.state = &processLangfuseScoreLifecycleState
	}
	return dependencies
}

func isEmptyLangfuseScoreLifecycleInput(input LangfuseScoreLifecycleInput) bool {
	return input == (LangfuseScoreLifecycleInput{})
}

func isCompleteLangfuseScoreLifecycleInput(input LangfuseScoreLifecycleInput) bool {
	return input.BaseURL != "" && strings.TrimSpace(input.BaseURL) == input.BaseURL &&
		input.PublicKey != "" && strings.TrimSpace(input.PublicKey) == input.PublicKey &&
		input.SecretKey != "" && strings.TrimSpace(input.SecretKey) == input.SecretKey &&
		input.QueueCapacity > 0 && input.QueueCapacity <= maxLangfuseScoreQueueCapacity &&
		input.MaxAttempts > 0 && input.MaxAttempts <= maxLangfuseScoreAttempts &&
		input.InitialBackoff > 0 && input.MaxBackoff >= input.InitialBackoff &&
		input.MaxBackoff <= maxLangfuseScoreBackoff &&
		input.RequestTimeout > 0 && input.RequestTimeout <= maxLangfuseScoreTimeout
}

func transitionLifecycleProjection(
	projection langfuse.ScoreProjection,
	status langfuse.ScoreProjectionStatus,
) langfuse.ScoreProjection {
	updated, err := projection.Snapshot().Transition(status)
	if err != nil {
		return projection.Snapshot()
	}
	return updated
}

func scoreTransitionRecorder(
	metrics LangfuseScoreMetrics,
	queueDepth func() int,
) func(langfuse.ScoreWorkerTransition) {
	if metrics == nil {
		return nil
	}
	return func(transition langfuse.ScoreWorkerTransition) {
		status, ok := coarseScoreProjectionStatus(transition.Status)
		if ok {
			_ = metrics.RecordScoreProjection(context.Background(), appobservability.ScoreProjectionMetric{
				Backend: langfuseScoreBackend, Status: status,
			})
		}
		if queueDepth != nil {
			recordLifecycleQueue(metrics, queueDepth())
		}
	}
}

func coarseScoreProjectionStatus(status langfuse.ScoreProjectionStatus) (string, bool) {
	switch status {
	case langfuse.ScoreProjectionStatusQueued:
		return "queued", true
	case langfuse.ScoreProjectionStatusSent:
		return "sent", true
	case langfuse.ScoreProjectionStatusDroppedQueueFull:
		return "dropped", true
	case langfuse.ScoreProjectionStatusFailedPermanent, langfuse.ScoreProjectionStatusFailedShutdownTimeout:
		return "failed", true
	default:
		return "", false
	}
}

func recordLifecycleProjection(metrics LangfuseScoreMetrics, status langfuse.ScoreProjectionStatus) {
	if metrics == nil {
		return
	}
	metricStatus := string(status)
	if coarse, ok := coarseScoreProjectionStatus(status); ok {
		metricStatus = coarse
	}
	_ = metrics.RecordScoreProjection(context.Background(), appobservability.ScoreProjectionMetric{
		Backend: langfuseScoreBackend, Status: metricStatus,
	})
}

func recordLifecycleQueue(metrics LangfuseScoreMetrics, depth int) {
	if metrics == nil || depth < 0 {
		return
	}
	_ = metrics.RecordScoreQueue(context.Background(), appobservability.ScoreQueueMetric{
		Backend: langfuseScoreBackend, Depth: int64(depth),
	})
}
