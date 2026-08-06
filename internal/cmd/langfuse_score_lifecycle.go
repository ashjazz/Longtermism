package cmd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
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

var errInvalidLangfuseScoreLifecycle = errors.New("langfuse score lifecycle configuration is invalid")

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
	NewClient func(langfuse.ScoreClientConfig) (langfuse.ScoreSender, error)
	NewWorker func(langfuse.ScoreWorkerConfig) (LangfuseScoreWorker, error)
	Metrics   LangfuseScoreMetrics
	state     *langfuseScoreLifecycleState
}

type LangfuseScoreLifecycleStatus struct {
	State         LangfuseScoreLifecycleState
	Started       bool
	Shutdown      bool
	QueueCapacity int
	QueueDepth    int
}

type LangfuseScoreLifecycle struct {
	mu           sync.Mutex
	worker       LangfuseScoreWorker
	metrics      LangfuseScoreMetrics
	status       LangfuseScoreLifecycleStatus
	shutdownOnce sync.Once
	shutdownErr  error
}

type langfuseScoreLifecycleState struct {
	mu        sync.Mutex
	lifecycle *LangfuseScoreLifecycle
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
	return lifecycle, nil
}

func newLangfuseScoreLifecycle(
	ctx context.Context,
	input LangfuseScoreLifecycleInput,
	dependencies LangfuseScoreLifecycleDependencies,
) (*LangfuseScoreLifecycle, error) {
	if isEmptyLangfuseScoreLifecycleInput(input) {
		return &LangfuseScoreLifecycle{
			metrics: dependencies.Metrics,
			status:  LangfuseScoreLifecycleStatus{State: LangfuseScoreLifecycleStateNotConfigured},
		}, nil
	}
	if !isCompleteLangfuseScoreLifecycleInput(input) {
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
	transitionRecorder := scoreTransitionRecorder(dependencies.Metrics, func() int {
		if worker == nil {
			return 0
		}
		return worker.QueueDepth()
	})
	worker, err = dependencies.NewWorker(langfuse.ScoreWorkerConfig{
		QueueCapacity:  input.QueueCapacity,
		MaxAttempts:    input.MaxAttempts,
		InitialBackoff: input.InitialBackoff,
		MaxBackoff:     input.MaxBackoff,
		Sender:         sender,
		OnTransition:   transitionRecorder,
	})
	if err != nil || worker == nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	if ctx.Err() != nil {
		return nil, errInvalidLangfuseScoreLifecycle
	}
	worker.Start()
	return &LangfuseScoreLifecycle{
		worker:  worker,
		metrics: dependencies.Metrics,
		status: LangfuseScoreLifecycleStatus{
			State: LangfuseScoreLifecycleStateRunning, Started: true, QueueCapacity: input.QueueCapacity,
		},
	}, nil
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

	result := worker.Enqueue(projection.Snapshot())
	if result.Status == langfuse.ScoreProjectionStatusQueued {
		recordLifecycleProjection(metrics, result.Status)
	}
	recordLifecycleQueue(metrics, worker.QueueDepth())
	return result
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
