// Package observability fixes the application adapter contract for AI generation spans.
package observability

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	traceapi "go.opentelemetry.io/otel/trace"
)

func TestGenerationSpanAdapterRecordsNativeParentageAndExplicitFacts(t *testing.T) {
	tests := []struct {
		name              string
		input             GenerationSpanInput
		wantTTFT          *time.Duration
		wantFailureStatus string
		wantPayloadMode   obs.PayloadMode
		wantRedacted      bool
	}{
		{
			name:            "streaming generation records measured TTFT",
			input:           newGenerationSpanInput(ptrDuration(42*time.Millisecond), "success", "", obs.PayloadModeContentRedacted, true),
			wantTTFT:        ptrDuration(42 * time.Millisecond),
			wantPayloadMode: obs.PayloadModeContentRedacted,
			wantRedacted:    true,
		},
		{
			// nil is a fact: this non-streaming request did not observe a first token. A zero
			// duration would fabricate a latency measurement and make dashboards misleading.
			name:              "non-streaming generation omits TTFT",
			input:             newGenerationSpanInput(nil, "failed", string(obs.FailureUpstream), obs.PayloadModeMetadataOnly, false),
			wantFailureStatus: string(obs.FailureUpstream),
			wantPayloadMode:   obs.PayloadModeMetadataOnly,
			wantRedacted:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanRecorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			t.Cleanup(func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Errorf("TracerProvider.Shutdown() error = %v", err)
				}
			})
			tracer := provider.Tracer("t072-generation")
			parentContext, parent := tracer.Start(context.Background(), "ai.chat")
			defer parent.End()
			parentSpanContext := parent.SpanContext()

			adapter := NewGenerationSpanAdapter(tracer)
			platformIdentity, err := adapter.RecordGeneration(parentContext, tt.input)
			if err != nil {
				t.Fatalf("RecordGeneration() error = %v", err)
			}
			generation := onlyEndedSpanNamed(t, spanRecorder.Ended(), "ai.generation")
			assertGenerationNativeParentage(t, generation, parentSpanContext, platformIdentity)
			assertGenerationAttributes(t, generation.Attributes(), tt.input, tt.wantTTFT, tt.wantFailureStatus, tt.wantPayloadMode, tt.wantRedacted)
			assertGenerationInputDoesNotOfferRawPayload(t)
		})
	}
}

func TestGenerationSpanAdapterRejectsInvalidOutcomeFailureAndPayloadFacts(t *testing.T) {
	tests := []struct {
		name  string
		input GenerationSpanInput
	}{
		{
			name: "successful outcome cannot carry a failure status",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.FailureStatus = string(obs.FailureUpstream)
				return input
			}),
		},
		{
			name:  "failed outcome requires a stable failure status",
			input: newGenerationSpanInput(nil, "failed", "", obs.PayloadModeMetadataOnly, false),
		},
		{
			name: "unsupported outcome is rejected",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.Outcome = "arbitrary-user-status"
				return input
			}),
		},
		{
			name:  "unknown failure status is rejected",
			input: newGenerationSpanInput(nil, "failed", "arbitrary-user-status", obs.PayloadModeMetadataOnly, false),
		},
		{
			name:  "metadata-only cannot claim redacted content",
			input: newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, true),
		},
		{
			name:  "raw payload mode is not an OTel export mode",
			input: newGenerationSpanInput(nil, "success", "", obs.PayloadModeContentRaw, false),
		},
		{
			name: "prompt hash must include a sha256 digest",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.PromptHash = "sha256:"
				return input
			}),
		},
		{
			name: "prompt hash rejects non-hex digest content",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.PromptHash = "sha256:gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg"
				return input
			}),
		},
		{
			name: "prompt hash rejects a digest with the wrong length",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.PromptHash = "sha256:0123456789abcdef"
				return input
			}),
		},
		{
			name: "prompt hash rejects credential-shaped content even with a sha256 prefix",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.PromptHash = "sha256:Bearer t072-generation-private-token"
				return input
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanRecorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			parentContext, parent := provider.Tracer("t072-invalid-generation").Start(context.Background(), "ai.chat")
			defer parent.End()

			_, err := NewGenerationSpanAdapter(provider.Tracer("t072-invalid-generation")).RecordGeneration(parentContext, tt.input)
			if err == nil || len(spanRecorder.Ended()) != 0 {
				t.Fatalf("RecordGeneration() = (%v, spans:%d), want fail-fast without exported generation span", err, len(spanRecorder.Ended()))
			}
		})
	}
}

func TestGenerationSpanAdapterRejectsSensitiveFactsWithoutExportingThem(t *testing.T) {
	const userMarker = "用户原文: t072-generation-private"
	const responseMarker = "external_response: t072-generation-private"
	const credentialMarker = "Bearer t072-generation-private-token"
	tests := []struct {
		name  string
		input GenerationSpanInput
	}{
		{
			name: "provider and model facts",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.Provider = credentialMarker
				input.RequestedModel = userMarker
				input.ActualModel = responseMarker
				return input
			}),
		},
		{
			name: "prompt template version and hash facts",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.PromptTemplateVersion = userMarker
				input.PromptHash = credentialMarker
				return input
			}),
		},
		{
			name: "sha256-prefixed prompt hash cannot contain user content",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.PromptHash = "sha256:" + userMarker
				return input
			}),
		},
		{
			name: "sha256-prefixed prompt hash cannot contain credentials",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.PromptHash = "sha256:" + credentialMarker
				return input
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanRecorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			parentContext, parent := provider.Tracer("t072-private-generation").Start(context.Background(), "ai.chat")
			defer parent.End()

			_, err := NewGenerationSpanAdapter(provider.Tracer("t072-private-generation")).RecordGeneration(parentContext, tt.input)
			if err == nil || len(spanRecorder.Ended()) != 0 {
				t.Fatal("sensitive generation facts must be rejected before export")
			}
			for _, marker := range []string{userMarker, responseMarker, credentialMarker} {
				if strings.Contains(err.Error(), marker) {
					t.Fatal("generation validation error must not echo sensitive input")
				}
			}
		})
	}
}

func TestGenerationSpanAdapterRequiresAnActiveParentSpanContext(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	_, err := NewGenerationSpanAdapter(provider.Tracer("t072-missing-parent")).RecordGeneration(
		context.Background(),
		newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false),
	)
	if err == nil || len(spanRecorder.Ended()) != 0 {
		t.Fatal("generation adapter must reject a request without an active parent SpanContext")
	}
}

func newGenerationSpanInput(ttft *time.Duration, outcome, failureStatus string, payloadMode obs.PayloadMode, payloadRedacted bool) GenerationSpanInput {
	return GenerationSpanInput{
		Identity: obs.NewCorrelationIdentity(
			"req-t072-generation",
			// These deliberately forged strings prove that native OTel identity comes only from
			// the active parent SpanContext, not from domain correlation fields.
			obs.WithServiceSpan("forged-service-trace-t072", "forged-service-span-t072"),
			obs.WithAITraceID("ai-trace-t072-generation"),
		),
		Provider:              "openai-compatible",
		RequestedModel:        "server-requested-model",
		ActualModel:           "provider-actual-model",
		FinishReason:          llm.FinishLength,
		Usage:                 llm.Usage{InputTokens: 11, OutputTokens: 17, ReasoningTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2, TotalTokens: 38},
		TotalLatency:          370 * time.Millisecond,
		TTFT:                  ttft,
		Outcome:               outcome,
		FailureStatus:         failureStatus,
		PromptTemplateVersion: "chat-v1",
		PromptHash:            "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PayloadMode:           payloadMode,
		PayloadRedacted:       payloadRedacted,
	}
}

func assertGenerationNativeParentage(t *testing.T, generation sdktrace.ReadOnlySpan, parent traceapi.SpanContext, platformIdentity PlatformSpanIdentity) {
	t.Helper()
	spanContext := generation.SpanContext()
	if spanContext.TraceID() != parent.TraceID() || generation.Parent().SpanID() != parent.SpanID() {
		t.Fatalf("generation parentage = trace:%s parent:%s, want active parent trace:%s span:%s", spanContext.TraceID(), generation.Parent().SpanID(), parent.TraceID(), parent.SpanID())
	}
	if !spanContext.IsValid() || spanContext.SpanID() == parent.SpanID() || spanContext.TraceID().String() == "forged-service-trace-t072" || spanContext.SpanID().String() == "forged-service-span-t072" {
		t.Fatal("generation must have its own native OTel span identity rather than a domain-supplied value")
	}
	if platformIdentity.TraceID != spanContext.TraceID().String() || platformIdentity.SpanID != spanContext.SpanID().String() {
		t.Fatalf("returned platform identity = %#v, want native generation SpanContext", platformIdentity)
	}
	assertGenerationDoesNotLeakForgedDomainPlatformIDs(t, generation)
}

func assertGenerationAttributes(t *testing.T, values []attribute.KeyValue, input GenerationSpanInput, wantTTFT *time.Duration, wantFailureStatus string, wantPayloadMode obs.PayloadMode, wantRedacted bool) {
	t.Helper()
	attributes := generationAttributesByKey(values)
	for key, want := range map[string]string{
		"longtermism.observability.plane": "ai",
		"longtermism.ai.trace_id":         "ai-trace-t072-generation",
		"request.id":                      "req-t072-generation",
		"gen_ai.provider.name":            "openai-compatible",
		"gen_ai.request.model":            "server-requested-model",
		"gen_ai.response.model":           "provider-actual-model",
		"ai.usage.cache_read_tokens":      "3",
		"ai.usage.cache_write_tokens":     "2",
		"ai.latency.total_ms":             "370",
		"ai.outcome":                      input.Outcome,
		"ai.prompt.template_version":      "chat-v1",
		"ai.prompt.hash":                  input.PromptHash,
		"longtermism.payload.mode":        string(wantPayloadMode),
		"longtermism.payload.redacted":    boolString(wantRedacted),
	} {
		if got := attributes[key].AsString(); got != want {
			t.Fatalf("generation attribute %q = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{
		"gen_ai.usage.input_tokens":            11,
		"gen_ai.usage.output_tokens":           17,
		"gen_ai.usage.reasoning.output_tokens": 5,
	} {
		if got := attributes[key].AsInt64(); got != want {
			t.Fatalf("generation token attribute %q = %d, want %d", key, got, want)
		}
	}
	if got := attributes["gen_ai.response.finish_reasons"].AsStringSlice(); !reflect.DeepEqual(got, []string{string(llm.FinishLength)}) {
		t.Fatalf("generation finish reasons = %#v, want [%q]", got, llm.FinishLength)
	}
	if wantTTFT == nil {
		if _, exists := attributes["ai.latency.ttft_ms"]; exists {
			t.Fatal("non-streaming generation must omit ai.latency.ttft_ms rather than fabricate 0")
		}
		if _, exists := attributes["gen_ai.server.time_to_first_token"]; exists {
			t.Fatal("non-streaming generation must omit gen_ai.server.time_to_first_token")
		}
	} else {
		for _, key := range []string{"ai.latency.ttft_ms", "gen_ai.server.time_to_first_token"} {
			if got := attributes[key].AsInt64(); got != wantTTFT.Milliseconds() {
				t.Fatalf("generation TTFT attribute %q = %dms, want %dms", key, got, wantTTFT.Milliseconds())
			}
		}
	}
	if wantFailureStatus == "" {
		if _, exists := attributes["ai.failure_status"]; exists {
			t.Fatal("successful generation must not fabricate a failure status")
		}
	} else if got := attributes["ai.failure_status"].AsString(); got != wantFailureStatus {
		t.Fatalf("generation failure status = %q, want %q", got, wantFailureStatus)
	}
}

func assertGenerationInputDoesNotOfferRawPayload(t *testing.T) {
	t.Helper()
	typeOfInput := reflect.TypeFor[GenerationSpanInput]()
	allowed := map[string]struct{}{
		"Identity": {}, "Provider": {}, "RequestedModel": {}, "ActualModel": {}, "FinishReason": {}, "Usage": {},
		"TotalLatency": {}, "TTFT": {}, "Outcome": {}, "FailureStatus": {}, "PromptTemplateVersion": {}, "PromptHash": {},
		"PayloadMode": {}, "PayloadRedacted": {},
	}
	for index := range typeOfInput.NumField() {
		field := typeOfInput.Field(index)
		if _, exists := allowed[field.Name]; !exists {
			t.Fatalf("GenerationSpanInput must expose only low-sensitivity facts; unexpected field %q", field.Name)
		}
	}
}

func assertGenerationDoesNotLeakForgedDomainPlatformIDs(t *testing.T, generation sdktrace.ReadOnlySpan) {
	t.Helper()
	for _, value := range append(generation.Attributes(), generation.Resource().Attributes()...) {
		assertGenerationValueDoesNotContain(t, value.Value.AsInterface())
	}
	for _, event := range generation.Events() {
		for _, value := range event.Attributes {
			assertGenerationValueDoesNotContain(t, value.Value.AsInterface())
		}
	}
}

func assertGenerationValueDoesNotContain(t *testing.T, value any) {
	t.Helper()
	serialized := fmt.Sprint(value)
	for _, forged := range []string{"forged-service-trace-t072", "forged-service-span-t072"} {
		if strings.Contains(serialized, forged) {
			t.Fatal("domain service/span identity must not be copied into platform span data")
		}
	}
}

func deriveGenerationInput(base GenerationSpanInput, update func(GenerationSpanInput) GenerationSpanInput) GenerationSpanInput {
	return update(base)
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

func generationAttributesByKey(values []attribute.KeyValue) map[string]attribute.Value {
	attributes := make(map[string]attribute.Value, len(values))
	for _, value := range values {
		attributes[string(value.Key)] = value.Value
	}
	return attributes
}

func ptrDuration(value time.Duration) *time.Duration {
	return &value
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
