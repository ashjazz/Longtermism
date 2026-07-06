package smoke

import (
	"context"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

func TestObservabilityChainSmokeCoversRequestOutcomes(t *testing.T) {
	tests := []struct {
		name                 string
		scenario             ObservabilityChainScenario
		wantOutcome          string
		wantFailureStatus    string
		wantObservationTypes []obs.ObservationType
	}{
		{
			name:        "success links service AI and eval evidence",
			scenario:    ObservabilityChainScenarioSuccess,
			wantOutcome: "success",
			wantObservationTypes: []obs.ObservationType{
				obs.ObservationTypeGeneration,
				obs.ObservationTypeRetriever,
				obs.ObservationTypeTool,
				obs.ObservationTypeAgent,
				obs.ObservationTypeEvaluator,
			},
		},
		{
			name:              "upstream failure stays traceable",
			scenario:          ObservabilityChainScenarioUpstreamFailure,
			wantOutcome:       "failure",
			wantFailureStatus: "upstream_failure",
			wantObservationTypes: []obs.ObservationType{
				obs.ObservationTypeGeneration,
				obs.ObservationTypeAgent,
				obs.ObservationTypeEvaluator,
			},
		},
		{
			name:              "retrieval miss explains failed request",
			scenario:          ObservabilityChainScenarioRetrievalMiss,
			wantOutcome:       "failure",
			wantFailureStatus: string(obs.FailureRetrievalMiss),
			wantObservationTypes: []obs.ObservationType{
				obs.ObservationTypeGeneration,
				obs.ObservationTypeRetriever,
				obs.ObservationTypeAgent,
				obs.ObservationTypeEvaluator,
			},
		},
		{
			name:              "tool error links tool observation",
			scenario:          ObservabilityChainScenarioToolError,
			wantOutcome:       "failure",
			wantFailureStatus: "tool_error",
			wantObservationTypes: []obs.ObservationType{
				obs.ObservationTypeGeneration,
				obs.ObservationTypeTool,
				obs.ObservationTypeAgent,
				obs.ObservationTypeEvaluator,
			},
		},
		{
			name:              "loop detected terminates agent chain",
			scenario:          ObservabilityChainScenarioLoopDetected,
			wantOutcome:       "terminated",
			wantFailureStatus: string(obs.FailureLoopDetected),
			wantObservationTypes: []obs.ObservationType{
				obs.ObservationTypeGeneration,
				obs.ObservationTypeAgent,
				obs.ObservationTypeEvaluator,
			},
		},
		{
			name:              "budget exceeded terminates agent chain",
			scenario:          ObservabilityChainScenarioBudgetExceeded,
			wantOutcome:       "terminated",
			wantFailureStatus: string(obs.FailureBudgetExceeded),
			wantObservationTypes: []obs.ObservationType{
				obs.ObservationTypeGeneration,
				obs.ObservationTypeAgent,
				obs.ObservationTypeEvaluator,
			},
		},
		{
			name:              "degraded request keeps fallback evidence",
			scenario:          ObservabilityChainScenarioDegraded,
			wantOutcome:       "degraded",
			wantFailureStatus: "upstream_failure",
			wantObservationTypes: []obs.ObservationType{
				obs.ObservationTypeGeneration,
				obs.ObservationTypeRetriever,
				obs.ObservationTypeAgent,
				obs.ObservationTypeEvaluator,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestID := "req-chain-" + string(tt.scenario)
			serviceTraceID := "svc-trace-chain-" + string(tt.scenario)
			spanID := "span-chain-" + string(tt.scenario)
			aiTraceID := "ai-trace-chain-" + string(tt.scenario)
			evalRunID := "eval-run-chain-" + string(tt.scenario)

			// 这条 smoke 是双平面关联层的契约入口：它不关心真实 OTel 或 Langfuse
			// 是否可用，只要求本地结果能从 request_id 还原基础入口、AI 阶段、
			// eval evidence 和最终 outcome 解释。
			result, err := RunObservabilityChainSmoke(context.Background(), ObservabilityChainSmokeConfig{
				Scenario:       tt.scenario,
				RequestID:      requestID,
				ServiceTraceID: serviceTraceID,
				SpanID:         spanID,
				AITraceID:      aiTraceID,
				EvalRunID:      evalRunID,
				SampleID:       "sample-" + string(tt.scenario),
			})
			if err != nil {
				t.Fatalf("RunObservabilityChainSmoke() error = %v", err)
			}

			assertObservabilityChainIdentity(t, result, requestID, serviceTraceID, spanID, aiTraceID, evalRunID)
			assertObservabilityChainOutcome(t, result, tt.wantOutcome, tt.wantFailureStatus)
			assertObservabilityChainServiceStage(t, result, requestID, serviceTraceID, spanID)
			assertObservabilityChainObservationTypes(t, result, requestID, serviceTraceID, spanID, aiTraceID, tt.wantObservationTypes)
			assertObservabilityChainEvalEvidence(t, result, requestID, aiTraceID, evalRunID)
		})
	}
}

func assertObservabilityChainIdentity(t *testing.T, result ObservabilityChainSmokeResult, requestID, serviceTraceID, spanID, aiTraceID, evalRunID string) {
	t.Helper()

	if result.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", result.RequestID, requestID)
	}
	if result.ServiceTraceID != serviceTraceID {
		t.Fatalf("ServiceTraceID = %q, want %q", result.ServiceTraceID, serviceTraceID)
	}
	if result.RootSpanID != spanID {
		t.Fatalf("RootSpanID = %q, want %q", result.RootSpanID, spanID)
	}
	if result.RootAITraceID != aiTraceID {
		t.Fatalf("RootAITraceID = %q, want %q", result.RootAITraceID, aiTraceID)
	}
	if result.EvalRunID != evalRunID {
		t.Fatalf("EvalRunID = %q, want %q", result.EvalRunID, evalRunID)
	}
}

func assertObservabilityChainOutcome(t *testing.T, result ObservabilityChainSmokeResult, wantOutcome, wantFailureStatus string) {
	t.Helper()

	if result.OutcomeStatus != wantOutcome {
		t.Fatalf("OutcomeStatus = %q, want %q", result.OutcomeStatus, wantOutcome)
	}
	if result.FailureStatus != wantFailureStatus {
		t.Fatalf("FailureStatus = %q, want %q", result.FailureStatus, wantFailureStatus)
	}
	if result.OutcomeExplanation == "" {
		t.Fatalf("OutcomeExplanation is empty, want diagnostic explanation")
	}
}

func assertObservabilityChainServiceStage(t *testing.T, result ObservabilityChainSmokeResult, requestID, serviceTraceID, spanID string) {
	t.Helper()

	if len(result.ServiceStages) == 0 {
		t.Fatalf("ServiceStages is empty, want HTTP/service entry")
	}

	root := result.ServiceStages[0]
	if root.Name != "http.server.request" {
		t.Fatalf("root service stage name = %q, want http.server.request", root.Name)
	}
	if root.RequestID != requestID || root.ServiceTraceID != serviceTraceID || root.SpanID != spanID {
		t.Fatalf("root service stage identity = %#v, want request/service/span ids", root)
	}
}

func assertObservabilityChainObservationTypes(t *testing.T, result ObservabilityChainSmokeResult, requestID, serviceTraceID, parentSpanID, aiTraceID string, wantTypes []obs.ObservationType) {
	t.Helper()

	gotByType := make(map[obs.ObservationType]ObservabilityChainAIObservation, len(result.AIObservations))
	for _, observation := range result.AIObservations {
		gotByType[observation.ObservationType] = observation
	}

	for _, wantType := range wantTypes {
		observation, ok := gotByType[wantType]
		if !ok {
			t.Fatalf("AIObservations missing %q in %#v", wantType, result.AIObservations)
		}
		if observation.RequestID != requestID {
			t.Fatalf("%s RequestID = %q, want %q", wantType, observation.RequestID, requestID)
		}
		if observation.ServiceTraceID != serviceTraceID {
			t.Fatalf("%s ServiceTraceID = %q, want %q", wantType, observation.ServiceTraceID, serviceTraceID)
		}
		if observation.ParentSpanID != parentSpanID {
			t.Fatalf("%s ParentSpanID = %q, want %q", wantType, observation.ParentSpanID, parentSpanID)
		}
		if observation.AITraceID != aiTraceID {
			t.Fatalf("%s AITraceID = %q, want %q", wantType, observation.AITraceID, aiTraceID)
		}
	}
}

func assertObservabilityChainEvalEvidence(t *testing.T, result ObservabilityChainSmokeResult, requestID, aiTraceID, evalRunID string) {
	t.Helper()

	if len(result.EvalEvidence) == 0 {
		t.Fatalf("EvalEvidence is empty, want evaluator evidence linked to request and AI trace")
	}

	evidence := result.EvalEvidence[0]
	if evidence.EvalRunID != evalRunID {
		t.Fatalf("EvalRunID = %q, want %q", evidence.EvalRunID, evalRunID)
	}
	if evidence.RequestID != requestID {
		t.Fatalf("evidence RequestID = %q, want %q", evidence.RequestID, requestID)
	}
	if evidence.AITraceID != aiTraceID {
		t.Fatalf("evidence AITraceID = %q, want %q", evidence.AITraceID, aiTraceID)
	}
	if evidence.SampleID == "" {
		t.Fatalf("SampleID is empty, want sample identity")
	}
}
