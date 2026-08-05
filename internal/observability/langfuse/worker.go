package langfuse

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	errInvalidScoreWorkerConfig = errors.New("langfuse score worker configuration is invalid")
	errScoreWorkerDependency    = errors.New("langfuse score worker dependency failed")
)

const (
	maxScoreWorkerQueueCapacity = 4096
	scoreWorkerDiagnosticBuffer = 256
)

// ScoreRetryWaiter makes retry timing replaceable in deterministic tests while
// production uses a context-aware timer rather than blocking sleeps. Implementations
// must honor ctx so worker shutdown retains a real deadline and Done semantic.
type ScoreRetryWaiter interface {
	Wait(context.Context, time.Duration) error
}

// ScoreWorkerTransition is deliberately low-cardinality and low-sensitivity.
// Platform and evidence identities belong in local evidence, never metrics/logs.
type ScoreWorkerTransition struct {
	Status  ScoreProjectionStatus `json:"status"`
	Attempt int                   `json:"attempt"`
}

type ScoreWorkerConfig struct {
	QueueCapacity  int
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Sender         ScoreSender
	Waiter         ScoreRetryWaiter
	// OnTransition must return quickly and must not perform blocking I/O. Events
	// are best-effort through a bounded channel so diagnostics never backpressure chat.
	OnTransition func(ScoreWorkerTransition)
}

type ScoreWorker struct {
	queue          chan ScoreProjection
	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	sender         ScoreSender
	waiter         ScoreRetryWaiter
	onTransition   func(ScoreWorkerTransition)

	runCtx      context.Context
	cancelRun   context.CancelFunc
	stop        chan struct{}
	done        chan struct{}
	diagnostics chan ScoreWorkerTransition
	startOnce   sync.Once
	stopOnce    sync.Once

	mu              sync.Mutex
	accepting       bool
	stopping        bool
	diagnosticsOpen bool
}

func NewScoreWorker(config ScoreWorkerConfig) (*ScoreWorker, error) {
	if !isValidScoreWorkerConfig(config) {
		return nil, errInvalidScoreWorkerConfig
	}
	waiter := config.Waiter
	if waiter == nil {
		waiter = timerScoreRetryWaiter{}
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	return &ScoreWorker{
		queue:           make(chan ScoreProjection, config.QueueCapacity),
		maxAttempts:     config.MaxAttempts,
		initialBackoff:  config.InitialBackoff,
		maxBackoff:      config.MaxBackoff,
		sender:          config.Sender,
		waiter:          waiter,
		onTransition:    config.OnTransition,
		runCtx:          runCtx,
		cancelRun:       cancelRun,
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
		diagnostics:     make(chan ScoreWorkerTransition, scoreWorkerDiagnosticBuffer),
		accepting:       true,
		diagnosticsOpen: true,
	}, nil
}

func isValidScoreWorkerConfig(config ScoreWorkerConfig) bool {
	return config.QueueCapacity > 0 && config.QueueCapacity <= maxScoreWorkerQueueCapacity &&
		config.MaxAttempts > 0 && config.MaxAttempts <= maxScoreProjectionAttempts &&
		config.InitialBackoff > 0 && config.MaxBackoff >= config.InitialBackoff &&
		config.Sender != nil
}

func (worker *ScoreWorker) Start() {
	if worker == nil {
		return
	}
	worker.startOnce.Do(func() {
		go worker.consumeDiagnostics()
		go worker.run()
	})
}

// Enqueue owns only a bounded memory slot and never waits for HTTP delivery.
// The mutex makes lifecycle admission and channel send one atomic decision;
// the channel is never closed, avoiding send-on-closed races during shutdown.
func (worker *ScoreWorker) Enqueue(projection ScoreProjection) ScoreProjection {
	snapshot := projection.Snapshot()
	snapshot.maxAttempts = worker.maxAttempts
	if !isValidQueuedWorkerProjection(snapshot) {
		return rejectWorkerProjection(snapshot)
	}

	worker.mu.Lock()
	if !worker.accepting {
		worker.mu.Unlock()
		terminated := terminateWorkerProjection(snapshot, ScoreProjectionStatusFailedShutdownTimeout)
		worker.record(terminated)
		return terminated
	}
	select {
	case worker.queue <- snapshot:
		worker.mu.Unlock()
		return snapshot
	default:
		worker.mu.Unlock()
		terminated := terminateWorkerProjection(snapshot, ScoreProjectionStatusDroppedQueueFull)
		worker.record(terminated)
		return terminated
	}
}

func (worker *ScoreWorker) QueueDepth() int {
	if worker == nil {
		return 0
	}
	return len(worker.queue)
}

func (worker *ScoreWorker) QueueCapacity() int {
	if worker == nil {
		return 0
	}
	return cap(worker.queue)
}

func (worker *ScoreWorker) Done() <-chan struct{} {
	if worker == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return worker.done
}

func (worker *ScoreWorker) Shutdown(ctx context.Context) error {
	if worker == nil || ctx == nil {
		return context.Canceled
	}
	worker.stopOnce.Do(func() {
		worker.mu.Lock()
		worker.accepting = false
		worker.stopping = true
		close(worker.stop)
		worker.mu.Unlock()
		worker.Start()
	})

	select {
	case <-worker.done:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		worker.cancelRun()
		<-worker.done
		return ctx.Err()
	}
}

func (worker *ScoreWorker) run() {
	defer close(worker.done)
	defer worker.closeDiagnostics()
	defer worker.cancelRun()
	for {
		select {
		case projection := <-worker.queue:
			worker.deliver(projection)
		case <-worker.stop:
			worker.drain()
			return
		}
	}
}

func (worker *ScoreWorker) drain() {
	for {
		select {
		case projection := <-worker.queue:
			worker.deliver(projection)
		default:
			return
		}
	}
}

func (worker *ScoreWorker) deliver(projection ScoreProjection) {
	current := projection.Snapshot()
	for {
		if worker.runCtx.Err() != nil {
			worker.recordTransition(current, ScoreProjectionStatusFailedShutdownTimeout)
			return
		}
		sending, ok := worker.recordTransition(current, ScoreProjectionStatusSending)
		if !ok {
			return
		}
		err := worker.callSender(sending.Snapshot())
		if err == nil {
			worker.recordTransition(sending, ScoreProjectionStatusSent)
			return
		}
		if worker.runCtx.Err() != nil && worker.isStopping() {
			worker.recordTransition(sending, ScoreProjectionStatusFailedShutdownTimeout)
			return
		}
		if !isRetryableScoreError(err) {
			worker.recordTransition(sending, ScoreProjectionStatusFailedPermanent)
			return
		}

		retryWait, transitionErr := sending.Transition(ScoreProjectionStatusRetryWait)
		if transitionErr != nil {
			worker.recordTransition(sending, ScoreProjectionStatusFailedPermanent)
			return
		}
		worker.record(retryWait)
		if err := worker.callWaiter(worker.retryDelay(retryWait.Attempt)); err != nil {
			if worker.runCtx.Err() != nil {
				worker.recordTransition(retryWait, ScoreProjectionStatusFailedShutdownTimeout)
			} else {
				queued, transitionErr := retryWait.Transition(ScoreProjectionStatusQueued)
				if transitionErr == nil {
					worker.recordTransition(queued, ScoreProjectionStatusFailedPermanent)
				}
			}
			return
		}
		current, ok = worker.recordTransition(retryWait, ScoreProjectionStatusQueued)
		if !ok {
			return
		}
	}
}

func (worker *ScoreWorker) callSender(projection ScoreProjection) (err error) {
	defer func() {
		if recover() != nil {
			err = errScoreWorkerDependency
		}
	}()
	return worker.sender.Create(worker.runCtx, projection)
}

func (worker *ScoreWorker) callWaiter(delay time.Duration) (err error) {
	defer func() {
		if recover() != nil {
			err = errScoreWorkerDependency
		}
	}()
	return worker.waiter.Wait(worker.runCtx, delay)
}

func isRetryableScoreError(err error) bool {
	return errors.Is(err, ErrScoreTimeout) || errors.Is(err, ErrScoreRateLimited) || errors.Is(err, ErrScoreUpstream)
}

func (worker *ScoreWorker) retryDelay(attempt int) time.Duration {
	delay := worker.initialBackoff
	for retry := 1; retry < attempt && delay < worker.maxBackoff; retry++ {
		if delay > worker.maxBackoff/2 {
			return worker.maxBackoff
		}
		delay *= 2
	}
	if delay > worker.maxBackoff {
		return worker.maxBackoff
	}
	return delay
}

func (worker *ScoreWorker) recordTransition(projection ScoreProjection, status ScoreProjectionStatus) (ScoreProjection, bool) {
	updated, err := projection.Transition(status)
	if err != nil {
		return projection.Snapshot(), false
	}
	worker.record(updated)
	return updated, true
}

func (worker *ScoreWorker) record(projection ScoreProjection) {
	if worker.onTransition == nil {
		return
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if !worker.diagnosticsOpen {
		return
	}
	select {
	case worker.diagnostics <- ScoreWorkerTransition{Status: projection.Status, Attempt: projection.Attempt}:
	default:
	}
}

func (worker *ScoreWorker) closeDiagnostics() {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.diagnosticsOpen {
		worker.diagnosticsOpen = false
		close(worker.diagnostics)
	}
}

func (worker *ScoreWorker) consumeDiagnostics() {
	for diagnostic := range worker.diagnostics {
		func() {
			defer func() { _ = recover() }()
			worker.onTransition(diagnostic)
		}()
	}
}

func isValidQueuedWorkerProjection(projection ScoreProjection) bool {
	if projection.Status != ScoreProjectionStatusQueued || projection.Attempt != 0 || !isValidScoreRequestProjection(projection) {
		return false
	}
	facts := []string{
		projection.Evidence.EvalRunID, projection.Evidence.RequestID, projection.Evidence.AITraceID,
		projection.Evidence.ServiceTraceID, projection.Evidence.SpanID, projection.Evidence.Dataset.Name,
		projection.Evidence.Dataset.Version, projection.Evidence.SampleID, projection.Evidence.MetricName,
		projection.Evidence.FailureSummary,
	}
	for _, fact := range facts {
		if len(fact) > maxProjectionFactBytes {
			return false
		}
	}
	return true
}

func rejectWorkerProjection(projection ScoreProjection) ScoreProjection {
	return terminateWorkerProjection(projection, ScoreProjectionStatusFailedPermanent)
}

func terminateWorkerProjection(projection ScoreProjection, status ScoreProjectionStatus) ScoreProjection {
	terminated := projection.Snapshot()
	terminated.Status = status
	return terminated
}

func (worker *ScoreWorker) isStopping() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.stopping
}

type timerScoreRetryWaiter struct{}

func (timerScoreRetryWaiter) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
