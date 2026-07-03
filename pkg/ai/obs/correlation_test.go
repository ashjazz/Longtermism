package obs

import (
	"context"
	"testing"
)

func TestNewCorrelationIdentityAppliesCoreFieldsAndOptions(t *testing.T) {
	identity := NewCorrelationIdentity(
		"req-001",
		WithServiceSpan("svc-trace-001", "span-001"),
		WithAITraceID("ai-trace-001"),
		WithSessionID("session-001"),
		WithEvalRunID("eval-run-001"),
	)

	for field, gotWant := range map[string][2]string{
		"RequestID":      {identity.RequestID, "req-001"},
		"ServiceTraceID": {identity.ServiceTraceID, "svc-trace-001"},
		"SpanID":         {identity.SpanID, "span-001"},
		"AITraceID":      {identity.AITraceID, "ai-trace-001"},
		"SessionID":      {identity.SessionID, "session-001"},
		"EvalRunID":      {identity.EvalRunID, "eval-run-001"},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %q, want %q", field, gotWant[0], gotWant[1])
		}
	}
}

func TestApplyCorrelationOptionsDoesNotMutateBaseIdentity(t *testing.T) {
	base := CorrelationIdentity{
		RequestID:      "req-base",
		ServiceTraceID: "svc-trace-base",
		SpanID:         "span-base",
		AITraceID:      "ai-trace-base",
		SessionID:      "session-base",
		EvalRunID:      "eval-run-base",
	}

	derived := ApplyCorrelationOptions(
		base,
		WithServiceSpan("svc-trace-derived", "span-derived"),
		WithAITraceID("ai-trace-derived"),
		WithSessionID("session-derived"),
		WithEvalRunID("eval-run-derived"),
	)

	for field, gotWant := range map[string][2]string{
		"base.RequestID":      {base.RequestID, "req-base"},
		"base.ServiceTraceID": {base.ServiceTraceID, "svc-trace-base"},
		"base.SpanID":         {base.SpanID, "span-base"},
		"base.AITraceID":      {base.AITraceID, "ai-trace-base"},
		"base.SessionID":      {base.SessionID, "session-base"},
		"base.EvalRunID":      {base.EvalRunID, "eval-run-base"},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %q, want unchanged %q", field, gotWant[0], gotWant[1])
		}
	}

	for field, gotWant := range map[string][2]string{
		"derived.RequestID":      {derived.RequestID, "req-base"},
		"derived.ServiceTraceID": {derived.ServiceTraceID, "svc-trace-derived"},
		"derived.SpanID":         {derived.SpanID, "span-derived"},
		"derived.AITraceID":      {derived.AITraceID, "ai-trace-derived"},
		"derived.SessionID":      {derived.SessionID, "session-derived"},
		"derived.EvalRunID":      {derived.EvalRunID, "eval-run-derived"},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %q, want %q", field, gotWant[0], gotWant[1])
		}
	}
}

func TestApplyCorrelationOptionsIgnoresNilOptions(t *testing.T) {
	base := CorrelationIdentity{RequestID: "req-nil-option"}

	identity := ApplyCorrelationOptions(base, nil, WithAITraceID("ai-trace-001"))

	if identity.RequestID != "req-nil-option" {
		t.Fatalf("RequestID = %q, want unchanged req-nil-option", identity.RequestID)
	}
	if identity.AITraceID != "ai-trace-001" {
		t.Fatalf("AITraceID = %q, want ai-trace-001", identity.AITraceID)
	}
}

func TestCorrelationIdentityContextHelpersUseOnlyExplicitIdentity(t *testing.T) {
	ctx := context.WithValue(context.Background(), sensitiveCorrelationContextKey{}, "raw-secret-should-not-leak")
	_, ok := CorrelationIdentityFromContext(ctx)
	if ok {
		t.Fatal("CorrelationIdentityFromContext() ok = true, want false for unrelated context value")
	}

	identity := NewCorrelationIdentity(
		"req-context",
		WithServiceSpan("svc-trace-context", "span-context"),
		WithAITraceID("ai-trace-context"),
		WithSessionID("session-context"),
		WithEvalRunID("eval-run-context"),
	)
	ctx = ContextWithCorrelationIdentity(ctx, identity)
	got, ok := CorrelationIdentityFromContext(ctx)
	if !ok {
		t.Fatal("CorrelationIdentityFromContext() ok = false, want true")
	}
	if got != identity {
		t.Fatalf("CorrelationIdentityFromContext() = %#v, want %#v", got, identity)
	}

	ctx = ContextWithCorrelationIdentity(ctx, ApplyCorrelationOptions(identity, WithAITraceID("ai-trace-updated")))
	got, ok = CorrelationIdentityFromContext(ctx)
	if !ok {
		t.Fatal("CorrelationIdentityFromContext() after update ok = false, want true")
	}
	if got.AITraceID != "ai-trace-updated" {
		t.Fatalf("AITraceID = %q, want ai-trace-updated", got.AITraceID)
	}
}

type sensitiveCorrelationContextKey struct{}
