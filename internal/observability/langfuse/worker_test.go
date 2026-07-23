package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

func TestScoreWorkerEnqueueIsNonBlocking(t *testing.T) {
	sender := newT080Sender()
	transitions := newT080TransitionRecorder()
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    1,
		InitialBackoff: time.Second,
		MaxBackoff:     3 * time.Second,
		Sender:         sender,
		Waiter:         newT080RetryWaiter(),
		OnTransition:   transitions.Record,
	})
	worker.Start()

	projection := mustNewT078Projection(t)
	result := make(chan ScoreProjection, 1)
	go func() { result <- worker.Enqueue(projection) }()
	if got := receiveT080(t, result); got.Status != ScoreProjectionStatusQueued {
		t.Fatalf("Enqueue() = %#v, want immediate queued projection", got)
	}
	// sender 被 gate 阻塞仍不能反压 chat 调用方；真正 HTTP 投递必须在后台发生。
	if got := receiveT080(t, sender.started); got.ProjectionID != projection.ProjectionID {
		t.Fatalf("sender projection = %#v, want queued projection %q", got, projection.ProjectionID)
	}
	sender.results <- nil
	assertT080TransitionStatus(t, transitions, ScoreProjectionStatusSent)
	shutdownT080Worker(t, worker)
}

func TestScoreWorkerEnqueueDropsWhenQueueIsFullWithoutChangingEvidence(t *testing.T) {
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    1,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
		Sender:         newT080Sender(),
		Waiter:         newT080RetryWaiter(),
	})
	projection := mustNewT078Projection(t)
	wantEvidence := cloneT078Evidence(projection.Evidence)

	first := worker.Enqueue(projection)
	second := worker.Enqueue(projection)
	if first.Status != ScoreProjectionStatusQueued || second.Status != ScoreProjectionStatusDroppedQueueFull {
		t.Fatalf("enqueue statuses = %q, %q; want queued, dropped_queue_full", first.Status, second.Status)
	}
	if !reflect.DeepEqual(projection.Evidence, wantEvidence) || !reflect.DeepEqual(second.Evidence, wantEvidence) {
		t.Fatal("queue-full handling must not modify or discard the local evidence snapshot")
	}
}

func TestScoreWorkerRetriesWithExponentialBackoffAndStableProjectionIdentity(t *testing.T) {
	sender := newT080Sender()
	waiter := newT080RetryWaiter()
	transitions := newT080TransitionRecorder()
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    3,
		InitialBackoff: time.Second,
		MaxBackoff:     3 * time.Second,
		Sender:         sender,
		Waiter:         waiter,
		OnTransition:   transitions.Record,
	})
	worker.Start()
	projection := mustNewT078Projection(t)
	wantEvidence := cloneT078Evidence(projection.Evidence)
	if got := worker.Enqueue(projection); got.Status != ScoreProjectionStatusQueued {
		t.Fatalf("Enqueue() status = %q, want queued", got.Status)
	}

	first := receiveT080(t, sender.started)
	sender.results <- ErrScoreUpstream
	if delay := receiveT080(t, waiter.started); delay != time.Second {
		t.Fatalf("first retry delay = %s, want 1s", delay)
	}
	waiter.release <- struct{}{}
	second := receiveT080(t, sender.started)
	sender.results <- ErrScoreUpstream
	if delay := receiveT080(t, waiter.started); delay != 2*time.Second {
		t.Fatalf("second retry delay = %s, want 2s", delay)
	}
	waiter.release <- struct{}{}
	third := receiveT080(t, sender.started)
	sender.results <- nil
	assertT080TransitionStatus(t, transitions, ScoreProjectionStatusSent)

	for attempt, got := range []ScoreProjection{first, second, third} {
		if got.ProjectionID != projection.ProjectionID || !got.CreatedAt.Equal(projection.CreatedAt) || got.Target != projection.Target || !reflect.DeepEqual(got.Evidence, wantEvidence) {
			t.Fatalf("retry %d projection = %#v, want the same id/target/timestamp/evidence", attempt+1, got)
		}
	}
	gotStatuses := transitions.Statuses()
	wantStatuses := []ScoreProjectionStatus{
		ScoreProjectionStatusSending,
		ScoreProjectionStatusRetryWait,
		ScoreProjectionStatusQueued,
		ScoreProjectionStatusSending,
		ScoreProjectionStatusRetryWait,
		ScoreProjectionStatusQueued,
		ScoreProjectionStatusSending,
		ScoreProjectionStatusSent,
	}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("transitions = %#v, want %#v", gotStatuses, wantStatuses)
	}
	shutdownT080Worker(t, worker)
}

func TestScoreWorkerMarksPermanentFailureWithoutRetryOrEvidenceMutation(t *testing.T) {
	sender := newT080Sender()
	waiter := newT080RetryWaiter()
	transitions := newT080TransitionRecorder()
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    2,
		InitialBackoff: time.Second,
		MaxBackoff:     2 * time.Second,
		Sender:         sender,
		Waiter:         waiter,
		OnTransition:   transitions.Record,
	})
	worker.Start()
	projection := mustNewT078Projection(t)
	wantEvidence := cloneT078Evidence(projection.Evidence)
	worker.Enqueue(projection)
	_ = receiveT080(t, sender.started)
	sender.results <- ErrScoreRejected
	_ = assertT080TransitionStatus(t, transitions, ScoreProjectionStatusFailedPermanent)
	received := sender.Received()
	if len(received) != 1 || !reflect.DeepEqual(received[0].Evidence, wantEvidence) || !reflect.DeepEqual(projection.Evidence, wantEvidence) {
		t.Fatal("permanent score failure must leave local evidence intact")
	}
	select {
	case delay := <-waiter.started:
		t.Fatalf("permanent failure scheduled retry delay %s", delay)
	default:
	}
	shutdownT080Worker(t, worker)
}

func TestScoreWorkerStopsRetryingWhenAttemptBudgetIsExhausted(t *testing.T) {
	sender := newT080Sender()
	waiter := newT080RetryWaiter()
	transitions := newT080TransitionRecorder()
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    1,
		InitialBackoff: time.Second,
		MaxBackoff:     2 * time.Second,
		Sender:         sender,
		Waiter:         waiter,
		OnTransition:   transitions.Record,
	})
	worker.Start()
	projection := mustNewT078Projection(t)
	wantEvidence := cloneT078Evidence(projection.Evidence)
	worker.Enqueue(projection)
	_ = receiveT080(t, sender.started)
	sender.results <- ErrScoreUpstream
	if delay := receiveT080(t, waiter.started); delay != time.Second {
		t.Fatalf("retry delay = %s, want 1s", delay)
	}
	waiter.release <- struct{}{}
	_ = receiveT080(t, sender.started)
	sender.results <- ErrScoreUpstream
	_ = assertT080TransitionStatus(t, transitions, ScoreProjectionStatusFailedPermanent)
	for _, sent := range sender.Received() {
		if sent.ProjectionID != projection.ProjectionID || !reflect.DeepEqual(sent.Evidence, wantEvidence) {
			t.Fatalf("exhausted retry send = %#v, want immutable projection evidence", sent)
		}
	}
	select {
	case extra := <-sender.started:
		t.Fatalf("attempt budget exhaustion sent an unexpected third projection %#v", extra)
	case delay := <-waiter.started:
		t.Fatalf("attempt budget exhaustion scheduled an unexpected retry %s", delay)
	default:
	}
	shutdownT080Worker(t, worker)
}

func TestScoreWorkerShutdownMarksUndrainedProjectionsWithoutDeletingEvidence(t *testing.T) {
	sender := newT080Sender()
	transitions := newT080TransitionRecorder()
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    2,
		InitialBackoff: time.Second,
		MaxBackoff:     2 * time.Second,
		Sender:         sender,
		Waiter:         newT080RetryWaiter(),
		OnTransition:   transitions.Record,
	})
	worker.Start()
	first := mustNewT078Projection(t)
	second := mustNewT080Projection(t, "sample-t080-undrained")
	firstEvidence := cloneT078Evidence(first.Evidence)
	secondEvidence := cloneT078Evidence(second.Evidence)
	worker.Enqueue(first)
	_ = receiveT080(t, sender.started)
	worker.Enqueue(second)

	shutdownContext, cancel := context.WithCancel(context.Background())
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- worker.Shutdown(shutdownContext) }()
	cancel()
	if err := receiveT080(t, shutdownDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown() error = %v, want context cancellation", err)
	}
	_ = receiveT080(t, sender.canceled)
	failed := transitions.ByStatus(ScoreProjectionStatusFailedShutdownTimeout)
	if len(failed) != 2 || !reflect.DeepEqual(first.Evidence, firstEvidence) || !reflect.DeepEqual(second.Evidence, secondEvidence) {
		t.Fatalf("shutdown failures = %#v, want both immutable local evidence snapshots", failed)
	}
	if len(sender.Received()) != 1 {
		t.Fatalf("sender calls = %d, want no new send after shutdown begins", len(sender.Received()))
	}
	select {
	case <-worker.Done():
	default:
		t.Fatal("Shutdown() returned before the score worker goroutine completed")
	}
	// Done 已关闭后，释放旧 sender gate 也不可能让 worker 恢复消费第二条投影。
	sender.results <- nil
	select {
	case sent := <-sender.started:
		t.Fatalf("completed worker started an unexpected send %#v", sent)
	default:
	}
}

func TestScoreWorkerRejectsEnqueueAfterShutdownWithoutStartingAnotherSend(t *testing.T) {
	sender := newT080Sender()
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    1,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
		Sender:         sender,
		Waiter:         newT080RetryWaiter(),
	})
	worker.Start()
	shutdownT080Worker(t, worker)
	projection := mustNewT078Projection(t)
	wantEvidence := cloneT078Evidence(projection.Evidence)
	result := make(chan ScoreProjection, 1)
	go func() { result <- worker.Enqueue(projection) }()
	if got := receiveT080(t, result); got.Status != ScoreProjectionStatusFailedShutdownTimeout || !reflect.DeepEqual(got.Evidence, wantEvidence) {
		t.Fatalf("Enqueue() after shutdown = %#v, want nonblocking shutdown terminal with immutable evidence", got)
	}
	select {
	case sent := <-sender.started:
		t.Fatalf("shutdown worker started an unexpected send %#v", sent)
	default:
	}
}

func TestScoreWorkerConcurrentEnqueueAndShutdownNeverPanicsOrBlocks(t *testing.T) {
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  4,
		MaxAttempts:    1,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
		Sender:         newT080Sender(),
		Waiter:         newT080RetryWaiter(),
	})
	worker.Start()
	const callers = 32
	start := make(chan struct{})
	results := make(chan ScoreProjection, callers)
	var callersDone sync.WaitGroup
	for caller := range callers {
		projection := mustNewT080Projection(t, fmt.Sprintf("sample-t080-close-race-%03d", caller))
		callersDone.Add(1)
		go func() {
			defer callersDone.Done()
			<-start
			results <- worker.Enqueue(projection)
		}()
	}
	shutdownContext, cancel := context.WithCancel(context.Background())
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- worker.Shutdown(shutdownContext) }()
	close(start)
	cancel()
	joined := make(chan struct{})
	go func() {
		callersDone.Wait()
		close(joined)
	}()
	_ = receiveT080(t, joined)
	if err := receiveT080(t, shutdownDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown() error = %v, want context cancellation", err)
	}
	close(results)
	for result := range results {
		switch result.Status {
		case ScoreProjectionStatusQueued, ScoreProjectionStatusDroppedQueueFull, ScoreProjectionStatusFailedShutdownTimeout:
		default:
			t.Fatalf("concurrent close Enqueue() status = %q, want queued/dropped/shutdown terminal", result.Status)
		}
	}
}

func TestScoreWorkerTransitionDiagnosticsNeverExposeEvidence(t *testing.T) {
	sender := newT080Sender()
	transitions := newT080TransitionRecorder()
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  1,
		MaxAttempts:    1,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
		Sender:         sender,
		Waiter:         newT080RetryWaiter(),
		OnTransition:   transitions.Record,
	})
	worker.Start()
	projection := mustNewT078Projection(t)
	projection.Evidence.FailureSummary = "raw-t080-evidence Authorization: Bearer secret"
	worker.Enqueue(projection)
	_ = receiveT080(t, sender.started)
	sender.results <- ErrScoreRejected
	diagnostic := assertT080TransitionStatus(t, transitions, ScoreProjectionStatusFailedPermanent)
	serialized, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatalf("marshal transition diagnostic: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(serialized, &fields); err != nil || len(fields) != 2 || fields["status"] == nil || fields["attempt"] == nil {
		t.Fatalf("transition diagnostic fields = %#v err=%v, want only status and attempt", fields, err)
	}
	for _, forbidden := range []string{projection.ProjectionID, projection.Evidence.RequestID, projection.Evidence.AITraceID, projection.Evidence.FailureSummary, "Authorization: Bearer secret"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("transition diagnostic leaked %q: %s", forbidden, serialized)
		}
	}
	shutdownT080Worker(t, worker)
}

func TestScoreWorkerConcurrentEnqueueIsRaceSafeAndConservesEveryProjection(t *testing.T) {
	worker := newT080Worker(t, ScoreWorkerConfig{
		QueueCapacity:  32,
		MaxAttempts:    1,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
		Sender:         newT080Sender(),
		Waiter:         newT080RetryWaiter(),
	})
	const callers = 128
	start := make(chan struct{})
	results := make(chan ScoreProjection, callers)
	var callersDone sync.WaitGroup
	projections := make([]ScoreProjection, callers)
	wantEvidenceByProjectionID := make(map[string]aieval.EvaluationEvidence, callers)
	for caller := range callers {
		projections[caller] = mustNewT080Projection(t, fmt.Sprintf("sample-t080-concurrent-%03d", caller))
		wantEvidenceByProjectionID[projections[caller].ProjectionID] = cloneT078Evidence(projections[caller].Evidence)
	}
	for caller := range callers {
		callersDone.Add(1)
		go func(caller int) {
			defer callersDone.Done()
			<-start
			results <- worker.Enqueue(projections[caller])
		}(caller)
	}
	close(start)
	callersDone.Wait()
	close(results)

	queued, dropped := 0, 0
	seen := make(map[string]struct{}, callers)
	for result := range results {
		switch result.Status {
		case ScoreProjectionStatusQueued:
			queued++
		case ScoreProjectionStatusDroppedQueueFull:
			dropped++
		default:
			t.Fatalf("concurrent Enqueue() status = %q, want queued or dropped_queue_full", result.Status)
		}
		wantEvidence, exists := wantEvidenceByProjectionID[result.ProjectionID]
		if !exists || !reflect.DeepEqual(result.Evidence, wantEvidence) {
			t.Fatalf("concurrent Enqueue() result %#v, want one projection ID per immutable evidence snapshot", result)
		}
		if _, duplicate := seen[result.ProjectionID]; duplicate {
			t.Fatalf("concurrent Enqueue() duplicated projection ID %q", result.ProjectionID)
		}
		seen[result.ProjectionID] = struct{}{}
	}
	if queued+dropped != callers || queued > 32 || len(seen) != callers {
		t.Fatalf("queued=%d dropped=%d, want exactly %d results and at most queue capacity", queued, dropped, callers)
	}
}

func mustNewT080Projection(t *testing.T, sampleID string) ScoreProjection {
	t.Helper()
	evidence := newT078Evidence(t, "answer_relevance")
	evidence.SampleID = sampleID
	projection, err := NewScoreProjection(newT078ProjectionInput(t, evidence, ScoreTargetKindObservation))
	if err != nil {
		t.Fatalf("NewScoreProjection() error = %v", err)
	}
	return projection
}

func newT080Worker(t *testing.T, config ScoreWorkerConfig) *ScoreWorker {
	t.Helper()
	worker, err := NewScoreWorker(config)
	if err != nil {
		t.Fatalf("NewScoreWorker() error = %v", err)
	}
	return worker
}

func shutdownT080Worker(t *testing.T, worker *ScoreWorker) {
	t.Helper()
	context, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Shutdown(context); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func receiveT080[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	context, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case value := <-values:
		return value
	case <-context.Done():
		t.Fatal("timed out waiting for deterministic worker signal")
		var zero T
		return zero
	}
}

func assertT080TransitionStatus(t *testing.T, recorder *t080TransitionRecorder, want ScoreProjectionStatus) ScoreWorkerTransition {
	t.Helper()
	for {
		transition := receiveT080(t, recorder.transitions)
		if transition.Status == want {
			return transition
		}
	}
}

type t080Sender struct {
	started  chan ScoreProjection
	results  chan error
	canceled chan ScoreProjection

	mu       sync.Mutex
	received []ScoreProjection
}

func newT080Sender() *t080Sender {
	return &t080Sender{started: make(chan ScoreProjection, 16), results: make(chan error, 16), canceled: make(chan ScoreProjection, 16)}
}

func (sender *t080Sender) Create(ctx context.Context, projection ScoreProjection) error {
	snapshot := cloneT078Projection(projection)
	sender.mu.Lock()
	sender.received = append(sender.received, snapshot)
	sender.mu.Unlock()
	select {
	case sender.started <- snapshot:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case result := <-sender.results:
		return result
	case <-ctx.Done():
		sender.canceled <- snapshot
		return ctx.Err()
	}
}

func (sender *t080Sender) Received() []ScoreProjection {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]ScoreProjection(nil), sender.received...)
}

type t080RetryWaiter struct {
	started chan time.Duration
	release chan struct{}
}

func newT080RetryWaiter() *t080RetryWaiter {
	return &t080RetryWaiter{started: make(chan time.Duration, 16), release: make(chan struct{}, 16)}
}

func (waiter *t080RetryWaiter) Wait(ctx context.Context, delay time.Duration) error {
	select {
	case waiter.started <- delay:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-waiter.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type t080TransitionRecorder struct {
	transitions chan ScoreWorkerTransition

	mu       sync.Mutex
	recorded []ScoreWorkerTransition
}

func newT080TransitionRecorder() *t080TransitionRecorder {
	return &t080TransitionRecorder{transitions: make(chan ScoreWorkerTransition, 32)}
}

func (recorder *t080TransitionRecorder) Record(transition ScoreWorkerTransition) {
	recorder.mu.Lock()
	recorder.recorded = append(recorder.recorded, transition)
	recorder.mu.Unlock()
	recorder.transitions <- transition
}

func (recorder *t080TransitionRecorder) Statuses() []ScoreProjectionStatus {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	statuses := make([]ScoreProjectionStatus, len(recorder.recorded))
	for index, transition := range recorder.recorded {
		statuses[index] = transition.Status
	}
	return statuses
}

func (recorder *t080TransitionRecorder) ByStatus(status ScoreProjectionStatus) []ScoreWorkerTransition {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var matches []ScoreWorkerTransition
	for _, transition := range recorder.recorded {
		if transition.Status == status {
			matches = append(matches, transition)
		}
	}
	return matches
}
