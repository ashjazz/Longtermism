package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

func TestRunnerAddsEvaluationEvidenceWithTraceIdentity(t *testing.T) {
	identity := obs.NewCorrelationIdentity(
		"req-runner-trace-link-001",
		obs.WithServiceSpan("svc-trace-runner-trace-link-001", "span-runner-trace-link-001"),
		obs.WithAITraceID("ai-trace-runner-trace-link-001"),
	)
	runner := NewRunner(
		DatasetIdentity{Name: "agent-golden", Version: "agent-golden-v1"},
		WithEvalRunID("eval-run-runner-trace-link-001"),
		WithMetricThreshold("answer_relevance", 0.8),
	)
	dataset := runnerTestDataset{samples: []Sample{
		{ID: "sample-runner-trace-link-001", Query: "How should eval evidence link traces?"},
	}}
	predict := func(_ context.Context, sample Sample) (Prediction, error) {
		return Prediction{
			Answer:        "Eval evidence links sample, metric and trace identity.",
			TokensUsed:    17,
			TraceIdentity: identity,
		}, nil
	}
	metrics := []Metric{
		runnerTestMetric{
			name:   "answer_relevance",
			scores: map[string]float64{"sample-runner-trace-link-001": 0.92},
		},
	}

	report, err := runner.Run(context.Background(), dataset, predict, metrics)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if report.Dataset.Name != "agent-golden" {
		t.Fatalf("Dataset.Name = %q, want agent-golden", report.Dataset.Name)
	}
	if report.Dataset.Version != "agent-golden-v1" {
		t.Fatalf("Dataset.Version = %q, want agent-golden-v1", report.Dataset.Version)
	}
	if report.SampleCount != 1 {
		t.Fatalf("SampleCount = %d, want 1", report.SampleCount)
	}
	assertReportScore(t, report, "answer_relevance", 0.92)
	if len(report.Evidence) != 1 {
		t.Fatalf("Evidence length = %d, want 1: %#v", len(report.Evidence), report.Evidence)
	}

	evidence := report.Evidence[0]
	if evidence.EvalRunID != "eval-run-runner-trace-link-001" {
		t.Fatalf("EvalRunID = %q, want eval-run-runner-trace-link-001", evidence.EvalRunID)
	}
	if evidence.Dataset.Name != "agent-golden" || evidence.Dataset.Version != "agent-golden-v1" {
		t.Fatalf("dataset identity = (%q, %q), want agent-golden/agent-golden-v1", evidence.Dataset.Name, evidence.Dataset.Version)
	}
	if evidence.SampleID != "sample-runner-trace-link-001" {
		t.Fatalf("SampleID = %q, want sample-runner-trace-link-001", evidence.SampleID)
	}
	if evidence.MetricName != "answer_relevance" {
		t.Fatalf("MetricName = %q, want answer_relevance", evidence.MetricName)
	}
	if evidence.Score != 0.92 {
		t.Fatalf("Score = %v, want 0.92", evidence.Score)
	}
	if evidence.Threshold == nil || *evidence.Threshold != 0.8 {
		t.Fatalf("Threshold = %#v, want 0.8", evidence.Threshold)
	}
	if evidence.RegressionStatus != RegressionStatusPassed {
		t.Fatalf("RegressionStatus = %q, want %q", evidence.RegressionStatus, RegressionStatusPassed)
	}
	assertRunnerEvidenceTraceIdentity(t, evidence, obs.NewCorrelationIdentity(
		identity.RequestID,
		obs.WithServiceSpan(identity.ServiceTraceID, identity.SpanID),
		obs.WithAITraceID(identity.AITraceID),
		obs.WithEvalRunID("eval-run-runner-trace-link-001"),
	))
}

func TestRunnerReportsMissingTraceLinkForSingleSample(t *testing.T) {
	runner := NewRunner(
		DatasetIdentity{Name: "agent-golden", Version: "agent-golden-v1"},
		WithEvalRunID("eval-run-missing-trace-link-001"),
		WithMetricThreshold("answer_relevance", 0.8),
	)
	dataset := runnerTestDataset{samples: []Sample{
		{ID: "sample-missing-trace-link-001", Query: "missing trace"},
	}}
	predict := func(_ context.Context, sample Sample) (Prediction, error) {
		return Prediction{
			Answer:     "This prediction forgot to return trace identity.",
			TokensUsed: 11,
		}, nil
	}
	metrics := []Metric{
		runnerTestMetric{
			name:   "answer_relevance",
			scores: map[string]float64{"sample-missing-trace-link-001": 0.91},
		},
	}

	_, err := runner.Run(context.Background(), dataset, predict, metrics)
	if err == nil {
		t.Fatal("Run() error = nil, want missing trace link error")
	}
	for _, want := range []string{"sample-missing-trace-link-001", "trace", "request_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Run() error = %v, want mention %q", err, want)
		}
	}
}

func TestRunnerAddsFailedRegressionEvidenceForBelowThresholdMetric(t *testing.T) {
	identity := obs.NewCorrelationIdentity(
		"req-runner-regression-001",
		obs.WithServiceSpan("svc-trace-runner-regression-001", "span-runner-regression-001"),
		obs.WithAITraceID("ai-trace-runner-regression-001"),
	)
	runner := NewRunner(
		DatasetIdentity{Name: "agent-golden", Version: "agent-golden-v1"},
		WithEvalRunID("eval-run-runner-regression-001"),
		WithMetricThreshold("answer_relevance", 0.8),
	)
	dataset := runnerTestDataset{samples: []Sample{
		{ID: "sample-runner-regression-001", Query: "low quality answer"},
	}}
	predict := func(_ context.Context, sample Sample) (Prediction, error) {
		return Prediction{
			Answer:        "insufficient answer",
			TokensUsed:    9,
			TraceIdentity: identity,
		}, nil
	}
	metrics := []Metric{
		runnerTestMetric{
			name:   "answer_relevance",
			scores: map[string]float64{"sample-runner-regression-001": 0.42},
		},
	}

	report, err := runner.Run(context.Background(), dataset, predict, metrics)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Evidence) != 1 {
		t.Fatalf("Evidence length = %d, want 1", len(report.Evidence))
	}

	evidence := report.Evidence[0]
	if evidence.RegressionStatus != RegressionStatusFailed {
		t.Fatalf("RegressionStatus = %q, want %q", evidence.RegressionStatus, RegressionStatusFailed)
	}
	if evidence.FailureSummary == "" {
		t.Fatalf("FailureSummary is empty, want threshold failure summary")
	}
	if !strings.Contains(evidence.FailureSummary, "below threshold") {
		t.Fatalf("FailureSummary = %q, want threshold diagnostic", evidence.FailureSummary)
	}
}

func assertRunnerEvidenceTraceIdentity(t *testing.T, evidence EvaluationEvidence, identity obs.CorrelationIdentity) {
	t.Helper()

	if evidence.RequestID != identity.RequestID {
		t.Fatalf("RequestID = %q, want %q", evidence.RequestID, identity.RequestID)
	}
	if evidence.AITraceID != identity.AITraceID {
		t.Fatalf("AITraceID = %q, want %q", evidence.AITraceID, identity.AITraceID)
	}
	if evidence.ServiceTraceID != identity.ServiceTraceID {
		t.Fatalf("ServiceTraceID = %q, want %q", evidence.ServiceTraceID, identity.ServiceTraceID)
	}
	if evidence.SpanID != identity.SpanID {
		t.Fatalf("SpanID = %q, want %q", evidence.SpanID, identity.SpanID)
	}
	if evidence.EvalRunID != identity.EvalRunID {
		t.Fatalf("EvalRunID = %q, want %q", evidence.EvalRunID, identity.EvalRunID)
	}
}
