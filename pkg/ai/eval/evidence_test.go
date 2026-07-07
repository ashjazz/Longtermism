package eval

import (
	"strings"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

func TestNewEvaluationEvidenceBuildsLinkedEvidence(t *testing.T) {
	identity := obs.NewCorrelationIdentity(
		"req-eval-evidence-001",
		obs.WithServiceSpan("svc-trace-eval-evidence-001", "span-eval-evidence-001"),
		obs.WithAITraceID("ai-trace-eval-evidence-001"),
		obs.WithEvalRunID("eval-run-evidence-001"),
	)

	evidence, err := NewEvaluationEvidence(EvaluationEvidenceInput{
		Identity:       identity,
		DatasetName:    "agent-golden",
		DatasetVersion: "v1.2.0",
		SampleID:       "sample-tool-loop-001",
		MetricName:     "answer_relevance",
		Score:          0.91,
		Threshold:      float64Pointer(0.8),
	})
	if err != nil {
		t.Fatalf("NewEvaluationEvidence() error = %v", err)
	}

	assertEvaluationEvidenceIdentity(t, evidence, identity)
	if evidence.DatasetName != "agent-golden" {
		t.Fatalf("DatasetName = %q, want agent-golden", evidence.DatasetName)
	}
	if evidence.DatasetVersion != "v1.2.0" {
		t.Fatalf("DatasetVersion = %q, want v1.2.0", evidence.DatasetVersion)
	}
	if evidence.SampleID != "sample-tool-loop-001" {
		t.Fatalf("SampleID = %q, want sample-tool-loop-001", evidence.SampleID)
	}
	if evidence.MetricName != "answer_relevance" {
		t.Fatalf("MetricName = %q, want answer_relevance", evidence.MetricName)
	}
	if evidence.Score != 0.91 {
		t.Fatalf("Score = %v, want 0.91", evidence.Score)
	}
	if evidence.Threshold == nil || *evidence.Threshold != 0.8 {
		t.Fatalf("Threshold = %#v, want 0.8", evidence.Threshold)
	}
	if evidence.RegressionStatus != RegressionStatusPassed {
		t.Fatalf("RegressionStatus = %q, want %q", evidence.RegressionStatus, RegressionStatusPassed)
	}
	if evidence.FailureSummary != "" {
		t.Fatalf("FailureSummary = %q, want empty for passing evidence", evidence.FailureSummary)
	}
}

func TestNewEvaluationEvidenceClassifiesThresholdRegressionStatus(t *testing.T) {
	tests := []struct {
		name          string
		score         float64
		threshold     *float64
		wantStatus    RegressionStatus
		wantSummary   string
		wantErrSubstr string
	}{
		{
			name:        "score passes configured threshold",
			score:       0.86,
			threshold:   float64Pointer(0.8),
			wantStatus:  RegressionStatusPassed,
			wantSummary: "",
		},
		{
			name:        "score below configured threshold fails with summary",
			score:       0.42,
			threshold:   float64Pointer(0.8),
			wantStatus:  RegressionStatusFailed,
			wantSummary: "score 0.42 is below threshold 0.80",
		},
		{
			name:       "missing threshold keeps status warning for manual review",
			score:      0.77,
			threshold:  nil,
			wantStatus: RegressionStatusWarning,
		},
		{
			name:          "score below zero is rejected",
			score:         -0.01,
			threshold:     float64Pointer(0.8),
			wantErrSubstr: "score",
		},
		{
			name:          "threshold above one is rejected",
			score:         0.9,
			threshold:     float64Pointer(1.2),
			wantErrSubstr: "threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence, err := NewEvaluationEvidence(EvaluationEvidenceInput{
				Identity:       validEvidenceIdentity(),
				DatasetName:    "agent-golden",
				DatasetVersion: "v1.2.0",
				SampleID:       "sample-threshold-001",
				MetricName:     "answer_relevance",
				Score:          tt.score,
				Threshold:      tt.threshold,
			})
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("NewEvaluationEvidence() error = nil, want %q", tt.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("NewEvaluationEvidence() error = %v, want mention %q", err, tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewEvaluationEvidence() error = %v", err)
			}
			if evidence.RegressionStatus != tt.wantStatus {
				t.Fatalf("RegressionStatus = %q, want %q", evidence.RegressionStatus, tt.wantStatus)
			}
			if tt.wantSummary != "" && !strings.Contains(evidence.FailureSummary, tt.wantSummary) {
				t.Fatalf("FailureSummary = %q, want contain %q", evidence.FailureSummary, tt.wantSummary)
			}
		})
	}
}

func TestValidateEvaluationEvidenceRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(EvaluationEvidenceInput) EvaluationEvidenceInput
		wantErrSubstr string
	}{
		{
			name: "missing eval run id",
			mutate: func(input EvaluationEvidenceInput) EvaluationEvidenceInput {
				input.Identity = obs.NewCorrelationIdentity(
					"req-eval-evidence-001",
					obs.WithServiceSpan("svc-trace-eval-evidence-001", "span-eval-evidence-001"),
					obs.WithAITraceID("ai-trace-eval-evidence-001"),
				)
				return input
			},
			wantErrSubstr: "eval_run_id",
		},
		{
			name: "missing dataset name",
			mutate: func(input EvaluationEvidenceInput) EvaluationEvidenceInput {
				input.DatasetName = ""
				return input
			},
			wantErrSubstr: "dataset_name",
		},
		{
			name: "missing dataset version",
			mutate: func(input EvaluationEvidenceInput) EvaluationEvidenceInput {
				input.DatasetVersion = ""
				return input
			},
			wantErrSubstr: "dataset_version",
		},
		{
			name: "missing sample id",
			mutate: func(input EvaluationEvidenceInput) EvaluationEvidenceInput {
				input.SampleID = ""
				return input
			},
			wantErrSubstr: "sample_id",
		},
		{
			name: "missing metric name",
			mutate: func(input EvaluationEvidenceInput) EvaluationEvidenceInput {
				input.MetricName = ""
				return input
			},
			wantErrSubstr: "metric_name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validEvaluationEvidenceInput()
			_, err := NewEvaluationEvidence(tt.mutate(input))
			if err == nil {
				t.Fatalf("NewEvaluationEvidence() error = nil, want %q", tt.wantErrSubstr)
			}
			if !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("NewEvaluationEvidence() error = %v, want mention %q", err, tt.wantErrSubstr)
			}
		})
	}
}

func TestValidateEvaluationEvidenceRejectsMissingTraceLink(t *testing.T) {
	tests := []struct {
		name          string
		identity      obs.CorrelationIdentity
		wantErrSubstr string
	}{
		{
			name: "missing request id",
			identity: obs.NewCorrelationIdentity(
				"",
				obs.WithServiceSpan("svc-trace-eval-evidence-001", "span-eval-evidence-001"),
				obs.WithAITraceID("ai-trace-eval-evidence-001"),
				obs.WithEvalRunID("eval-run-evidence-001"),
			),
			wantErrSubstr: "request_id",
		},
		{
			name: "missing ai trace id",
			identity: obs.NewCorrelationIdentity(
				"req-eval-evidence-001",
				obs.WithServiceSpan("svc-trace-eval-evidence-001", "span-eval-evidence-001"),
				obs.WithEvalRunID("eval-run-evidence-001"),
			),
			wantErrSubstr: "ai_trace_id",
		},
		{
			name: "missing service trace id",
			identity: obs.NewCorrelationIdentity(
				"req-eval-evidence-001",
				obs.WithServiceSpan("", "span-eval-evidence-001"),
				obs.WithAITraceID("ai-trace-eval-evidence-001"),
				obs.WithEvalRunID("eval-run-evidence-001"),
			),
			wantErrSubstr: "service_trace_id",
		},
		{
			name: "missing span id",
			identity: obs.NewCorrelationIdentity(
				"req-eval-evidence-001",
				obs.WithServiceSpan("svc-trace-eval-evidence-001", ""),
				obs.WithAITraceID("ai-trace-eval-evidence-001"),
				obs.WithEvalRunID("eval-run-evidence-001"),
			),
			wantErrSubstr: "span_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validEvaluationEvidenceInput()
			input.Identity = tt.identity

			_, err := NewEvaluationEvidence(input)
			if err == nil {
				t.Fatalf("NewEvaluationEvidence() error = nil, want missing trace link error")
			}
			if !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("NewEvaluationEvidence() error = %v, want mention %q", err, tt.wantErrSubstr)
			}
		})
	}
}

func assertEvaluationEvidenceIdentity(t *testing.T, evidence EvaluationEvidence, identity obs.CorrelationIdentity) {
	t.Helper()

	if evidence.EvalRunID != identity.EvalRunID {
		t.Fatalf("EvalRunID = %q, want %q", evidence.EvalRunID, identity.EvalRunID)
	}
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
}

func validEvaluationEvidenceInput() EvaluationEvidenceInput {
	return EvaluationEvidenceInput{
		Identity:       validEvidenceIdentity(),
		DatasetName:    "agent-golden",
		DatasetVersion: "v1.2.0",
		SampleID:       "sample-evidence-001",
		MetricName:     "answer_relevance",
		Score:          0.9,
		Threshold:      float64Pointer(0.8),
	}
}

func validEvidenceIdentity() obs.CorrelationIdentity {
	return obs.NewCorrelationIdentity(
		"req-eval-evidence-001",
		obs.WithServiceSpan("svc-trace-eval-evidence-001", "span-eval-evidence-001"),
		obs.WithAITraceID("ai-trace-eval-evidence-001"),
		obs.WithEvalRunID("eval-run-evidence-001"),
	)
}

func float64Pointer(value float64) *float64 {
	return &value
}
