package observability

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	traceapi "go.opentelemetry.io/otel/trace"
)

const evaluatorSpanName = "ai.evaluator"

// EvaluatorSpanInput 把业务 feature 与稳定 evidence 显式组合。
//
// feature 不属于 EvaluationEvidence 的评估身份，但属于 span 的业务路由事实；
// adapter 不能根据包名、route 或调用位置把它猜成 chat。
type EvaluatorSpanInput struct {
	SmokeRunID  string
	Feature     string
	StartedAt   time.Time
	CompletedAt time.Time
	Evidence    aieval.EvaluationEvidence
}

// EvaluatorSpanAdapter 把本地 EvaluationEvidence 投影为 evaluator span。
//
// adapter 直接消费稳定 evidence，避免在应用观测层重复定义 metric、score、
// threshold 和 regression status 的第二套事实模型。
type EvaluatorSpanAdapter struct {
	tracer traceapi.Tracer
}

// NewEvaluatorSpanAdapter 创建 evaluator adapter。
func NewEvaluatorSpanAdapter(tracer traceapi.Tracer) *EvaluatorSpanAdapter {
	return &EvaluatorSpanAdapter{tracer: tracer}
}

// RecordEvaluator 在活动 generation/bridge parent 下记录低敏评估证据。
func (adapter *EvaluatorSpanAdapter) RecordEvaluator(ctx context.Context, input EvaluatorSpanInput) (PlatformSpanIdentity, error) {
	if err := validateSpanRuntime(ctx, evaluatorAdapterTracer(adapter)); err != nil {
		return PlatformSpanIdentity{}, err
	}
	input = cloneEvaluatorSpanInput(input)
	if err := validateEvaluatorSpanInput(input); err != nil {
		return PlatformSpanIdentity{}, err
	}
	evidence := input.Evidence

	identity := obs.NewCorrelationIdentity(
		evidence.RequestID,
		obs.WithAITraceID(evidence.AITraceID),
		obs.WithEvalRunID(evidence.EvalRunID),
	)
	routingAttributes, err := obs.MapSpanRoutingAttributes(obs.SpanRoutingInput{
		Role:     obs.SpanRoutingRoleAIEvaluator,
		Identity: identity,
		Feature:  input.Feature,
	})
	if err != nil {
		return PlatformSpanIdentity{}, errors.New("evaluator span routing facts are invalid")
	}

	return recordNativeChildSpan(
		ctx,
		adapter.tracer,
		evaluatorSpanName,
		evaluatorSpanAttributes(input, routingAttributes),
		codes.Unset,
		"",
		nativeSpanTiming{
			Start: input.StartedAt,
			End:   input.CompletedAt,
		},
	)
}

func evaluatorAdapterTracer(adapter *EvaluatorSpanAdapter) traceapi.Tracer {
	if adapter == nil {
		return nil
	}
	return adapter.tracer
}

func cloneEvaluatorSpanInput(input EvaluatorSpanInput) EvaluatorSpanInput {
	cloned := input
	if input.Evidence.Threshold != nil {
		threshold := *input.Evidence.Threshold
		cloned.Evidence.Threshold = &threshold
	}
	return cloned
}

func validateEvaluatorSpanInput(input EvaluatorSpanInput) error {
	if input.SmokeRunID != "" && !isSafeObservationIdentifier(input.SmokeRunID) {
		return errors.New("evaluator smoke run identity is invalid")
	}
	evidence := input.Evidence
	if input.StartedAt.IsZero() || input.CompletedAt.IsZero() || input.CompletedAt.Before(input.StartedAt) {
		return errors.New("evaluator timing boundary is invalid")
	}
	if input.CompletedAt.Sub(input.StartedAt) > maxSemanticSpanDuration {
		return errors.New("evaluator duration exceeds the allowed boundary")
	}
	textFacts := map[string]string{
		"ai.feature":                input.Feature,
		"longtermism.eval.run_id":   evidence.EvalRunID,
		"request.id":                evidence.RequestID,
		"longtermism.ai.trace_id":   evidence.AITraceID,
		"ai.eval.dataset.name":      evidence.Dataset.Name,
		"ai.eval.dataset.version":   evidence.Dataset.Version,
		"ai.eval.sample_id":         evidence.SampleID,
		"ai.eval.metric.name":       evidence.MetricName,
		"ai.eval.regression_status": string(evidence.RegressionStatus),
	}
	for key, value := range textFacts {
		if !isSafeObservationIdentifier(value) {
			return fmt.Errorf("evaluator span required fact %s is invalid", key)
		}
	}
	if len(obs.ScanForbiddenPayloadFields(textFacts)) > 0 {
		return errors.New("evaluator span contains unsafe text facts")
	}
	if math.IsNaN(evidence.Score) || math.IsInf(evidence.Score, 0) || evidence.Score < 0 || evidence.Score > 1 {
		return errors.New("evaluator score must be within the unit interval")
	}
	if evidence.Threshold != nil {
		threshold := *evidence.Threshold
		if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
			return errors.New("evaluator threshold must be within the unit interval")
		}
	}
	if evidence.RegressionStatus != expectedRegressionStatus(evidence.Score, evidence.Threshold) {
		return errors.New("evaluator regression status is inconsistent with score facts")
	}
	return nil
}

func expectedRegressionStatus(score float64, threshold *float64) aieval.RegressionStatus {
	if threshold == nil {
		return aieval.RegressionStatusWarning
	}
	if score >= *threshold {
		return aieval.RegressionStatusPassed
	}
	return aieval.RegressionStatusFailed
}

func evaluatorSpanAttributes(input EvaluatorSpanInput, routing map[string]string) []attribute.KeyValue {
	evidence := input.Evidence
	attributes := routingOTelAttributes(routing)
	attributes = append(attributes,
		attribute.String("longtermism.eval.run_id", evidence.EvalRunID),
		attribute.String("ai.eval.dataset.name", evidence.Dataset.Name),
		attribute.String("ai.eval.dataset.version", evidence.Dataset.Version),
		attribute.String("ai.eval.sample_id", evidence.SampleID),
		attribute.String("ai.eval.metric.name", evidence.MetricName),
		attribute.Float64("ai.eval.score", evidence.Score),
		attribute.String("ai.eval.regression_status", string(evidence.RegressionStatus)),
	)
	if evidence.Threshold != nil {
		attributes = append(attributes, attribute.Float64("ai.eval.threshold", *evidence.Threshold))
	}
	if input.SmokeRunID != "" {
		attributes = append(attributes, attribute.String("longtermism.smoke.run_id", input.SmokeRunID))
	}
	return attributes
}
