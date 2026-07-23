package cmd

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

const t081LangfuseSecret = "T081_LANGFUSE_SECRET_MUST_NOT_LEAK"

func TestBuildLangfuseScoreLifecycleLeavesEvidenceProjectionAvailableWhenUnconfigured(t *testing.T) {
	metrics := &t081MetricsRecorder{}
	dependencies := LangfuseScoreLifecycleDependencies{
		NewClient: func(langfuse.ScoreClientConfig) (langfuse.ScoreSender, error) {
			t.Fatal("unconfigured lifecycle must not construct a Langfuse client")
			return nil, nil
		},
		NewWorker: func(langfuse.ScoreWorkerConfig) (LangfuseScoreWorker, error) {
			t.Fatal("unconfigured lifecycle must not construct a score worker")
			return nil, nil
		},
		Metrics: metrics,
		state:   &langfuseScoreLifecycleState{},
	}

	lifecycle, err := BuildLangfuseScoreLifecycle(context.Background(), LangfuseScoreLifecycleInput{}, dependencies)
	if err != nil {
		t.Fatalf("BuildLangfuseScoreLifecycle() error = %v", err)
	}
	if status := lifecycle.Status(); status.State != LangfuseScoreLifecycleStateNotConfigured || status.Started || status.QueueCapacity != 0 || status.QueueDepth != 0 {
		t.Fatalf("unconfigured lifecycle status = %#v, want explicit inactive not_configured state", status)
	}

	projection := mustNewT081Projection(t)
	wantEvidence := cloneT081Evidence(projection.Evidence)
	result := lifecycle.Enqueue(projection)
	if result.Status != langfuse.ScoreProjectionStatusNotConfigured || !reflect.DeepEqual(result.Evidence, wantEvidence) {
		t.Fatalf("unconfigured Enqueue() = %#v, want not_configured with the immutable local evidence snapshot", result)
	}
	if got := metrics.projectionSnapshots(); len(got) != 1 || got[0].Backend != "langfuse" || got[0].Status != "not_configured" {
		t.Fatalf("unconfigured projection metrics = %#v, want one low-cardinality not_configured record", got)
	}
}

func TestBuildLangfuseScoreLifecycleStartsOneConfiguredWorker(t *testing.T) {
	worker := &t081Worker{}
	var (
		clientCalls  int
		workerCalls  int
		workerConfig langfuse.ScoreWorkerConfig
	)
	state := &langfuseScoreLifecycleState{}
	input := newT081LifecycleInput()
	dependencies := LangfuseScoreLifecycleDependencies{
		NewClient: func(config langfuse.ScoreClientConfig) (langfuse.ScoreSender, error) {
			clientCalls++
			if config.BaseURL != input.BaseURL || config.PublicKey != input.PublicKey || config.SecretKey != input.SecretKey || config.Timeout != input.RequestTimeout {
				t.Fatalf("score client config = base_url:%q public_key:%q secret_matches:%t timeout:%s, want configured endpoint, credentials and request budget", config.BaseURL, config.PublicKey, config.SecretKey == input.SecretKey, config.Timeout)
			}
			return t081Sender{}, nil
		},
		NewWorker: func(config langfuse.ScoreWorkerConfig) (LangfuseScoreWorker, error) {
			workerCalls++
			workerConfig = config
			return worker, nil
		},
		Metrics: &t081MetricsRecorder{},
		state:   state,
	}

	first, err := BuildLangfuseScoreLifecycle(context.Background(), input, dependencies)
	if err != nil {
		t.Fatalf("first BuildLangfuseScoreLifecycle() error = %v", err)
	}
	second, err := BuildLangfuseScoreLifecycle(context.Background(), input, dependencies)
	if err != nil {
		t.Fatalf("second BuildLangfuseScoreLifecycle() error = %v", err)
	}
	if first != second || clientCalls != 1 || workerCalls != 1 || worker.startCalls() != 1 {
		t.Fatalf("lifecycle reuse=%t client_calls=%d worker_calls=%d start_calls=%d, want one configured worker", first == second, clientCalls, workerCalls, worker.startCalls())
	}
	if workerConfig.QueueCapacity != input.QueueCapacity || workerConfig.MaxAttempts != input.MaxAttempts || workerConfig.InitialBackoff != input.InitialBackoff || workerConfig.MaxBackoff != input.MaxBackoff || workerConfig.Sender == nil {
		t.Fatalf("worker config = queue_capacity:%d max_attempts:%d initial_backoff:%s max_backoff:%s sender_present:%t, want the bounded queue and retry policy from command configuration", workerConfig.QueueCapacity, workerConfig.MaxAttempts, workerConfig.InitialBackoff, workerConfig.MaxBackoff, workerConfig.Sender != nil)
	}
	if status := first.Status(); status.State != LangfuseScoreLifecycleStateRunning || !status.Started || status.QueueCapacity != input.QueueCapacity {
		t.Fatalf("configured lifecycle status = %#v, want running bounded worker", status)
	}
}

func TestLangfuseScoreLifecycleShutdownFlushesTheWorkerOnce(t *testing.T) {
	worker := &t081Worker{}
	lifecycle := newT081ConfiguredLifecycle(t, worker, &t081MetricsRecorder{})

	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lifecycle.Shutdown(shutdownContext); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := lifecycle.Shutdown(shutdownContext); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
	if worker.shutdownCalls() != 1 || !worker.wasFlushed() {
		t.Fatalf("worker shutdown calls=%d flushed=%t, want one graceful worker flush", worker.shutdownCalls(), worker.wasFlushed())
	}
	if status := lifecycle.Status(); !status.Shutdown || status.State != LangfuseScoreLifecycleStateShutdown {
		t.Fatalf("shutdown lifecycle status = %#v, want shutdown state", status)
	}
}

func TestLangfuseScoreLifecycleRecordsBoundedQueueMetricsWithoutIdentityLabels(t *testing.T) {
	metrics := &t081MetricsRecorder{}
	worker := &t081Worker{queueDepth: 1}
	lifecycle := newT081ConfiguredLifecycle(t, worker, metrics)

	result := lifecycle.Enqueue(mustNewT081Projection(t))
	if result.Status != langfuse.ScoreProjectionStatusQueued {
		t.Fatalf("Enqueue() = %#v, want immediate queued projection", result)
	}
	queueMetrics := metrics.queueSnapshots()
	if len(queueMetrics) != 1 || queueMetrics[0].Backend != "langfuse" || queueMetrics[0].Depth != 1 || queueMetrics[0].RequestID != "" || queueMetrics[0].TraceID != "" || queueMetrics[0].SpanID != "" || queueMetrics[0].SessionID != "" {
		t.Fatalf("queue metrics = %#v, want bounded langfuse depth without high-cardinality identity labels", queueMetrics)
	}
	if status := lifecycle.Status(); status.QueueCapacity != 2 || status.QueueDepth != 1 {
		t.Fatalf("queue status = %#v, want bounded depth 1/2", status)
	}
}

func TestLangfuseScoreLifecycleEnqueueDoesNotBlockAnHTTPRequestOnWorkerDelivery(t *testing.T) {
	worker := &t081Worker{enqueueStarted: make(chan struct{}), releaseEnqueue: make(chan struct{})}
	t.Cleanup(worker.releaseDelivery)
	lifecycle := newT081ConfiguredLifecycle(t, worker, &t081MetricsRecorder{})
	result := make(chan langfuse.ScoreProjection, 1)
	go func() { result <- lifecycle.Enqueue(mustNewT081Projection(t)) }()

	// HTTP 路径只能等待本地有界队列接纳结果，不能等待 Langfuse sender、重试或 shutdown。
	select {
	case got := <-result:
		if got.Status != langfuse.ScoreProjectionStatusQueued {
			t.Fatalf("HTTP-facing Enqueue() = %#v, want immediate queued result", got)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP-facing Enqueue() waited for worker delivery")
	}
	select {
	case <-worker.enqueueStarted:
		// fake worker仍卡在投递端口；HTTP-facing Enqueue 已先返回才证明 command lifecycle
		// 不会把网络投递或重试反压给 handler。
	case <-time.After(time.Second):
		t.Fatal("worker did not receive the queued projection")
	}

	worker.releaseDelivery()
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lifecycle.Shutdown(shutdownContext); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func newT081LifecycleInput() LangfuseScoreLifecycleInput {
	return LangfuseScoreLifecycleInput{
		BaseURL:        "https://langfuse.example.test",
		PublicKey:      "pk-t081",
		SecretKey:      t081LangfuseSecret,
		QueueCapacity:  2,
		MaxAttempts:    2,
		InitialBackoff: time.Second,
		MaxBackoff:     3 * time.Second,
		RequestTimeout: 60 * time.Second,
	}
}

func newT081ConfiguredLifecycle(t *testing.T, worker *t081Worker, metrics *t081MetricsRecorder) *LangfuseScoreLifecycle {
	t.Helper()
	lifecycle, err := BuildLangfuseScoreLifecycle(context.Background(), newT081LifecycleInput(), LangfuseScoreLifecycleDependencies{
		NewClient: func(langfuse.ScoreClientConfig) (langfuse.ScoreSender, error) { return t081Sender{}, nil },
		NewWorker: func(langfuse.ScoreWorkerConfig) (LangfuseScoreWorker, error) { return worker, nil },
		Metrics:   metrics,
		state:     &langfuseScoreLifecycleState{},
	})
	if err != nil {
		t.Fatalf("BuildLangfuseScoreLifecycle() error = %v", err)
	}
	return lifecycle
}

func mustNewT081Projection(t *testing.T) langfuse.ScoreProjection {
	t.Helper()
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity:   obs.NewCorrelationIdentity("req-t081", obs.WithServiceSpan("trace-t081", "span-t081"), obs.WithAITraceID("ai-t081"), obs.WithEvalRunID("eval-t081")),
		Dataset:    aieval.DatasetIdentity{Name: "chat-golden", Version: "v1"},
		SampleID:   "sample-t081",
		MetricName: "answer_relevance",
		Score:      0.91,
	})
	if err != nil {
		t.Fatalf("NewEvaluationEvidence() error = %v", err)
	}
	trace, err := langfuse.MapTraceToProjection(langfuse.TraceMapperInput{
		Span: langfuse.OTLPSpanSnapshot{
			TraceID:         "platform-trace-t081",
			SpanID:          "platform-observation-t081",
			Name:            "ai.generation",
			ObservationType: obs.ObservationTypeGeneration,
			Attributes: map[string]string{
				"ai.feature":                   "chat",
				"ai.outcome":                   "success",
				"gen_ai.provider.name":         "openai-compatible",
				"gen_ai.request.model":         "request-model",
				"gen_ai.response.model":        "response-model",
				"longtermism.payload.mode":     string(obs.PayloadModeMetadataOnly),
				"longtermism.payload.redacted": "false",
			},
		},
		PayloadMode: obs.PayloadModeMetadataOnly,
	})
	if err != nil {
		t.Fatalf("MapTraceToProjection() error = %v", err)
	}
	target, err := langfuse.NewScoreTarget(trace, langfuse.ScoreTargetKindObservation)
	if err != nil {
		t.Fatalf("NewScoreTarget() error = %v", err)
	}
	projection, err := langfuse.NewScoreProjection(langfuse.ScoreProjectionInput{Target: target, Evidence: evidence, MaxAttempts: 2})
	if err != nil {
		t.Fatalf("NewScoreProjection() error = %v", err)
	}
	return projection
}

func cloneT081Evidence(input aieval.EvaluationEvidence) aieval.EvaluationEvidence {
	cloned := input
	if input.Threshold != nil {
		threshold := *input.Threshold
		cloned.Threshold = &threshold
	}
	return cloned
}

type t081Sender struct{}

func (t081Sender) Create(context.Context, langfuse.ScoreProjection) error { return nil }

type t081Worker struct {
	mu             sync.Mutex
	startCount     int
	shutdownCount  int
	flushed        bool
	queueDepth     int
	enqueueStarted chan struct{}
	releaseEnqueue chan struct{}
	releaseOnce    sync.Once
}

func (worker *t081Worker) Start() {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	worker.startCount++
}

func (worker *t081Worker) Enqueue(projection langfuse.ScoreProjection) langfuse.ScoreProjection {
	if worker.enqueueStarted != nil {
		close(worker.enqueueStarted)
		<-worker.releaseEnqueue
	}
	return projection
}

func (worker *t081Worker) releaseDelivery() {
	if worker.releaseEnqueue != nil {
		worker.releaseOnce.Do(func() { close(worker.releaseEnqueue) })
	}
}

func (worker *t081Worker) Shutdown(context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	worker.shutdownCount++
	worker.flushed = true
	return nil
}

func (worker *t081Worker) QueueDepth() int {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.queueDepth
}

func (worker *t081Worker) startCalls() int {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.startCount
}

func (worker *t081Worker) shutdownCalls() int {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.shutdownCount
}

func (worker *t081Worker) wasFlushed() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.flushed
}

type t081MetricsRecorder struct {
	mu          sync.Mutex
	projections []appobservability.ScoreProjectionMetric
	queues      []appobservability.ScoreQueueMetric
}

func (recorder *t081MetricsRecorder) RecordScoreProjection(_ context.Context, metric appobservability.ScoreProjectionMetric) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.projections = append(recorder.projections, metric)
	return nil
}

func (recorder *t081MetricsRecorder) RecordScoreQueue(_ context.Context, metric appobservability.ScoreQueueMetric) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.queues = append(recorder.queues, metric)
	return nil
}

func (recorder *t081MetricsRecorder) projectionSnapshots() []appobservability.ScoreProjectionMetric {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]appobservability.ScoreProjectionMetric(nil), recorder.projections...)
}

func (recorder *t081MetricsRecorder) queueSnapshots() []appobservability.ScoreQueueMetric {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]appobservability.ScoreQueueMetric(nil), recorder.queues...)
}
