package langfuse

import (
	"reflect"
	"strings"
	"testing"
	"time"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

const (
	t078PlatformTraceID       = "platform-trace-t078"
	t078PlatformObservationID = "platform-observation-t078"
)

func TestNewScoreProjectionBuildsStableIdempotentEvidenceSnapshot(t *testing.T) {
	evidence := newT078Evidence(t, "answer_relevance")
	input := newT078ProjectionInput(t, evidence, ScoreTargetKindObservation)
	input.CreatedAt = time.Date(2026, 7, 23, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	first, err := NewScoreProjection(input)
	if err != nil {
		t.Fatalf("NewScoreProjection() first error = %v", err)
	}
	input.CreatedAt = input.CreatedAt.Add(time.Hour)
	second, err := NewScoreProjection(input)
	if err != nil {
		t.Fatalf("NewScoreProjection() second error = %v", err)
	}
	if first.ProjectionID == "" || first.ProjectionID != second.ProjectionID || first.Status != ScoreProjectionStatusQueued || first.Attempt != 0 {
		t.Fatalf("new projections = %#v %#v, want stable queued idempotency projection", first, second)
	}
	if first.CreatedAt.Location() != time.UTC || !first.CreatedAt.Equal(time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("CreatedAt = %s (%s), want normalized UTC instant", first.CreatedAt, first.CreatedAt.Location())
	}

	// 投影必须拥有 evidence 的防御性快照；队列重试不能被后续评估写入篡改。
	originalThreshold := *evidence.Threshold
	*evidence.Threshold = 0.01
	evidence.MetricName = "mutated-after-projection"
	if first.Evidence.MetricName != "answer_relevance" || first.Evidence.Threshold == nil || *first.Evidence.Threshold != originalThreshold {
		t.Fatalf("projection evidence = %#v, want immutable snapshot", first.Evidence)
	}
	*first.Evidence.Threshold = 0.02
	if second.Evidence.Threshold == nil || *second.Evidence.Threshold != originalThreshold {
		t.Fatal("separate projections must not share evidence pointer state")
	}

	identityMutations := []struct {
		name   string
		mutate func(*aieval.EvaluationEvidence)
	}{
		{name: "eval run", mutate: func(value *aieval.EvaluationEvidence) { value.EvalRunID = "eval-run-t078-next" }},
		{name: "dataset name", mutate: func(value *aieval.EvaluationEvidence) { value.Dataset.Name = "chat-golden-next" }},
		{name: "dataset version", mutate: func(value *aieval.EvaluationEvidence) { value.Dataset.Version = "v2" }},
		{name: "sample", mutate: func(value *aieval.EvaluationEvidence) { value.SampleID = "sample-t078-next" }},
		{name: "request", mutate: func(value *aieval.EvaluationEvidence) { value.RequestID = "req-t078-next" }},
		{name: "AI trace", mutate: func(value *aieval.EvaluationEvidence) { value.AITraceID = "ai-trace-t078-next" }},
		{name: "service trace", mutate: func(value *aieval.EvaluationEvidence) { value.ServiceTraceID = "service-trace-t078-next" }},
		{name: "span", mutate: func(value *aieval.EvaluationEvidence) { value.SpanID = "span-t078-next" }},
		{name: "metric", mutate: func(value *aieval.EvaluationEvidence) { value.MetricName = "completion_contract" }},
	}
	for _, tt := range identityMutations {
		t.Run("id changes with "+tt.name, func(t *testing.T) {
			changed := cloneT078Evidence(second.Evidence)
			tt.mutate(&changed)
			different, err := NewScoreProjection(newT078ProjectionInput(t, changed, ScoreTargetKindObservation))
			if err != nil {
				t.Fatalf("NewScoreProjection() error = %v", err)
			}
			if different.ProjectionID == second.ProjectionID {
				t.Fatalf("changed %s must not reuse score idempotency ID", tt.name)
			}
		})
	}
}

func TestNewScoreTargetRequiresMappedPlatformIdentity(t *testing.T) {
	validSource := mappedT078TraceProjection(t)
	tests := []struct {
		name            string
		kind            ScoreTargetKind
		source          TraceProjection
		wantObservation string
		wantError       bool
	}{
		{name: "trace score permits no observation", kind: ScoreTargetKindTrace, source: validSource},
		{name: "generation score requires mapped observation", kind: ScoreTargetKindObservation, source: validSource, wantObservation: t078PlatformObservationID},
		{name: "missing mapped trace", kind: ScoreTargetKindTrace, wantError: true},
		{name: "hand assembled platform IDs have no mapper provenance", kind: ScoreTargetKindObservation, source: TraceProjection{PlatformTraceID: t078PlatformTraceID, PlatformObservationID: t078PlatformObservationID}, wantError: true},
		{name: "unknown target kind", kind: ScoreTargetKind("unknown"), source: validSource, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := NewScoreTarget(tt.source, tt.kind)
			if tt.wantError {
				if err == nil || target != (ScoreTarget{}) {
					t.Fatalf("NewScoreTarget() = (%#v, %v), want fail-fast", target, err)
				}
				assertT078ErrorDoesNotContainIdentity(t, err, newT078Evidence(t, "answer_relevance"))
				return
			}
			if err != nil || target.Kind() != tt.kind || target.PlatformTraceID() != tt.source.PlatformTraceID || target.PlatformObservationID() != tt.wantObservation {
				t.Fatalf("NewScoreTarget() = (%#v, %v), want target derived from mapped projection", target, err)
			}
		})
	}

	assertT078ProjectionTypesContainNoRawOrSecretFields(t)
}

func TestScoreProjectionStateMachinePreservesEvidence(t *testing.T) {
	base := mustNewT078Projection(t)
	wantEvidence := cloneT078Evidence(base.Evidence)
	sent := mustTransitionT078(t, mustTransitionT078(t, base, ScoreProjectionStatusSending), ScoreProjectionStatusSent)
	dropped := mustTransitionT078(t, base, ScoreProjectionStatusDroppedQueueFull)
	permanent := mustTransitionT078(t, base, ScoreProjectionStatusFailedPermanent)
	shutdown := mustTransitionT078(t, base, ScoreProjectionStatusFailedShutdownTimeout)
	retryWait := mustTransitionT078(t, mustTransitionT078(t, base, ScoreProjectionStatusSending), ScoreProjectionStatusRetryWait)
	tests := []struct {
		name        string
		start       ScoreProjection
		next        ScoreProjectionStatus
		wantStatus  ScoreProjectionStatus
		wantAttempt int
		wantError   bool
	}{
		{name: "queued to sending", start: base, next: ScoreProjectionStatusSending, wantStatus: ScoreProjectionStatusSending},
		{name: "sending to retry", start: mustTransitionT078(t, base, ScoreProjectionStatusSending), next: ScoreProjectionStatusRetryWait, wantStatus: ScoreProjectionStatusRetryWait, wantAttempt: 1},
		{name: "retry to queued", start: retryWait, next: ScoreProjectionStatusQueued, wantStatus: ScoreProjectionStatusQueued, wantAttempt: 1},
		{name: "sending to sent", start: mustTransitionT078(t, base, ScoreProjectionStatusSending), next: ScoreProjectionStatusSent, wantStatus: ScoreProjectionStatusSent},
		{name: "queued drops when full", start: base, next: ScoreProjectionStatusDroppedQueueFull, wantStatus: ScoreProjectionStatusDroppedQueueFull},
		{name: "queued fails permanently", start: base, next: ScoreProjectionStatusFailedPermanent, wantStatus: ScoreProjectionStatusFailedPermanent},
		{name: "queued times out at shutdown", start: base, next: ScoreProjectionStatusFailedShutdownTimeout, wantStatus: ScoreProjectionStatusFailedShutdownTimeout},
		{name: "queued cannot skip to sent", start: base, next: ScoreProjectionStatusSent, wantError: true},
		{name: "retry wait cannot send without queueing", start: retryWait, next: ScoreProjectionStatusSending, wantError: true},
		{name: "sent is terminal", start: sent, next: ScoreProjectionStatusSending, wantError: true},
		{name: "dropped projection is terminal", start: dropped, next: ScoreProjectionStatusSending, wantError: true},
		{name: "permanent failure is terminal", start: permanent, next: ScoreProjectionStatusSending, wantError: true},
		{name: "shutdown failure is terminal", start: shutdown, next: ScoreProjectionStatusSending, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneT078Projection(tt.start)
			got, err := tt.start.Transition(tt.next)
			if tt.wantError {
				if err == nil || !isZeroT078Projection(got) || !reflect.DeepEqual(tt.start, before) {
					t.Fatalf("Transition(%q) = (%#v, %v), want rejected immutable transition", tt.next, got, err)
				}
				assertT078ErrorDoesNotContainIdentity(t, err, tt.start.Evidence)
				return
			}
			if err != nil || got.Status != tt.wantStatus || got.Attempt != tt.wantAttempt || got.ProjectionID != base.ProjectionID || !reflect.DeepEqual(got.Evidence, wantEvidence) || !reflect.DeepEqual(tt.start, before) {
				t.Fatalf("Transition(%q) = (%#v, %v), want immutable status=%q attempt=%d", tt.next, got, err, tt.wantStatus, tt.wantAttempt)
			}
			if got.Evidence.Threshold != nil {
				*got.Evidence.Threshold = 0.01
				if tt.start.Evidence.Threshold == nil || *tt.start.Evidence.Threshold != *wantEvidence.Threshold {
					t.Fatal("transition must defensively copy evidence pointers")
				}
			}
		})
	}

	// 非终态也必须只沿图中明确的边转换；否则失败投影可能绕开队列直接发送或完成。
	nonTerminalStates := map[string]struct {
		projection ScoreProjection
		allowed    map[ScoreProjectionStatus]bool
	}{
		"queued": {
			projection: base,
			allowed: map[ScoreProjectionStatus]bool{
				ScoreProjectionStatusSending:               true,
				ScoreProjectionStatusDroppedQueueFull:      true,
				ScoreProjectionStatusFailedPermanent:       true,
				ScoreProjectionStatusFailedShutdownTimeout: true,
			},
		},
		"sending": {
			projection: mustTransitionT078(t, base, ScoreProjectionStatusSending),
			allowed: map[ScoreProjectionStatus]bool{
				ScoreProjectionStatusSent:      true,
				ScoreProjectionStatusRetryWait: true,
			},
		},
		"retry wait": {
			projection: retryWait,
			allowed: map[ScoreProjectionStatus]bool{
				ScoreProjectionStatusQueued: true,
			},
		},
	}
	allStatuses := []ScoreProjectionStatus{
		ScoreProjectionStatusQueued,
		ScoreProjectionStatusSending,
		ScoreProjectionStatusRetryWait,
		ScoreProjectionStatusSent,
		ScoreProjectionStatusDroppedQueueFull,
		ScoreProjectionStatusFailedPermanent,
		ScoreProjectionStatusFailedShutdownTimeout,
	}
	for name, current := range nonTerminalStates {
		for _, next := range allStatuses {
			t.Run(name+" transition "+string(next), func(t *testing.T) {
				before := cloneT078Projection(current.projection)
				got, err := current.projection.Transition(next)
				if current.allowed[next] {
					if err != nil || got.Status != next || !reflect.DeepEqual(current.projection, before) {
						t.Fatalf("Transition(%q) = (%#v, %v), want allowed immutable transition", next, got, err)
					}
					return
				}
				if err == nil || !isZeroT078Projection(got) || !reflect.DeepEqual(current.projection, before) {
					t.Fatalf("Transition(%q) = (%#v, %v), want rejected immutable transition", next, got, err)
				}
				assertT078ErrorDoesNotContainIdentity(t, err, current.projection.Evidence)
			})
		}
	}

	// 已发送、已丢弃、永久失败和 shutdown 失败都是终态，任何后续转换都可能造成重复写分数或错误复活。
	terminalStates := map[string]ScoreProjection{
		"sent":              sent,
		"dropped":           dropped,
		"permanent failure": permanent,
		"shutdown failure":  shutdown,
	}
	for name, terminal := range terminalStates {
		for _, next := range allStatuses {
			t.Run(name+" rejects "+string(next), func(t *testing.T) {
				before := cloneT078Projection(terminal)
				got, err := terminal.Transition(next)
				if err == nil || !isZeroT078Projection(got) || !reflect.DeepEqual(terminal, before) {
					t.Fatalf("terminal Transition(%q) = (%#v, %v), want immutable rejection", next, got, err)
				}
				assertT078ErrorDoesNotContainIdentity(t, err, terminal.Evidence)
			})
		}
	}
}

func TestScoreProjectionRejectsRetryBeyondAttemptBudget(t *testing.T) {
	input := newT078ProjectionInput(t, newT078Evidence(t, "answer_relevance"), ScoreTargetKindObservation)
	input.MaxAttempts = 1
	projection, err := NewScoreProjection(input)
	if err != nil {
		t.Fatalf("NewScoreProjection() error = %v", err)
	}
	retryWait := mustTransitionT078(t, mustTransitionT078(t, projection, ScoreProjectionStatusSending), ScoreProjectionStatusRetryWait)
	queued := mustTransitionT078(t, retryWait, ScoreProjectionStatusQueued)
	sending := mustTransitionT078(t, queued, ScoreProjectionStatusSending)
	before := cloneT078Projection(sending)
	got, err := sending.Transition(ScoreProjectionStatusRetryWait)
	if err == nil || !isZeroT078Projection(got) || !reflect.DeepEqual(sending, before) {
		t.Fatalf("Transition(retry_wait) = (%#v, %v), want retry budget rejection", got, err)
	}
	assertT078ErrorDoesNotContainIdentity(t, err, sending.Evidence)
}

func mustNewT078Projection(t *testing.T) ScoreProjection {
	t.Helper()
	projection, err := NewScoreProjection(newT078ProjectionInput(t, newT078Evidence(t, "answer_relevance"), ScoreTargetKindObservation))
	if err != nil {
		t.Fatalf("NewScoreProjection() error = %v", err)
	}
	return projection
}

func mustTransitionT078(t *testing.T, projection ScoreProjection, next ScoreProjectionStatus) ScoreProjection {
	t.Helper()
	updated, err := projection.Transition(next)
	if err != nil {
		t.Fatalf("Transition(%q) error = %v", next, err)
	}
	return updated
}

func newT078ProjectionInput(t *testing.T, evidence aieval.EvaluationEvidence, kind ScoreTargetKind) ScoreProjectionInput {
	t.Helper()
	target, err := NewScoreTarget(mappedT078TraceProjection(t), kind)
	if err != nil {
		t.Fatalf("NewScoreTarget() error = %v", err)
	}
	return ScoreProjectionInput{Target: target, Evidence: evidence, MaxAttempts: 2}
}

func newT078Evidence(t *testing.T, metric string) aieval.EvaluationEvidence {
	t.Helper()
	threshold := 0.8
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity:   obs.NewCorrelationIdentity("req-t078", obs.WithServiceSpan("service-trace-t078", "span-t078"), obs.WithAITraceID("ai-trace-t078"), obs.WithEvalRunID("eval-run-t078")),
		Dataset:    aieval.DatasetIdentity{Name: "chat-golden", Version: "v1"},
		SampleID:   "sample-t078",
		MetricName: metric,
		Score:      0.91,
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewEvaluationEvidence() error = %v", err)
	}
	return evidence
}

func mappedT078TraceProjection(t *testing.T) TraceProjection {
	t.Helper()
	projection, err := MapTraceToProjection(TraceMapperInput{
		Span: OTLPSpanSnapshot{
			TraceID:         t078PlatformTraceID,
			SpanID:          t078PlatformObservationID,
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
	return projection
}

func cloneT078Evidence(input aieval.EvaluationEvidence) aieval.EvaluationEvidence {
	cloned := input
	if input.Threshold != nil {
		threshold := *input.Threshold
		cloned.Threshold = &threshold
	}
	return cloned
}

func cloneT078Projection(input ScoreProjection) ScoreProjection {
	cloned := input
	cloned.Evidence = cloneT078Evidence(input.Evidence)
	return cloned
}

func isZeroT078Projection(projection ScoreProjection) bool {
	return projection.ProjectionID == "" && projection.Target == (ScoreTarget{}) && projection.Status == "" && projection.Attempt == 0 && reflect.DeepEqual(projection.Evidence, aieval.EvaluationEvidence{})
}

func assertT078ErrorDoesNotContainIdentity(t *testing.T, err error, evidence aieval.EvaluationEvidence) {
	t.Helper()
	for _, forbidden := range []string{
		t078PlatformTraceID,
		t078PlatformObservationID,
		evidence.EvalRunID,
		evidence.RequestID,
		evidence.AITraceID,
		evidence.ServiceTraceID,
		evidence.SpanID,
	} {
		if err != nil && strings.Contains(err.Error(), forbidden) {
			t.Fatalf("projection error must not echo identity %q", forbidden)
		}
	}
}

func assertT078ProjectionTypesContainNoRawOrSecretFields(t *testing.T) {
	t.Helper()
	for _, typeOfValue := range []reflect.Type{
		reflect.TypeFor[ScoreProjectionInput](),
		reflect.TypeFor[ScoreProjection](),
		reflect.TypeFor[ScoreTarget](),
	} {
		for index := range typeOfValue.NumField() {
			field := typeOfValue.Field(index)
			name := strings.ToLower(field.Name)
			if field.Type == reflect.TypeFor[obs.LocalRawPayload]() || strings.Contains(name, "raw") || strings.Contains(name, "secret") || strings.Contains(name, "credential") || strings.Contains(name, "authorization") {
				t.Fatalf("%s must not expose raw content or a secret-bearing field %q", typeOfValue.Name(), field.Name)
			}
			if (field.Name == "PlatformTraceID" || field.Name == "PlatformObservationID") && field.IsExported() {
				t.Fatalf("%s must not expose caller-constructible platform identity %q", typeOfValue.Name(), field.Name)
			}
		}
	}
}
