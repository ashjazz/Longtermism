package smoke

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

const (
	observabilityChainFeature    = "observability_chain_smoke"
	observabilityChainMetricName = "answer_relevance"
	observabilityChainStartedAt  = "2026-07-03T10:00:00Z"
	observabilityChainEndedAt    = "2026-07-03T10:00:01Z"
)

// ObservabilityChainScenario 标识离线完整请求链路 smoke 覆盖的生产故障路径。
type ObservabilityChainScenario string

const (
	ObservabilityChainScenarioSuccess         ObservabilityChainScenario = "success"
	ObservabilityChainScenarioUpstreamFailure ObservabilityChainScenario = "upstream_failure"
	ObservabilityChainScenarioRetrievalMiss   ObservabilityChainScenario = "retrieval_miss"
	ObservabilityChainScenarioToolError       ObservabilityChainScenario = "tool_error"
	ObservabilityChainScenarioLoopDetected    ObservabilityChainScenario = "loop_detected"
	ObservabilityChainScenarioBudgetExceeded  ObservabilityChainScenario = "budget_exceeded"
	ObservabilityChainScenarioDegraded        ObservabilityChainScenario = "degraded"
)

// ObservabilityChainSmokeConfig 描述一次默认离线完整观测链路 smoke。
type ObservabilityChainSmokeConfig struct {
	Scenario       ObservabilityChainScenario
	RequestID      string
	ServiceTraceID string
	SpanID         string
	AITraceID      string
	EvalRunID      string
	SampleID       string
}

// ObservabilityChainServiceStage 是 smoke 对外暴露的基础设施阶段快照。
type ObservabilityChainServiceStage = obs.RequestObservationServiceStage

// ObservabilityChainAIObservation 是 smoke 对外暴露的 AI 语义阶段快照。
type ObservabilityChainAIObservation = obs.RequestObservationAIStage

// ObservabilityChainEvalEvidence 是 smoke 对外暴露的 eval evidence 快照。
type ObservabilityChainEvalEvidence = obs.RequestObservationEvalEvidence

// ObservabilityChainSmokeResult 是一次请求的完整离线观测链路。
type ObservabilityChainSmokeResult struct {
	RequestID          string
	ServiceTraceID     string
	RootSpanID         string
	RootAITraceID      string
	EvalRunID          string
	OutcomeStatus      string
	FailureStatus      string
	OutcomeExplanation string
	ServiceStages      []ObservabilityChainServiceStage
	AIObservations     []ObservabilityChainAIObservation
	EvalEvidence       []ObservabilityChainEvalEvidence
}

// RunObservabilityChainSmoke 构造一条不依赖真实平台的完整请求观测链路。
//
// 这条 smoke 的目标是验证“双平面关联”本身：基础 span、AI 语义阶段和 eval
// evidence 能否通过稳定身份互相回查。它不拨打 OTel collector、Langfuse 或真实模型。
func RunObservabilityChainSmoke(ctx context.Context, config ObservabilityChainSmokeConfig) (ObservabilityChainSmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	scenario, err := buildObservabilityChainScenario(config.Scenario)
	if err != nil {
		return ObservabilityChainSmokeResult{}, err
	}

	identity := obs.NewCorrelationIdentity(
		strings.TrimSpace(config.RequestID),
		obs.WithServiceSpan(strings.TrimSpace(config.ServiceTraceID), strings.TrimSpace(config.SpanID)),
		obs.WithAITraceID(strings.TrimSpace(config.AITraceID)),
		obs.WithEvalRunID(strings.TrimSpace(config.EvalRunID)),
	)
	if _, err := obs.BuildDualPlaneLinks(obs.DualPlaneLinkInput{
		Identity:        identity,
		AIObservationID: "obs-generation-" + scenario.id,
		EvalSampleID:    strings.TrimSpace(config.SampleID),
	}); err != nil {
		return ObservabilityChainSmokeResult{}, err
	}

	chain, err := obs.NewRequestObservationChainRecorder().Record(ctx, obs.RequestObservationChainInput{
		Identity:           identity,
		Feature:            observabilityChainFeature,
		StartedAt:          mustObservabilityChainTime(observabilityChainStartedAt),
		EndedAt:            mustObservabilityChainTime(observabilityChainEndedAt),
		OutcomeStatus:      scenario.outcomeStatus,
		FailureStatus:      scenario.failureStatus,
		OutcomeExplanation: scenario.explanation,
		ServiceStages:      buildObservabilityChainServiceStages(identity, scenario),
		AIObservations:     buildObservabilityChainAIObservations(identity, scenario),
		EvalEvidence:       buildObservabilityChainEvalEvidence(identity, strings.TrimSpace(config.SampleID)),
	})
	if err != nil {
		return ObservabilityChainSmokeResult{}, err
	}

	return ObservabilityChainSmokeResult{
		RequestID:          chain.RequestID,
		ServiceTraceID:     chain.ServiceTraceID,
		RootSpanID:         chain.RootSpanID,
		RootAITraceID:      chain.RootAITraceID,
		EvalRunID:          chain.EvalRunID,
		OutcomeStatus:      chain.OutcomeStatus,
		FailureStatus:      chain.FailureStatus,
		OutcomeExplanation: chain.OutcomeExplanation,
		ServiceStages:      append([]ObservabilityChainServiceStage(nil), chain.ServiceStages...),
		AIObservations:     append([]ObservabilityChainAIObservation(nil), chain.AIObservations...),
		EvalEvidence:       append([]ObservabilityChainEvalEvidence(nil), chain.EvalEvidence...),
	}, nil
}

type observabilityChainScenarioSpec struct {
	id             string
	outcomeStatus  string
	failureStatus  string
	explanation    string
	observationSet []obs.ObservationType
}

func buildObservabilityChainScenario(scenario ObservabilityChainScenario) (observabilityChainScenarioSpec, error) {
	switch scenario {
	case ObservabilityChainScenarioSuccess:
		return newObservabilityChainScenarioSpec(scenario, "success", "", []obs.ObservationType{
			obs.ObservationTypeGeneration,
			obs.ObservationTypeRetriever,
			obs.ObservationTypeTool,
			obs.ObservationTypeAgent,
			obs.ObservationTypeEvaluator,
		}), nil
	case ObservabilityChainScenarioUpstreamFailure:
		return newObservabilityChainScenarioSpec(scenario, "failure", "upstream_failure", []obs.ObservationType{
			obs.ObservationTypeGeneration,
			obs.ObservationTypeAgent,
			obs.ObservationTypeEvaluator,
		}), nil
	case ObservabilityChainScenarioRetrievalMiss:
		return newObservabilityChainScenarioSpec(scenario, "failure", string(obs.FailureRetrievalMiss), []obs.ObservationType{
			obs.ObservationTypeGeneration,
			obs.ObservationTypeRetriever,
			obs.ObservationTypeAgent,
			obs.ObservationTypeEvaluator,
		}), nil
	case ObservabilityChainScenarioToolError:
		return newObservabilityChainScenarioSpec(scenario, "failure", "tool_error", []obs.ObservationType{
			obs.ObservationTypeGeneration,
			obs.ObservationTypeTool,
			obs.ObservationTypeAgent,
			obs.ObservationTypeEvaluator,
		}), nil
	case ObservabilityChainScenarioLoopDetected:
		return newObservabilityChainScenarioSpec(scenario, "terminated", string(obs.FailureLoopDetected), []obs.ObservationType{
			obs.ObservationTypeGeneration,
			obs.ObservationTypeAgent,
			obs.ObservationTypeEvaluator,
		}), nil
	case ObservabilityChainScenarioBudgetExceeded:
		return newObservabilityChainScenarioSpec(scenario, "terminated", string(obs.FailureBudgetExceeded), []obs.ObservationType{
			obs.ObservationTypeGeneration,
			obs.ObservationTypeAgent,
			obs.ObservationTypeEvaluator,
		}), nil
	case ObservabilityChainScenarioDegraded:
		return newObservabilityChainScenarioSpec(scenario, "degraded", "upstream_failure", []obs.ObservationType{
			obs.ObservationTypeGeneration,
			obs.ObservationTypeRetriever,
			obs.ObservationTypeAgent,
			obs.ObservationTypeEvaluator,
		}), nil
	default:
		return observabilityChainScenarioSpec{}, fmt.Errorf("observability chain scenario %q is not supported", scenario)
	}
}

func newObservabilityChainScenarioSpec(scenario ObservabilityChainScenario, outcomeStatus, failureStatus string, observationSet []obs.ObservationType) observabilityChainScenarioSpec {
	id := string(scenario)
	return observabilityChainScenarioSpec{
		id:             id,
		outcomeStatus:  outcomeStatus,
		failureStatus:  failureStatus,
		explanation:    "observability chain smoke records " + id + " outcome with linked service, AI and eval evidence",
		observationSet: append([]obs.ObservationType(nil), observationSet...),
	}
}

func buildObservabilityChainServiceStages(identity obs.CorrelationIdentity, scenario observabilityChainScenarioSpec) []ObservabilityChainServiceStage {
	return []ObservabilityChainServiceStage{
		{
			Name:           "http.server.request",
			Component:      "http",
			RequestID:      identity.RequestID,
			ServiceTraceID: identity.ServiceTraceID,
			SpanID:         identity.SpanID,
			Status:         serviceStageStatusForOutcome(scenario.outcomeStatus),
			ErrorClass:     scenario.failureStatus,
			LatencyMs:      1000,
		},
	}
}

func buildObservabilityChainAIObservations(identity obs.CorrelationIdentity, scenario observabilityChainScenarioSpec) []ObservabilityChainAIObservation {
	observations := make([]ObservabilityChainAIObservation, 0, len(scenario.observationSet))
	for _, observationType := range scenario.observationSet {
		observations = append(observations, ObservabilityChainAIObservation{
			ObservationID:   "obs-" + observationType.String() + "-" + scenario.id,
			ObservationType: observationType,
			RequestID:       identity.RequestID,
			ServiceTraceID:  identity.ServiceTraceID,
			ParentSpanID:    identity.SpanID,
			AITraceID:       identity.AITraceID,
			OutcomeStatus:   observationOutcomeForType(observationType, scenario),
			FailureStatus:   observationFailureForType(observationType, scenario),
		})
	}
	return observations
}

func buildObservabilityChainEvalEvidence(identity obs.CorrelationIdentity, sampleID string) []ObservabilityChainEvalEvidence {
	return []ObservabilityChainEvalEvidence{
		{
			EvalRunID:  identity.EvalRunID,
			SampleID:   sampleID,
			MetricName: observabilityChainMetricName,
			RequestID:  identity.RequestID,
			AITraceID:  identity.AITraceID,
		},
	}
}

func serviceStageStatusForOutcome(outcomeStatus string) string {
	if outcomeStatus == "success" || outcomeStatus == "degraded" {
		return "success"
	}
	return "error"
}

func observationOutcomeForType(observationType obs.ObservationType, scenario observabilityChainScenarioSpec) string {
	if observationType == obs.ObservationTypeEvaluator {
		return "success"
	}
	return scenario.outcomeStatus
}

func observationFailureForType(observationType obs.ObservationType, scenario observabilityChainScenarioSpec) string {
	if observationType == obs.ObservationTypeEvaluator {
		return ""
	}
	return scenario.failureStatus
}

func mustObservabilityChainTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
