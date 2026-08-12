package observability

import (
	"context"
	"strings"
	"testing"
	"time"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	traceapi "go.opentelemetry.io/otel/trace"
)

func TestEvaluatorSpanAdapterRecordsEvidenceWithNativeParentage(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	registerTracerProviderCleanup(t, provider)
	tracer := provider.Tracer("t092-evaluator")
	evaluatorStartedAt := time.Date(2026, time.July, 28, 11, 0, 0, 0, time.UTC)
	evaluatorCompletedAt := evaluatorStartedAt.Add(8 * time.Millisecond)
	parentContext, parent := tracer.Start(
		context.Background(),
		"ai.generation",
		traceapi.WithTimestamp(evaluatorStartedAt.Add(-time.Millisecond)),
	)
	defer parent.End(traceapi.WithTimestamp(evaluatorCompletedAt.Add(time.Millisecond)))

	threshold := 0.8
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: obs.NewCorrelationIdentity(
			"req-t092-evaluator",
			obs.WithServiceSpan("forged-service-trace-t092", "forged-service-span-t092"),
			obs.WithAITraceID("ai-trace-t092-evaluator"),
			obs.WithEvalRunID("eval-run-t092-evaluator"),
		),
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "sample-t092-evaluator",
		MetricName: "answer_relevance",
		Score:      0.9,
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewEvaluationEvidence() error = %v", err)
	}

	platformIdentity, err := NewEvaluatorSpanAdapter(tracer).RecordEvaluator(parentContext, EvaluatorSpanInput{
		Feature:     "chat",
		StartedAt:   evaluatorStartedAt,
		CompletedAt: evaluatorCompletedAt,
		Evidence:    evidence,
	})
	if err != nil {
		t.Fatalf("RecordEvaluator() error = %v", err)
	}
	evaluator := onlyEndedSpanNamed(t, spanRecorder.Ended(), "ai.evaluator")
	assertNativeParentage(t, evaluator, parent.SpanContext(), platformIdentity)
	if got := evaluator.EndTime().Sub(evaluator.StartTime()); got != evaluatorCompletedAt.Sub(evaluatorStartedAt) {
		t.Fatalf("evaluator native span duration = %v, want %v", got, evaluatorCompletedAt.Sub(evaluatorStartedAt))
	}
	attributes := semanticSpanAttributesByKey(evaluator.Attributes())
	for key, want := range map[string]string{
		"longtermism.observability.plane": "ai",
		"longtermism.ai.trace_id":         "ai-trace-t092-evaluator",
		"longtermism.eval.run_id":         "eval-run-t092-evaluator",
		"request.id":                      "req-t092-evaluator",
		"ai.feature":                      "chat",
		"ai.eval.dataset.name":            "chat-completion-contract",
		"ai.eval.dataset.version":         "v1",
		"ai.eval.sample_id":               "sample-t092-evaluator",
		"ai.eval.metric.name":             "answer_relevance",
		"ai.eval.regression_status":       string(aieval.RegressionStatusPassed),
	} {
		if got := attributes[key].AsString(); got != want {
			t.Fatalf("evaluator attribute %q = %q, want %q", key, got, want)
		}
	}
	if got := attributes["ai.eval.score"].AsFloat64(); got != 0.9 {
		t.Fatalf("evaluator score = %v, want 0.9", got)
	}
	if got := attributes["ai.eval.threshold"].AsFloat64(); got != threshold {
		t.Fatalf("evaluator threshold = %v, want %v", got, threshold)
	}
	assertDoesNotLeakForgedDomainPlatformIDs(t, evaluator)
}

func TestEvaluatorSpanAdapterRecordsTrustedSmokeMarker(t *testing.T) {
	const marker = "run-t177-evaluator"
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	registerTracerProviderCleanup(t, provider)
	tracer := provider.Tracer("t177-evaluator")
	parentContext, parent := tracer.Start(context.Background(), "ai.generation")
	threshold := 0.5
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: obs.NewCorrelationIdentity(
			"req-t177-evaluator",
			obs.WithServiceSpan("0123456789abcdef0123456789abcdef", "0123456789abcdef"),
			obs.WithAITraceID("ai-t177-evaluator"),
			obs.WithEvalRunID("eval-t177-evaluator"),
		),
		Dataset: aieval.DatasetIdentity{Name: "chat", Version: "v1"}, SampleID: "sample-t177-evaluator",
		MetricName: "completion_contract", Score: 1, Threshold: &threshold,
	})
	if err != nil {
		t.Fatalf("NewEvaluationEvidence() error = %v", err)
	}
	input := EvaluatorSpanInput{
		Feature:     "chat",
		StartedAt:   time.Date(2026, time.July, 28, 11, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, time.July, 28, 11, 0, 0, int(time.Millisecond), time.UTC),
		Evidence:    evidence,
		SmokeRunID:  marker,
	}
	if _, err := NewEvaluatorSpanAdapter(tracer).RecordEvaluator(parentContext, input); err != nil {
		t.Fatalf("RecordEvaluator() error = %v", err)
	}
	parent.End()
	attributes := semanticSpanAttributesByKey(recorder.Ended()[0].Attributes())
	if got := attributes["longtermism.smoke.run_id"].AsString(); got != marker {
		t.Fatalf("evaluator smoke marker = %q, want %q", got, marker)
	}
}

func TestEvaluatorSpanAdapterRejectsSensitiveOrIncompleteEvidence(t *testing.T) {
	threshold := 0.8
	evaluatorStartedAt := time.Date(2026, time.July, 28, 11, 30, 0, 0, time.UTC)
	evaluatorCompletedAt := evaluatorStartedAt.Add(8 * time.Millisecond)
	tests := []struct {
		name  string
		input EvaluatorSpanInput
	}{
		{
			name: "missing eval identity",
			input: EvaluatorSpanInput{
				Feature:     "chat",
				StartedAt:   evaluatorStartedAt,
				CompletedAt: evaluatorCompletedAt,
				Evidence: aieval.EvaluationEvidence{
					RequestID:        "req-t092-evaluator",
					AITraceID:        "ai-trace-t092-evaluator",
					MetricName:       "answer_relevance",
					RegressionStatus: aieval.RegressionStatusPassed,
				},
			},
		},
		{
			name: "sensitive metric",
			input: EvaluatorSpanInput{
				Feature:     "chat",
				StartedAt:   evaluatorStartedAt,
				CompletedAt: evaluatorCompletedAt,
				Evidence: aieval.EvaluationEvidence{
					EvalRunID:        "eval-run-t092-evaluator",
					RequestID:        "req-t092-evaluator",
					AITraceID:        "ai-trace-t092-evaluator",
					Dataset:          aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
					SampleID:         "sample-t092-evaluator",
					MetricName:       "Bearer t092-private-token",
					Score:            0.9,
					RegressionStatus: aieval.RegressionStatusPassed,
				},
			},
		},
		{
			name: "regression status contradicts score and threshold",
			input: EvaluatorSpanInput{
				Feature:     "chat",
				StartedAt:   evaluatorStartedAt,
				CompletedAt: evaluatorCompletedAt,
				Evidence: aieval.EvaluationEvidence{
					EvalRunID:        "eval-run-t092-evaluator",
					RequestID:        "req-t092-evaluator",
					AITraceID:        "ai-trace-t092-evaluator",
					Dataset:          aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
					SampleID:         "sample-t092-evaluator",
					MetricName:       "answer_relevance",
					Score:            0.1,
					Threshold:        &threshold,
					RegressionStatus: aieval.RegressionStatusPassed,
				},
			},
		},
		{
			name: "sensitive feature",
			input: EvaluatorSpanInput{
				Feature:     "Bearer t092-private-token",
				StartedAt:   evaluatorStartedAt,
				CompletedAt: evaluatorCompletedAt,
			},
		},
		{
			name: "duration exceeds request deadline",
			input: EvaluatorSpanInput{
				Feature:     "chat",
				StartedAt:   evaluatorStartedAt,
				CompletedAt: evaluatorStartedAt.Add(maxSemanticSpanDuration + time.Nanosecond),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanRecorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			registerTracerProviderCleanup(t, provider)
			parentContext, parent := provider.Tracer("t092-invalid-evaluator").Start(context.Background(), "ai.generation")
			defer parent.End()

			_, err := NewEvaluatorSpanAdapter(provider.Tracer("t092-invalid-evaluator")).RecordEvaluator(parentContext, tt.input)
			if err == nil || len(spanRecorder.Ended()) != 0 {
				t.Fatalf("RecordEvaluator() = (%v, spans:%d), want fail-fast without evaluator span", err, len(spanRecorder.Ended()))
			}
			if strings.Contains(err.Error(), "Bearer t092-private-token") {
				t.Fatal("evaluator validation error must not echo sensitive evidence")
			}
		})
	}
}
