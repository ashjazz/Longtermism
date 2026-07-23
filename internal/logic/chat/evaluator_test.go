package chat

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestDeterministicEvaluatorBuildsPassedEvidenceWithFullCorrelation(t *testing.T) {
	threshold := 1.0
	evaluator, err := NewDeterministicEvaluator(DeterministicEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "chat-completion-t074",
		MetricName: "chat_completion_contract_v1",
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewDeterministicEvaluator() error = %v", err)
	}
	input := validEvaluationInput()

	result, err := evaluator.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.Evidence == nil {
		t.Fatal("Evaluate() must create local evidence when evaluation runs")
	}
	assertEvaluationEvidenceCorrelation(t, *result.Evidence, input.Identity, "chat-completion-contract", "v1", "chat-completion-t074", "chat_completion_contract_v1", 1, &threshold, aieval.RegressionStatusPassed)
	assertDebugEvalSummary(t, result.Summary, EvalStatusPassed, "deterministic_completion_contract_v1", ptrEvaluationScore(1), "within_policy")
}

func TestDeterministicEvaluatorClassifiesWarningAndFailedCompletionFacts(t *testing.T) {
	threshold := 1.0
	tests := []struct {
		name            string
		configThreshold *float64
		mutate          func(EvaluationInput) EvaluationInput
		wantStatus      EvalStatus
		wantScore       float64
		wantReason      string
		wantEvidence    aieval.RegressionStatus
	}{
		{
			name:            "missing threshold reports warning rather than a false pass",
			configThreshold: nil,
			mutate:          func(input EvaluationInput) EvaluationInput { return input },
			wantStatus:      EvalStatusWarning,
			wantScore:       1,
			wantReason:      "threshold_not_configured",
			wantEvidence:    aieval.RegressionStatusWarning,
		},
		{
			name:            "missing output reports a stable failure class",
			configThreshold: &threshold,
			mutate: func(input EvaluationInput) EvaluationInput {
				input.OutputPresent = false
				return input
			},
			wantStatus:   EvalStatusFailed,
			wantScore:    0,
			wantReason:   "output_missing",
			wantEvidence: aieval.RegressionStatusFailed,
		},
		{
			name:            "missing actual model reports a stable failure class",
			configThreshold: &threshold,
			mutate: func(input EvaluationInput) EvaluationInput {
				input.ActualModel = ""
				return input
			},
			wantStatus:   EvalStatusFailed,
			wantScore:    0,
			wantReason:   "actual_model_missing",
			wantEvidence: aieval.RegressionStatusFailed,
		},
		{
			name:            "invalid finish reason reports a stable failure class",
			configThreshold: &threshold,
			mutate: func(input EvaluationInput) EvaluationInput {
				input.FinishReason = "unrecognized"
				return input
			},
			wantStatus:   EvalStatusFailed,
			wantScore:    0,
			wantReason:   "finish_reason_invalid",
			wantEvidence: aieval.RegressionStatusFailed,
		},
		{
			name:            "inconsistent usage reports a stable failure class",
			configThreshold: &threshold,
			mutate: func(input EvaluationInput) EvaluationInput {
				input.Usage.TotalTokens = input.Usage.InputTokens + input.Usage.OutputTokens - 1
				return input
			},
			wantStatus:   EvalStatusFailed,
			wantScore:    0,
			wantReason:   "usage_inconsistent",
			wantEvidence: aieval.RegressionStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluator, err := NewDeterministicEvaluator(DeterministicEvaluatorConfig{
				Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
				SampleID:   "chat-completion-t074",
				MetricName: "chat_completion_contract_v1",
				Threshold:  tt.configThreshold,
			})
			if err != nil {
				t.Fatalf("NewDeterministicEvaluator() error = %v", err)
			}
			input := tt.mutate(validEvaluationInput())
			result, err := evaluator.Evaluate(context.Background(), input)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Evidence == nil {
				t.Fatal("Evaluate() must retain evidence for a completed evaluation")
			}
			assertEvaluationEvidenceCorrelation(t, *result.Evidence, input.Identity, "chat-completion-contract", "v1", "chat-completion-t074", "chat_completion_contract_v1", tt.wantScore, tt.configThreshold, tt.wantEvidence)
			assertDebugEvalSummary(t, result.Summary, tt.wantStatus, "deterministic_completion_contract_v1", ptrEvaluationScore(tt.wantScore), tt.wantReason)
		})
	}
}

func TestNotRunEvaluatorRecordsNoSyntheticEvidence(t *testing.T) {
	evaluator := NewNotRunEvaluator()
	result, err := evaluator.Evaluate(context.Background(), validEvaluationInput())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.Evidence != nil {
		t.Fatalf("not_run evaluation evidence = %#v, want nil", result.Evidence)
	}
	assertDebugEvalSummary(t, result.Summary, EvalStatusNotRun, "", nil, "evaluator_not_configured")
}

func TestDebugEvalSummaryExposureIsBoundedAndHonorsDebugFlag(t *testing.T) {
	threshold := 1.0
	evaluator, err := NewDeterministicEvaluator(DeterministicEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "chat-completion-t074",
		MetricName: "chat_completion_contract_v1",
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewDeterministicEvaluator() error = %v", err)
	}

	// 模型名是 provider 事实而非用户输入。这里故意放入 synthetic marker，证明摘要和
	// evidence 不会把 provider 返回的任意字符串回显为诊断内容。
	const userMarker = "user-message-t074-private"
	const outputMarker = "model-output-t074-private"
	const credentialMarker = "Bearer t074-private-token"
	input := validEvaluationInput()
	input.ActualModel = credentialMarker
	result, err := evaluator.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if summary := ExposeDebugEvalSummary(result, false); summary != nil {
		t.Fatalf("ExposeDebugEvalSummary(debug=false) = %#v, want nil", summary)
	}
	summary := ExposeDebugEvalSummary(result, true)
	if summary == nil {
		t.Fatal("ExposeDebugEvalSummary(debug=true) = nil, want bounded low-sensitivity summary")
	}
	serializedSummary, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal debug summary: %v", err)
	}
	if len(serializedSummary) > 1024 {
		t.Fatalf("serialized debug summary length = %d, want <= 1024", len(serializedSummary))
	}
	serializedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal evaluation result: %v", err)
	}
	for _, forbidden := range []string{userMarker, outputMarker, credentialMarker, "prompt", "provider_body", "endpoint", "api_key", "authorization"} {
		if strings.Contains(strings.ToLower(string(serializedSummary)), strings.ToLower(forbidden)) || strings.Contains(strings.ToLower(string(serializedResult)), strings.ToLower(forbidden)) {
			t.Fatalf("evaluation debug output leaked forbidden content %q", forbidden)
		}
	}
	if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(summary.ReasonClass) {
		t.Fatalf("ReasonClass = %q, want stable low-sensitivity enum spelling", summary.ReasonClass)
	}
	assertEvaluationInputIsLowSensitivity(t)
}

func assertEvaluationEvidenceCorrelation(t *testing.T, evidence aieval.EvaluationEvidence, identity obs.CorrelationIdentity, datasetName, datasetVersion, sampleID, metricName string, score float64, threshold *float64, status aieval.RegressionStatus) {
	t.Helper()
	if evidence.EvalRunID != identity.EvalRunID || evidence.RequestID != identity.RequestID || evidence.AITraceID != identity.AITraceID || evidence.ServiceTraceID != identity.ServiceTraceID || evidence.SpanID != identity.SpanID {
		t.Fatalf("evidence correlation = %#v, want identity %#v", evidence, identity)
	}
	if evidence.Dataset != (aieval.DatasetIdentity{Name: datasetName, Version: datasetVersion}) || evidence.SampleID != sampleID || evidence.MetricName != metricName || evidence.Score != score || evidence.RegressionStatus != status {
		t.Fatalf("evidence = %#v, want dataset/sample/metric/score/status facts", evidence)
	}
	if !reflect.DeepEqual(evidence.Threshold, threshold) {
		t.Fatalf("evidence threshold = %#v, want %#v", evidence.Threshold, threshold)
	}
}

func assertDebugEvalSummary(t *testing.T, summary DebugEvalSummary, status EvalStatus, evaluator string, score *float64, reasonClass string) {
	t.Helper()
	if summary.Status != status || summary.Evaluator != evaluator || !reflect.DeepEqual(summary.Score, score) || summary.ReasonClass != reasonClass {
		t.Fatalf("debug summary = %#v, want status=%q evaluator=%q score=%#v reason_class=%q", summary, status, evaluator, score, reasonClass)
	}
}

func assertEvaluationInputIsLowSensitivity(t *testing.T) {
	t.Helper()
	allowed := map[string]struct{}{
		"Identity": {}, "ActualModel": {}, "FinishReason": {}, "Usage": {}, "OutputPresent": {},
	}
	inputType := reflect.TypeFor[EvaluationInput]()
	for index := range inputType.NumField() {
		field := inputType.Field(index)
		if _, ok := allowed[field.Name]; !ok {
			t.Fatalf("EvaluationInput must not expose raw or provider-sensitive field %q", field.Name)
		}
	}
}

func validEvaluationInput() EvaluationInput {
	return EvaluationInput{
		Identity: obs.NewCorrelationIdentity(
			"req-t074-evaluator",
			obs.WithServiceSpan("service-trace-t074", "span-t074"),
			obs.WithAITraceID("ai-trace-t074"),
			obs.WithEvalRunID("eval-run-t074"),
		),
		ActualModel:   "provider-actual-model",
		FinishReason:  llm.FinishStop,
		Usage:         llm.Usage{InputTokens: 11, OutputTokens: 17, TotalTokens: 28},
		OutputPresent: true,
	}
}

func ptrEvaluationScore(value float64) *float64 { return &value }
