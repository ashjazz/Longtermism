package observability

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	traceapi "go.opentelemetry.io/otel/trace"
)

func assertNativeParentage(t *testing.T, span sdktrace.ReadOnlySpan, parent traceapi.SpanContext, platformIdentity PlatformSpanIdentity) {
	t.Helper()
	spanContext := span.SpanContext()
	if spanContext.TraceID() != parent.TraceID() || span.Parent().SpanID() != parent.SpanID() {
		t.Fatalf("span parentage = trace:%s parent:%s, want active parent trace:%s span:%s", spanContext.TraceID(), span.Parent().SpanID(), parent.TraceID(), parent.SpanID())
	}
	if !spanContext.IsValid() || spanContext.SpanID() == parent.SpanID() {
		t.Fatal("semantic span must have its own native OTel span identity")
	}
	if platformIdentity.TraceID != spanContext.TraceID().String() || platformIdentity.SpanID != spanContext.SpanID().String() {
		t.Fatalf("returned platform identity = %#v, want native semantic SpanContext", platformIdentity)
	}
	if !platformIdentity.CanProject() {
		t.Fatalf("recording semantic span identity = %#v, want projectable", platformIdentity)
	}
	assertDoesNotLeakForgedDomainPlatformIDs(t, span)
}

func TestPlatformSpanIdentitySeparatesStructuralValidityFromProjectionAvailability(t *testing.T) {
	const traceID = "0123456789abcdef0123456789abcdef"
	const spanID = "0123456789abcdef"
	tests := []struct {
		name        string
		identity    PlatformSpanIdentity
		wantValid   bool
		wantProject bool
	}{
		{name: "recording native identity", identity: PlatformSpanIdentity{TraceID: traceID, SpanID: spanID, Projectable: true}, wantValid: true, wantProject: true},
		{name: "head sampled identity remains structurally valid", identity: PlatformSpanIdentity{TraceID: traceID, SpanID: spanID}, wantValid: true},
		{name: "zero trace is invalid", identity: PlatformSpanIdentity{TraceID: "00000000000000000000000000000000", SpanID: spanID, Projectable: true}},
		{name: "malformed span is invalid", identity: PlatformSpanIdentity{TraceID: traceID, SpanID: "not-a-span", Projectable: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.identity.IsValid(); got != tt.wantValid {
				t.Fatalf("IsValid() = %t, want %t", got, tt.wantValid)
			}
			if got := tt.identity.CanProject(); got != tt.wantProject {
				t.Fatalf("CanProject() = %t, want %t", got, tt.wantProject)
			}
		})
	}
}

func assertDoesNotLeakForgedDomainPlatformIDs(t *testing.T, span sdktrace.ReadOnlySpan) {
	t.Helper()
	for _, value := range append(span.Attributes(), span.Resource().Attributes()...) {
		assertValueDoesNotContainForgedDomainPlatformID(t, value.Value.AsInterface())
	}
	for _, event := range span.Events() {
		for _, value := range event.Attributes {
			assertValueDoesNotContainForgedDomainPlatformID(t, value.Value.AsInterface())
		}
	}
}

func assertValueDoesNotContainForgedDomainPlatformID(t *testing.T, value any) {
	t.Helper()
	serialized := fmt.Sprint(value)
	for _, forged := range []string{
		"forged-service-trace-t072",
		"forged-service-span-t072",
		"forged-service-trace-t092",
		"forged-service-span-t092",
	} {
		if strings.Contains(serialized, forged) {
			t.Fatal("domain service/span identity must not be copied into platform span data")
		}
	}
}

func onlyEndedSpanNamed(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var matched []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			matched = append(matched, span)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("ended spans named %q = %d, want exactly one", name, len(matched))
	}
	return matched[0]
}

func semanticSpanAttributesByKey(values []attribute.KeyValue) map[string]attribute.Value {
	attributes := make(map[string]attribute.Value, len(values))
	for _, value := range values {
		attributes[string(value.Key)] = value.Value
	}
	return attributes
}

func registerTracerProviderCleanup(t *testing.T, provider *sdktrace.TracerProvider) {
	t.Helper()
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("TracerProvider.Shutdown() error = %v", err)
		}
	})
}
