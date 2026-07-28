package chat

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestCompletionContractEvaluatorBuildsPassedEvidenceWithFullCorrelation(t *testing.T) {
	threshold := 1.0
	evaluator, err := NewCompletionContractEvaluator(CompletionContractEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "chat-completion-t074",
		MetricName: "chat_completion_contract_v1",
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewCompletionContractEvaluator() error = %v", err)
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
	assertDebugEvalSummary(t, result.Summary, EvalStatusPassed, "completion_contract_v1", ptrEvaluationScore(1), "within_policy")
}

func TestCompletionContractEvaluatorClassifiesWarningAndFailedCompletionFacts(t *testing.T) {
	threshold := 1.0
	tests := []struct {
		name            string
		configThreshold *float64
		mutate          func(CompletionContractEvaluationInput) CompletionContractEvaluationInput
		wantStatus      EvalStatus
		wantScore       float64
		wantReason      string
		wantEvidence    aieval.RegressionStatus
	}{
		{
			name:            "missing threshold reports warning rather than a false pass",
			configThreshold: nil,
			mutate:          func(input CompletionContractEvaluationInput) CompletionContractEvaluationInput { return input },
			wantStatus:      EvalStatusWarning,
			wantScore:       1,
			wantReason:      "threshold_not_configured",
			wantEvidence:    aieval.RegressionStatusWarning,
		},
		{
			name:            "missing output reports a stable failure class",
			configThreshold: &threshold,
			mutate: func(input CompletionContractEvaluationInput) CompletionContractEvaluationInput {
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
			mutate: func(input CompletionContractEvaluationInput) CompletionContractEvaluationInput {
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
			mutate: func(input CompletionContractEvaluationInput) CompletionContractEvaluationInput {
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
			mutate: func(input CompletionContractEvaluationInput) CompletionContractEvaluationInput {
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
			evaluator, err := NewCompletionContractEvaluator(CompletionContractEvaluatorConfig{
				Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
				SampleID:   "chat-completion-t074",
				MetricName: "chat_completion_contract_v1",
				Threshold:  tt.configThreshold,
			})
			if err != nil {
				t.Fatalf("NewCompletionContractEvaluator() error = %v", err)
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
			assertDebugEvalSummary(t, result.Summary, tt.wantStatus, "completion_contract_v1", ptrEvaluationScore(tt.wantScore), tt.wantReason)
		})
	}
}

func TestCompletionContractNotRunEvaluatorRecordsNoSyntheticEvidence(t *testing.T) {
	evaluator := NewCompletionContractNotRunEvaluator()
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
	evaluator, err := NewCompletionContractEvaluator(CompletionContractEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "chat-completion-t074",
		MetricName: "chat_completion_contract_v1",
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewCompletionContractEvaluator() error = %v", err)
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
	if string(serializedResult) != "{}" {
		t.Fatalf("serialized internal evaluation result = %s, want no debug or evidence fields", serializedResult)
	}
	for _, forbidden := range []string{userMarker, outputMarker, credentialMarker, "prompt", "provider_body", "endpoint", "api_key", "authorization"} {
		if strings.Contains(strings.ToLower(string(serializedSummary)), strings.ToLower(forbidden)) || strings.Contains(strings.ToLower(string(serializedResult)), strings.ToLower(forbidden)) {
			t.Fatalf("evaluation debug output leaked forbidden content %q", forbidden)
		}
	}
	if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(summary.ReasonClass) {
		t.Fatalf("ReasonClass = %q, want stable low-sensitivity enum spelling", summary.ReasonClass)
	}
	assertCompletionContractEvaluationInputIsLowSensitivity(t)
}

func TestNewCompletionContractEvaluatorRejectsInvalidConfigurationAndClonesThreshold(t *testing.T) {
	validConfig := func() CompletionContractEvaluatorConfig {
		threshold := 1.0
		return CompletionContractEvaluatorConfig{
			Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
			SampleID:   "chat-completion-t074",
			MetricName: "chat_completion_contract_v1",
			Threshold:  &threshold,
		}
	}
	tests := []struct {
		name   string
		mutate func(CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig
	}{
		{name: "dataset name is required", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			config.Dataset.Name = " "
			return config
		}},
		{name: "dataset version is required", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			config.Dataset.Version = " "
			return config
		}},
		{name: "sample id is required", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			config.SampleID = " "
			return config
		}},
		{name: "metric name is required", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			config.MetricName = " "
			return config
		}},
		{name: "sensitive dataset fact is rejected", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			config.Dataset.Name = "Bearer private-dataset-token"
			return config
		}},
		{name: "oversized sample identity is rejected", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			config.SampleID = strings.Repeat("a", maxEvaluationFactBytes+1)
			return config
		}},
		{name: "zero threshold would make a failed binary score pass", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			zero := 0.0
			config.Threshold = &zero
			return config
		}},
		{name: "nan threshold is invalid", mutate: func(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
			nan := math.NaN()
			config.Threshold = &nan
			return config
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCompletionContractEvaluator(tt.mutate(validConfig())); err == nil {
				t.Fatal("NewCompletionContractEvaluator() error = nil, want invalid configuration error")
			}
		})
	}

	config := validConfig()
	evaluator, err := NewCompletionContractEvaluator(config)
	if err != nil {
		t.Fatalf("NewCompletionContractEvaluator() error = %v", err)
	}
	*config.Threshold = 0.5
	result, err := evaluator.Evaluate(context.Background(), validEvaluationInput())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.Evidence == nil || result.Evidence.Threshold == nil || *result.Evidence.Threshold != 1 {
		t.Fatalf("evidence threshold = %#v, want immutable constructor snapshot 1", result.Evidence)
	}

	unicodeEvaluator, err := NewCompletionContractEvaluator(CompletionContractEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "客服 对话", Version: "v1.0.0+build.1"},
		SampleID:   "样例 01",
		MetricName: "回答 相关性",
		Threshold:  ptrEvaluationScore(1),
	})
	if err != nil {
		t.Fatalf("NewCompletionContractEvaluator(valid Unicode facts) error = %v", err)
	}
	unicodeResult, err := unicodeEvaluator.Evaluate(context.Background(), validEvaluationInput())
	if err != nil {
		t.Fatalf("Evaluate(valid Unicode facts) error = %v", err)
	}
	if unicodeResult.Evidence == nil ||
		unicodeResult.Evidence.Dataset.Name != "客服 对话" ||
		unicodeResult.Evidence.Dataset.Version != "v1.0.0+build.1" ||
		unicodeResult.Evidence.SampleID != "样例 01" ||
		unicodeResult.Evidence.MetricName != "回答 相关性" {
		t.Fatalf("Unicode evidence = %#v, want domain facts preserved", unicodeResult.Evidence)
	}
}

func TestEvaluatorContextAndDebugSummaryFailClosed(t *testing.T) {
	threshold := 1.0
	evaluator, err := NewCompletionContractEvaluator(CompletionContractEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "chat-completion-t074",
		MetricName: "chat_completion_contract_v1",
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewCompletionContractEvaluator() error = %v", err)
	}
	if _, err := evaluator.Evaluate(nil, validEvaluationInput()); !errors.Is(err, ErrEvaluatorInvalidContext) {
		t.Fatalf("Evaluate(nil) error = %v, want ErrEvaluatorInvalidContext", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evaluator.Evaluate(ctx, validEvaluationInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate(canceled) error = %v, want context.Canceled", err)
	}
	notRun := NewCompletionContractNotRunEvaluator()
	if _, err := notRun.Evaluate(nil, validEvaluationInput()); !errors.Is(err, ErrEvaluatorInvalidContext) {
		t.Fatalf("CompletionContractNotRunEvaluator.Evaluate(nil) error = %v, want ErrEvaluatorInvalidContext", err)
	}
	if _, err := notRun.Evaluate(ctx, validEvaluationInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("CompletionContractNotRunEvaluator.Evaluate(canceled) error = %v, want context.Canceled", err)
	}

	oversized := CompletionContractEvaluationResult{Summary: DebugEvalSummary{
		Status:      EvalStatusPassed,
		Evaluator:   strings.Repeat("a", 1025),
		Score:       ptrEvaluationScore(1),
		ReasonClass: "within_policy",
	}}
	if got := ExposeDebugEvalSummary(oversized, true); got != nil {
		t.Fatalf("ExposeDebugEvalSummary(oversized) = %#v, want fail-closed nil", got)
	}
	originalScore := 1.0
	result := CompletionContractEvaluationResult{Summary: DebugEvalSummary{
		Status: EvalStatusPassed, Evaluator: "completion_contract_v1",
		Score: &originalScore, ReasonClass: "within_policy",
	}}
	exposed := ExposeDebugEvalSummary(result, true)
	if exposed == nil || exposed.Score == nil {
		t.Fatal("ExposeDebugEvalSummary(valid) = nil, want defensive copy")
	}
	*exposed.Score = 0
	if *result.Summary.Score != 1 {
		t.Fatal("debug summary score mutation changed the evaluation result")
	}

	inconsistent := []DebugEvalSummary{
		{Status: EvalStatusPassed, Evaluator: completionContractEvaluatorName, Score: ptrEvaluationScore(0), ReasonClass: "output_missing"},
		{Status: EvalStatusFailed, Evaluator: completionContractEvaluatorName, Score: ptrEvaluationScore(1), ReasonClass: "within_policy"},
		{Status: EvalStatusWarning, Evaluator: completionContractEvaluatorName, Score: ptrEvaluationScore(0), ReasonClass: "threshold_not_configured"},
	}
	for _, summary := range inconsistent {
		if got := ExposeDebugEvalSummary(CompletionContractEvaluationResult{Summary: summary}, true); got != nil {
			t.Fatalf("ExposeDebugEvalSummary(inconsistent=%#v) = %#v, want fail-closed nil", summary, got)
		}
	}
}

func TestCompletionContractEvaluatorKeepsFailureReasonWhenThresholdIsMissing(t *testing.T) {
	evaluator, err := NewCompletionContractEvaluator(CompletionContractEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "chat-completion-t074",
		MetricName: "chat_completion_contract_v1",
	})
	if err != nil {
		t.Fatalf("NewCompletionContractEvaluator() error = %v", err)
	}
	input := validEvaluationInput()
	input.OutputPresent = false

	result, err := evaluator.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.Evidence == nil || result.Evidence.RegressionStatus != aieval.RegressionStatusWarning {
		t.Fatalf("evidence = %#v, want warning because regression threshold is not configured", result.Evidence)
	}
	assertDebugEvalSummary(t, result.Summary, EvalStatusWarning, completionContractEvaluatorName, ptrEvaluationScore(0), "output_missing")
	if exposed := ExposeDebugEvalSummary(result, true); exposed == nil {
		t.Fatal("ExposeDebugEvalSummary() = nil, want bounded warning with deterministic failure reason")
	}
}

func TestCompletionContractEvaluatorRejectsUnsafeEvidenceIdentityWithoutEcho(t *testing.T) {
	threshold := 1.0
	evaluator, err := NewCompletionContractEvaluator(CompletionContractEvaluatorConfig{
		Dataset:    aieval.DatasetIdentity{Name: "chat-completion-contract", Version: "v1"},
		SampleID:   "chat-completion-t074",
		MetricName: "chat_completion_contract_v1",
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewCompletionContractEvaluator() error = %v", err)
	}
	const credentialMarker = "Bearer t094-private-identity"
	input := validEvaluationInput()
	input.Identity = obs.ApplyCorrelationOptions(input.Identity, obs.WithEvalRunID(credentialMarker))

	result, err := evaluator.Evaluate(context.Background(), input)
	if !errors.Is(err, errEvaluatorInvalidInput) {
		t.Fatalf("Evaluate() error = %v, want errEvaluatorInvalidInput", err)
	}
	if result.Evidence != nil {
		t.Fatalf("Evaluate() evidence = %#v, want nil for unsafe identity", result.Evidence)
	}
	if strings.Contains(err.Error(), credentialMarker) {
		t.Fatalf("Evaluate() error echoed credential-shaped identity: %v", err)
	}
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

func assertCompletionContractEvaluationInputIsLowSensitivity(t *testing.T) {
	t.Helper()
	allowed := map[string]struct{}{
		"Identity": {}, "ActualModel": {}, "FinishReason": {}, "Usage": {}, "OutputPresent": {},
	}
	inputType := reflect.TypeFor[CompletionContractEvaluationInput]()
	for index := range inputType.NumField() {
		field := inputType.Field(index)
		if _, ok := allowed[field.Name]; !ok {
			t.Fatalf("CompletionContractEvaluationInput must not expose raw or provider-sensitive field %q", field.Name)
		}
	}
}

func validEvaluationInput() CompletionContractEvaluationInput {
	return CompletionContractEvaluationInput{
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
