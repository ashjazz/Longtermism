package observability

import (
	"context"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	traceapi "go.opentelemetry.io/otel/trace"
)

func TestChatAIExecutionBoundaryCreatesOwnedBridgeBelowActiveRoot(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("t090-chat-boundary")
	rootContext, root := tracer.Start(context.Background(), "HTTP POST /api/v1/chat")
	rootSpanContext := root.SpanContext()
	identity := obs.NewCorrelationIdentity("req-t090-root", obs.WithAITraceID("ai-t090-root"))

	derivedContext, derivedIdentity, end, err := NewChatAIExecutionBoundary(tracer).Start(rootContext, identity)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	derivedSpanContext := traceapi.SpanContextFromContext(derivedContext)
	if derivedSpanContext.TraceID() != rootSpanContext.TraceID() ||
		derivedSpanContext.SpanID() == rootSpanContext.SpanID() {
		t.Fatal("AI execution must use a native child bridge below the active root")
	}
	if derivedIdentity.ServiceTraceID != derivedSpanContext.TraceID().String() ||
		derivedIdentity.SpanID != derivedSpanContext.SpanID().String() ||
		derivedIdentity.AITraceID != "ai-t090-root" {
		t.Fatalf("derived identity = %#v, want native bridge identity and domain AI ID", derivedIdentity)
	}
	if err := end(ChatAIExecutionOutcome{Outcome: "success"}); err != nil {
		t.Fatalf("end() error = %v", err)
	}
	ended := recorder.Ended()
	if len(ended) != 1 || ended[0].Name() != "ai.chat" {
		t.Fatalf("ended spans = %#v, want only the owned bridge", ended)
	}
	if ended[0].Parent().SpanID() != rootSpanContext.SpanID() {
		t.Fatal("bridge must retain the active root as its native parent")
	}
	root.End()
	ended = recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want bridge and HTTP root", len(ended))
	}
	assertT090ChatBoundaryAttributes(t, ended[0])
}

func TestChatAIExecutionBoundaryRecordsTrustedSmokeMarker(t *testing.T) {
	const marker = "run-t177-chat-bridge"
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("t177-chat-bridge")
	rootContext, root := tracer.Start(context.Background(), "HTTP POST /api/v1/chat")
	defer root.End()

	trustedContext := ContextWithChatSmokeRunID(rootContext, marker)
	_, _, end, err := NewChatAIExecutionBoundary(tracer).Start(
		trustedContext,
		obs.NewCorrelationIdentity("req-t177-bridge", obs.WithAITraceID("ai-t177-bridge")),
	)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := end(ChatAIExecutionOutcome{Outcome: "success"}); err != nil {
		t.Fatalf("end bridge: %v", err)
	}
	attributes := semanticSpanAttributesByKey(recorder.Ended()[0].Attributes())
	if got := attributes["longtermism.smoke.run_id"].AsString(); got != marker {
		t.Fatalf("bridge smoke marker = %q, want %q", got, marker)
	}
}

func TestChatAIExecutionBoundaryKeepsNativeIdentityWhenHeadSamplerDropsSpan(t *testing.T) {
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	identity := obs.NewCorrelationIdentity("req-t090-unsampled", obs.WithAITraceID("ai-t090-unsampled"))

	derivedContext, derivedIdentity, end, err := NewChatAIExecutionBoundary(
		provider.Tracer("t090-chat-boundary-unsampled"),
	).Start(context.Background(), identity)
	if err != nil {
		t.Fatalf("Start() error = %v, want valid native identity independent of recording decision", err)
	}
	spanContext := traceapi.SpanContextFromContext(derivedContext)
	if !spanContext.IsValid() || spanContext.IsSampled() {
		t.Fatalf("unsampled bridge SpanContext = %#v, want valid non-sampled native identity", spanContext)
	}
	if derivedIdentity.ServiceTraceID != spanContext.TraceID().String() ||
		derivedIdentity.SpanID != spanContext.SpanID().String() {
		t.Fatalf("derived identity = %#v, want unsampled native identity", derivedIdentity)
	}
	if err := end(ChatAIExecutionOutcome{Outcome: "success"}); err != nil {
		t.Fatalf("end() error = %v", err)
	}
}

func TestChatAIExecutionBoundaryCreatesAndEndsBridgeWithoutRecordingRoot(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("t090-chat-boundary")
	identity := obs.NewCorrelationIdentity(
		"req-t090-bridge",
		obs.WithServiceSpan("forged-trace", "forged-span"),
		obs.WithAITraceID("ai-t090-bridge"),
	)

	derivedContext, derivedIdentity, end, err := NewChatAIExecutionBoundary(tracer).Start(context.Background(), identity)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	bridgeSpanContext := traceapi.SpanContextFromContext(derivedContext)
	if !bridgeSpanContext.IsValid() {
		t.Fatal("bridge must expose a native SpanContext")
	}
	if derivedIdentity.ServiceTraceID != bridgeSpanContext.TraceID().String() ||
		derivedIdentity.SpanID != bridgeSpanContext.SpanID().String() ||
		derivedIdentity.ServiceTraceID == "forged-trace" ||
		derivedIdentity.SpanID == "forged-span" {
		t.Fatalf("derived identity = %#v, want native bridge identity", derivedIdentity)
	}
	if err := end(ChatAIExecutionOutcome{Outcome: "failed", FailureStatus: obs.FailureUpstream}); err != nil {
		t.Fatalf("end() error = %v", err)
	}
	ended := recorder.Ended()
	if len(ended) != 1 || ended[0].Name() != "ai.chat" {
		t.Fatalf("ended spans = %#v, want owned ai.chat bridge", ended)
	}
	assertT090ChatBoundaryAttributes(t, ended[0])
}

func assertT090ChatBoundaryAttributes(t *testing.T, span sdktrace.ReadOnlySpan) {
	t.Helper()
	attributes := make(map[string]string, len(span.Attributes()))
	for _, item := range span.Attributes() {
		attributes[string(item.Key)] = item.Value.AsString()
	}
	if attributes["longtermism.observability.plane"] != "ai" ||
		attributes["longtermism.ai.trace_id"] == "" ||
		attributes["request.id"] == "" ||
		attributes["ai.feature"] != "chat" {
		t.Fatalf("boundary attributes = %#v", attributes)
	}
}
