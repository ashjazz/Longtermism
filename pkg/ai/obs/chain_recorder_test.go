package obs

import (
	"context"
	"testing"
	"time"
)

func TestRequestObservationChainRecorderAssemblesOrderedStagesAndLookup(t *testing.T) {
	recorder := NewRequestObservationChainRecorder()
	identity := NewCorrelationIdentity(
		"req-chain-recorder-001",
		WithServiceSpan("svc-trace-chain-recorder-001", "span-root-001"),
		WithAITraceID("ai-trace-chain-recorder-001"),
		WithSessionID("session-chain-recorder-001"),
		WithEvalRunID("eval-run-chain-recorder-001"),
	)

	chain, err := recorder.Record(context.Background(), RequestObservationChainInput{
		Identity:           identity,
		Feature:            "rag_qa",
		StartedAt:          time.Date(2026, time.July, 3, 10, 0, 0, 0, time.UTC),
		EndedAt:            time.Date(2026, time.July, 3, 10, 0, 1, 0, time.UTC),
		OutcomeStatus:      "failure",
		FailureStatus:      string(FailureRetrievalMiss),
		OutcomeExplanation: "retriever returned no usable chunks, so agent stopped before generation",
		ServiceStages: []RequestObservationServiceStage{
			{
				Name:           "http.server.request",
				Component:      "http",
				RequestID:      identity.RequestID,
				ServiceTraceID: identity.ServiceTraceID,
				SpanID:         identity.SpanID,
				Status:         "error",
				LatencyMs:      1000,
			},
		},
		AIObservations: []RequestObservationAIStage{
			{
				ObservationID:   "obs-retriever-001",
				ObservationType: ObservationTypeRetriever,
				RequestID:       identity.RequestID,
				ServiceTraceID:  identity.ServiceTraceID,
				ParentSpanID:    identity.SpanID,
				AITraceID:       identity.AITraceID,
				OutcomeStatus:   "failure",
				FailureStatus:   string(FailureRetrievalMiss),
			},
			{
				ObservationID:   "obs-agent-001",
				ObservationType: ObservationTypeAgent,
				RequestID:       identity.RequestID,
				ServiceTraceID:  identity.ServiceTraceID,
				ParentSpanID:    identity.SpanID,
				AITraceID:       identity.AITraceID,
				OutcomeStatus:   "terminated",
				FailureStatus:   string(FailureRetrievalMiss),
			},
			{
				ObservationID:   "obs-evaluator-001",
				ObservationType: ObservationTypeEvaluator,
				RequestID:       identity.RequestID,
				ServiceTraceID:  identity.ServiceTraceID,
				ParentSpanID:    identity.SpanID,
				AITraceID:       identity.AITraceID,
				OutcomeStatus:   "success",
				FailureStatus:   string(FailureRetrievalMiss),
			},
		},
		EvalEvidence: []RequestObservationEvalEvidence{
			{
				EvalRunID:  identity.EvalRunID,
				SampleID:   "sample-retrieval-miss-001",
				MetricName: "answer_relevance",
				RequestID:  identity.RequestID,
				AITraceID:  identity.AITraceID,
			},
		},
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	assertRequestObservationChainIdentity(t, chain, identity)
	assertRequestObservationChainOutcome(t, chain, "failure", string(FailureRetrievalMiss))
	assertRequestObservationChainStageOrder(t, chain, []string{
		"service:http.server.request",
		"ai:retriever:obs-retriever-001",
		"ai:agent:obs-agent-001",
		"ai:evaluator:obs-evaluator-001",
		"eval:answer_relevance:sample-retrieval-miss-001",
	})
	assertRequestObservationChainParentLinks(t, chain, identity)

	found, ok := recorder.FindByRequestID(identity.RequestID)
	if !ok {
		t.Fatalf("FindByRequestID(%q) ok = false, want true", identity.RequestID)
	}
	if found.RequestID != identity.RequestID || found.RootAITraceID != identity.AITraceID {
		t.Fatalf("FindByRequestID() = %#v, want chain linked to request and AI trace", found)
	}
}

func TestRequestObservationChainRecorderRejectsUnexplainedOutcome(t *testing.T) {
	recorder := NewRequestObservationChainRecorder()
	identity := NewCorrelationIdentity(
		"req-chain-unexplained",
		WithServiceSpan("svc-trace-chain-unexplained", "span-chain-unexplained"),
		WithAITraceID("ai-trace-chain-unexplained"),
	)

	_, err := recorder.Record(context.Background(), RequestObservationChainInput{
		Identity:      identity,
		Feature:       "rag_qa",
		OutcomeStatus: "failure",
		ServiceStages: []RequestObservationServiceStage{
			{
				Name:           "http.server.request",
				RequestID:      identity.RequestID,
				ServiceTraceID: identity.ServiceTraceID,
				SpanID:         identity.SpanID,
				Status:         "error",
			},
		},
	})
	if err == nil {
		t.Fatal("Record() error = nil, want failure when outcome explanation is missing")
	}
}

func TestRequestObservationChainRecorderRejectsUnknownOutcome(t *testing.T) {
	recorder := NewRequestObservationChainRecorder()
	identity := NewCorrelationIdentity(
		"req-chain-unknown-outcome",
		WithServiceSpan("svc-trace-chain-unknown-outcome", "span-chain-unknown-outcome"),
		WithAITraceID("ai-trace-chain-unknown-outcome"),
	)

	_, err := recorder.Record(context.Background(), RequestObservationChainInput{
		Identity:           identity,
		Feature:            "rag_qa",
		OutcomeStatus:      "successed",
		OutcomeExplanation: "typo should not be accepted as stable outcome",
		ServiceStages: []RequestObservationServiceStage{
			{
				Name:           "http.server.request",
				RequestID:      identity.RequestID,
				ServiceTraceID: identity.ServiceTraceID,
				SpanID:         identity.SpanID,
				Status:         "success",
			},
		},
	})
	if err == nil {
		t.Fatal("Record() error = nil, want unknown outcome error")
	}
}

func assertRequestObservationChainIdentity(t *testing.T, chain RequestObservationChain, identity CorrelationIdentity) {
	t.Helper()

	for field, gotWant := range map[string][2]string{
		"RequestID":      {chain.RequestID, identity.RequestID},
		"ServiceTraceID": {chain.ServiceTraceID, identity.ServiceTraceID},
		"RootSpanID":     {chain.RootSpanID, identity.SpanID},
		"RootAITraceID":  {chain.RootAITraceID, identity.AITraceID},
		"SessionID":      {chain.SessionID, identity.SessionID},
		"EvalRunID":      {chain.EvalRunID, identity.EvalRunID},
		"Feature":        {chain.Feature, "rag_qa"},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %q, want %q", field, gotWant[0], gotWant[1])
		}
	}
}

func assertRequestObservationChainOutcome(t *testing.T, chain RequestObservationChain, wantOutcome, wantFailureStatus string) {
	t.Helper()

	if chain.OutcomeStatus != wantOutcome {
		t.Fatalf("OutcomeStatus = %q, want %q", chain.OutcomeStatus, wantOutcome)
	}
	if chain.FailureStatus != wantFailureStatus {
		t.Fatalf("FailureStatus = %q, want %q", chain.FailureStatus, wantFailureStatus)
	}
	if chain.OutcomeExplanation == "" {
		t.Fatalf("OutcomeExplanation is empty, want diagnostic explanation")
	}
}

func assertRequestObservationChainStageOrder(t *testing.T, chain RequestObservationChain, want []string) {
	t.Helper()

	if len(chain.StageRefs) != len(want) {
		t.Fatalf("StageRefs length = %d, want %d: %#v", len(chain.StageRefs), len(want), chain.StageRefs)
	}
	for index, wantRef := range want {
		if chain.StageRefs[index] != wantRef {
			t.Fatalf("StageRefs[%d] = %q, want %q", index, chain.StageRefs[index], wantRef)
		}
	}
}

func assertRequestObservationChainParentLinks(t *testing.T, chain RequestObservationChain, identity CorrelationIdentity) {
	t.Helper()

	if len(chain.ServiceStages) != 1 {
		t.Fatalf("ServiceStages length = %d, want 1", len(chain.ServiceStages))
	}
	if len(chain.AIObservations) != 3 {
		t.Fatalf("AIObservations length = %d, want 3", len(chain.AIObservations))
	}

	root := chain.ServiceStages[0]
	if root.SpanID != identity.SpanID {
		t.Fatalf("root SpanID = %q, want %q", root.SpanID, identity.SpanID)
	}
	for _, observation := range chain.AIObservations {
		if observation.ParentSpanID != root.SpanID {
			t.Fatalf("%s ParentSpanID = %q, want root span %q", observation.ObservationType, observation.ParentSpanID, root.SpanID)
		}
		if observation.RequestID != identity.RequestID {
			t.Fatalf("%s RequestID = %q, want %q", observation.ObservationType, observation.RequestID, identity.RequestID)
		}
	}
	if len(chain.EvalEvidence) != 1 {
		t.Fatalf("EvalEvidence length = %d, want 1", len(chain.EvalEvidence))
	}
	if chain.EvalEvidence[0].RequestID != identity.RequestID || chain.EvalEvidence[0].AITraceID != identity.AITraceID {
		t.Fatalf("EvalEvidence[0] = %#v, want linked request and AI trace", chain.EvalEvidence[0])
	}
}
