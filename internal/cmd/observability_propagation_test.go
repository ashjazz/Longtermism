package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestNewObservabilityPropagatorExtractsTraceContextAndAllowlistedBaggage(t *testing.T) {
	const (
		traceID   = "4bf92f3577b34da6a3ce929d0e0e4736"
		spanID    = "00f067aa0ba902b7"
		aiTraceID = "0123456789abcdef0123456789abcdef"
	)

	propagator := NewObservabilityPropagator()
	carrier := propagation.MapCarrier{
		"traceparent": "00-" + traceID + "-" + spanID + "-01",
		"baggage":     "session_id=session-opaque,ai_trace_id=" + aiTraceID + ",authorization=Bearer%20synthetic-t015,prompt=synthetic-private-prompt,raw_query=synthetic-private-query,unapproved_id=opaque-unapproved",
	}

	// 跨服务的实际 trace/span 只能来自 W3C TraceContext；AI 身份可关联但不能取代
	// OTel runtime identity。否则 adapter 会把领域 ID 伪造成平台可查询的 trace。
	ctx := propagator.Extract(context.Background(), carrier)
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		t.Fatal("extracted SpanContext is invalid")
	}
	if got := spanContext.TraceID().String(); got != traceID {
		t.Fatalf("TraceID = %q, want W3C traceparent ID", got)
	}
	if got := spanContext.SpanID().String(); got != spanID {
		t.Fatalf("SpanID = %q, want W3C traceparent span ID", got)
	}

	baggageValues := baggage.FromContext(ctx)
	if baggageValues.Member(obs.BaggageSessionID).Key() != "" {
		t.Fatal("session_id baggage was accepted without an explicit enable policy")
	}
	if got := baggageValues.Member(obs.BaggageAITraceID).Value(); got != aiTraceID {
		t.Fatalf("ai_trace_id baggage = %q, want explicit domain identity", got)
	}
	for _, forbiddenKey := range []string{obs.BaggageSessionID, "authorization", "prompt", "raw_query", "unapproved_id"} {
		if baggageValues.Member(forbiddenKey).Key() != "" {
			t.Fatal("forbidden or unapproved baggage key was accepted")
		}
	}
}

func TestNewObservabilityPropagatorDoesNotDeriveTraceContextFromAITraceID(t *testing.T) {
	const aiTraceID = "0123456789abcdef0123456789abcdef"

	ctx := NewObservabilityPropagator().Extract(context.Background(), propagation.MapCarrier{
		"baggage": "ai_trace_id=" + aiTraceID,
	})

	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("ai_trace_id without traceparent created a synthetic OTel SpanContext")
	}
	if got := baggage.FromContext(ctx).Member(obs.BaggageAITraceID).Value(); got != aiTraceID {
		t.Fatalf("ai_trace_id baggage = %q, want explicit domain identity retained", got)
	}
}

func TestNewObservabilityPropagatorRejectsSensitiveValuesInAllowlistedBaggage(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "session identity cannot carry a bearer value",
			key:   obs.BaggageSessionID,
			value: "Bearer%20synthetic-t015",
		},
		{
			name:  "AI domain identity cannot carry an API key shaped value",
			key:   obs.BaggageAITraceID,
			value: "sk-private-001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewObservabilityPropagator().Extract(context.Background(), propagation.MapCarrier{
				"baggage": tt.key + "=" + tt.value,
			})
			if baggage.FromContext(ctx).Member(tt.key).Key() != "" {
				t.Fatal("allowlisted baggage key retained a sensitive value")
			}

			carrier := propagation.MapCarrier{}
			NewObservabilityPropagator().Inject(ctx, carrier)
			if carrier.Get("baggage") != "" {
				t.Fatal("sensitive baggage survived extraction and was re-propagated")
			}
		})
	}
}

func TestNewObservabilityPropagatorDropsAllowedKeysWithSensitiveProperties(t *testing.T) {
	ctx := NewObservabilityPropagator().Extract(context.Background(), propagation.MapCarrier{
		"baggage": "ai_trace_id=ai-trace-opaque;authorization=Bearer%20synthetic-t027",
	})
	if baggage.FromContext(ctx).Member(obs.BaggageAITraceID).Key() != "" {
		t.Fatal("allowlisted baggage member retained an unapproved property")
	}

	carrier := propagation.MapCarrier{}
	NewObservabilityPropagator().Inject(ctx, carrier)
	if serialized := carrier.Get("baggage"); serialized != "" {
		t.Fatalf("baggage property was re-propagated: %q", serialized)
	}
}

func TestNewObservabilityPropagatorInjectsOnlyAllowlistedBaggage(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal("TraceIDFromHex() returned an unexpected error")
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal("SpanIDFromHex() returned an unexpected error")
	}

	inputBaggage := mustNewBaggage(t,
		obs.BaggageSessionID, "session-opaque",
		obs.BaggageAITraceID, "ai-trace-opaque",
		obs.BaggageServiceTraceID, "4bf92f3577b34da6a3ce929d0e0e4736",
		obs.BaggageSpanID, "00f067aa0ba902b7",
		"prompt", "synthetic-private-prompt",
		"authorization", "Bearer synthetic-t015",
	)
	ctx := baggage.ContextWithBaggage(context.Background(), inputBaggage)
	ctx = trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))

	carrier := propagation.MapCarrier{}
	NewObservabilityPropagator().Inject(ctx, carrier)

	if got := carrier.Get("traceparent"); !strings.Contains(got, traceID.String()) || !strings.Contains(got, spanID.String()) {
		t.Fatal("injected traceparent did not preserve the active SpanContext")
	}
	serializedBaggage := carrier.Get("baggage")
	if !strings.Contains(serializedBaggage, "ai_trace_id=ai-trace-opaque") {
		t.Fatal("injected baggage omitted the explicit AI domain identity")
	}
	for _, forbiddenValue := range []string{"synthetic-private-prompt", "synthetic-t015", "authorization", "prompt", "service_trace_id", "span_id", "session_id"} {
		if strings.Contains(serializedBaggage, forbiddenValue) {
			t.Fatal("injected baggage leaked forbidden content")
		}
	}
}

func TestNewObservabilityPropagatorAllowsOnlyTheAIPlaneMarker(t *testing.T) {
	ctx := NewObservabilityPropagator().Extract(context.Background(), propagation.MapCarrier{
		"baggage": "longtermism.observability.plane=ai",
	})
	if got := baggage.FromContext(ctx).Member(observabilityPlaneBaggageKey).Value(); got != "ai" {
		t.Fatalf("AI plane baggage = %q, want ai", got)
	}

	carrier := propagation.MapCarrier{}
	NewObservabilityPropagator().Inject(ctx, carrier)
	if !strings.Contains(carrier.Get("baggage"), "longtermism.observability.plane=ai") {
		t.Fatal("AI plane marker was not propagated")
	}
}

func mustNewBaggage(t *testing.T, keyValuePairs ...string) baggage.Baggage {
	t.Helper()
	if len(keyValuePairs)%2 != 0 {
		t.Fatal("mustNewBaggage requires key/value pairs")
	}

	members := make([]baggage.Member, 0, len(keyValuePairs)/2)
	for index := 0; index < len(keyValuePairs); index += 2 {
		member, err := baggage.NewMemberRaw(keyValuePairs[index], keyValuePairs[index+1])
		if err != nil {
			t.Fatal("NewMemberRaw() returned an unexpected error")
		}
		members = append(members, member)
	}
	result, err := baggage.New(members...)
	if err != nil {
		t.Fatal("baggage.New() returned an unexpected error")
	}
	return result
}
