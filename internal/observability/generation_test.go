// Package observability fixes the application adapter contract for AI generation spans.
package observability

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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
			// A non-nil zero is still a measured fact. Pointer presence, not numeric truthiness,
			// decides whether TTFT may be exported.
			name:            "streaming generation preserves measured zero TTFT",
			input:           newGenerationSpanInput(ptrDuration(0), "success", "", obs.PayloadModeMetadataOnly, false),
			wantTTFT:        ptrDuration(0),
			wantPayloadMode: obs.PayloadModeMetadataOnly,
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
			registerTracerProviderCleanup(t, provider)
			tracer := provider.Tracer("t072-generation")
			parentContext, parent := tracer.Start(
				context.Background(),
				"ai.chat",
				traceapi.WithTimestamp(tt.input.StartedAt.Add(-time.Millisecond)),
			)
			defer parent.End(traceapi.WithTimestamp(tt.input.CompletedAt.Add(time.Millisecond)))
			parentSpanContext := parent.SpanContext()

			adapter := NewGenerationSpanAdapter(tracer)
			platformIdentity, err := adapter.RecordGeneration(parentContext, tt.input)
			if err != nil {
				t.Fatalf("RecordGeneration() error = %v", err)
			}
			generation := onlyEndedSpanNamed(t, spanRecorder.Ended(), "ai.generation")
			assertNativeParentage(t, generation, parentSpanContext, platformIdentity)
			assertGenerationAttributes(t, generation.Attributes(), tt.input, tt.wantTTFT, tt.wantFailureStatus, tt.wantPayloadMode, tt.wantRedacted)
			if got := generation.EndTime().Sub(generation.StartTime()); got != tt.input.TotalLatency {
				t.Fatalf("generation native span duration = %v, want measured total latency %v", got, tt.input.TotalLatency)
			}
			wantStatus := codes.Unset
			if tt.input.Outcome == "failed" {
				wantStatus = codes.Error
			}
			if generation.Status().Code != wantStatus {
				t.Fatalf("generation status = %v, want %v", generation.Status().Code, wantStatus)
			}
			assertGenerationInputDoesNotOfferRawPayload(t)
		})
	}
}

func TestGenerationSpanAdapterRecordsTrustedSmokeMarker(t *testing.T) {
	const marker = "run-t177-generation"
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	registerTracerProviderCleanup(t, provider)
	tracer := provider.Tracer("t177-generation")
	parentContext, parent := tracer.Start(context.Background(), "ai.chat")
	input := newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false)
	input.SmokeRunID = marker
	if _, err := NewGenerationSpanAdapter(tracer).RecordGeneration(parentContext, input); err != nil {
		t.Fatalf("RecordGeneration() error = %v", err)
	}
	parent.End()
	attributes := semanticSpanAttributesByKey(recorder.Ended()[0].Attributes())
	if got := attributes["longtermism.smoke.run_id"].AsString(); got != marker {
		t.Fatalf("generation smoke marker = %q, want %q", got, marker)
	}
}

func TestGenerationSpanAdapterMarksUnexportedIdentityAsNotProjectable(t *testing.T) {
	tests := []struct {
		name    string
		sampler sdktrace.Sampler
	}{
		{name: "drop", sampler: sdktrace.NeverSample()},
		{name: "record only", sampler: recordOnlySampler{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(tt.sampler))
			registerTracerProviderCleanup(t, provider)
			tracer := provider.Tracer("t090-unsampled-generation")
			parentContext, parent := tracer.Start(context.Background(), "ai.chat")
			defer parent.End()

			identity, err := NewGenerationSpanAdapter(tracer).RecordGeneration(
				parentContext,
				newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false),
			)
			if err != nil {
				t.Fatalf("RecordGeneration() error = %v", err)
			}
			if !identity.IsValid() || identity.CanProject() {
				t.Fatalf("unexported generation identity = %#v, want valid local correlation but no platform target", identity)
			}
		})
	}
}

type recordOnlySampler struct{}

func (recordOnlySampler) ShouldSample(sdktrace.SamplingParameters) sdktrace.SamplingResult {
	return sdktrace.SamplingResult{Decision: sdktrace.RecordOnly}
}

func (recordOnlySampler) Description() string {
	return "record-only"
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
			registerTracerProviderCleanup(t, provider)
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
			name: "request and AI trace identities",
			input: deriveGenerationInput(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false), func(input GenerationSpanInput) GenerationSpanInput {
				input.Identity.RequestID = credentialMarker
				input.Identity.AITraceID = userMarker
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
			registerTracerProviderCleanup(t, provider)
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

func TestGenerationSpanAdapterRejectsInvalidIdentityAndNumericFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(GenerationSpanInput) GenerationSpanInput
	}{
		{
			name: "feature is required",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.Feature = ""
				return input
			},
		},
		{
			name: "request identity is required",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.Identity.RequestID = ""
				return input
			},
		},
		{
			name: "AI trace identity is required",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.Identity.AITraceID = ""
				return input
			},
		},
		{
			name: "provider is required",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.Provider = ""
				return input
			},
		},
		{
			name: "requested model is required",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.RequestedModel = ""
				return input
			},
		},
		{
			name: "successful generation requires actual model",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.ActualModel = ""
				return input
			},
		},
		{
			name: "finish reason must be stable",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.FinishReason = llm.FinishReason("arbitrary")
				return input
			},
		},
		{
			name: "token counts cannot be negative",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.Usage.InputTokens = -1
				return input
			},
		},
		{
			name: "total tokens cannot be below input plus output",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.Usage.TotalTokens = input.Usage.InputTokens + input.Usage.OutputTokens - 1
				return input
			},
		},
		{
			name: "start timestamp is required",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.StartedAt = time.Time{}
				return input
			},
		},
		{
			name: "measured timestamps must match total latency",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.CompletedAt = input.CompletedAt.Add(time.Millisecond)
				return input
			},
		},
		{
			name: "total latency cannot exceed request deadline",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.TotalLatency = maxSemanticSpanDuration + time.Nanosecond
				input.CompletedAt = input.StartedAt.Add(input.TotalLatency)
				return input
			},
		},
		{
			name: "total latency cannot be negative",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.TotalLatency = -time.Millisecond
				return input
			},
		},
		{
			name: "TTFT cannot be negative",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.TTFT = ptrDuration(-time.Millisecond)
				return input
			},
		},
		{
			name: "TTFT cannot exceed total latency",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.TTFT = ptrDuration(input.TotalLatency + time.Millisecond)
				return input
			},
		},
		{
			name: "payload mode must be exportable",
			mutate: func(input GenerationSpanInput) GenerationSpanInput {
				input.PayloadMode = obs.PayloadMode("unknown")
				return input
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanRecorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			registerTracerProviderCleanup(t, provider)
			parentContext, parent := provider.Tracer("t092-invalid-boundary").Start(context.Background(), "ai.chat")
			defer parent.End()

			input := tt.mutate(newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false))
			_, err := NewGenerationSpanAdapter(provider.Tracer("t092-invalid-boundary")).RecordGeneration(parentContext, input)
			if err == nil || len(spanRecorder.Ended()) != 0 {
				t.Fatalf("RecordGeneration() = (%v, spans:%d), want fail-fast without generation span", err, len(spanRecorder.Ended()))
			}
		})
	}
}

func TestGenerationSpanAdapterRejectsNilContextOrTracer(t *testing.T) {
	input := newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false)
	if _, err := NewGenerationSpanAdapter(nil).RecordGeneration(context.Background(), input); err == nil {
		t.Fatal("nil tracer must be rejected")
	}

	provider := sdktrace.NewTracerProvider()
	registerTracerProviderCleanup(t, provider)
	if _, err := NewGenerationSpanAdapter(provider.Tracer("t092-nil-context")).RecordGeneration(nil, input); err == nil {
		t.Fatal("nil context must be rejected")
	}
}

func TestGenerationSpanAdapterRequiresAnActiveParentSpanContext(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	registerTracerProviderCleanup(t, provider)

	_, err := NewGenerationSpanAdapter(provider.Tracer("t072-missing-parent")).RecordGeneration(
		context.Background(),
		newGenerationSpanInput(nil, "success", "", obs.PayloadModeMetadataOnly, false),
	)
	if err == nil || len(spanRecorder.Ended()) != 0 {
		t.Fatal("generation adapter must reject a request without an active parent SpanContext")
	}
}

func newGenerationSpanInput(ttft *time.Duration, outcome, failureStatus string, payloadMode obs.PayloadMode, payloadRedacted bool) GenerationSpanInput {
	startedAt := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	totalLatency := 370 * time.Millisecond
	return GenerationSpanInput{
		Feature:     "chat",
		StartedAt:   startedAt,
		CompletedAt: startedAt.Add(totalLatency),
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
		TotalLatency:          totalLatency,
		TTFT:                  ttft,
		Outcome:               outcome,
		FailureStatus:         failureStatus,
		PromptTemplateVersion: "chat-v1",
		PromptHash:            "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PayloadMode:           payloadMode,
		PayloadRedacted:       payloadRedacted,
	}
}

func assertGenerationAttributes(t *testing.T, values []attribute.KeyValue, input GenerationSpanInput, wantTTFT *time.Duration, wantFailureStatus string, wantPayloadMode obs.PayloadMode, wantRedacted bool) {
	t.Helper()
	attributes := semanticSpanAttributesByKey(values)
	for key, want := range map[string]string{
		"longtermism.observability.plane": "ai",
		"longtermism.ai.trace_id":         "ai-trace-t072-generation",
		"request.id":                      "req-t072-generation",
		"gen_ai.provider.name":            "openai-compatible",
		"gen_ai.request.model":            "server-requested-model",
		"gen_ai.response.model":           "provider-actual-model",
		"ai.outcome":                      input.Outcome,
		"ai.prompt.template_version":      "chat-v1",
		"ai.prompt.hash":                  input.PromptHash,
		"longtermism.payload.mode":        string(wantPayloadMode),
	} {
		if got := attributes[key].AsString(); got != want {
			t.Fatalf("generation attribute %q = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{
		"gen_ai.usage.input_tokens":            11,
		"gen_ai.usage.output_tokens":           17,
		"gen_ai.usage.reasoning.output_tokens": 5,
		"ai.usage.cache_read_tokens":           3,
		"ai.usage.cache_write_tokens":          2,
		"ai.latency.total_ms":                  370,
	} {
		if got := attributes[key].AsInt64(); got != want {
			t.Fatalf("generation numeric attribute %q = %d, want %d", key, got, want)
		}
	}
	if got := attributes["longtermism.payload.redacted"].AsBool(); got != wantRedacted {
		t.Fatalf("generation payload redacted = %v, want %v", got, wantRedacted)
	}
	if got := attributes["gen_ai.response.finish_reasons"].AsStringSlice(); !reflect.DeepEqual(got, []string{string(llm.FinishLength)}) {
		t.Fatalf("generation finish reasons = %#v, want [%q]", got, llm.FinishLength)
	}
	if wantTTFT == nil {
		if _, exists := attributes["ai.latency.ttft_ms"]; exists {
			t.Fatal("non-streaming generation must omit ai.latency.ttft_ms rather than fabricate 0")
		}
	} else {
		value, exists := attributes["ai.latency.ttft_ms"]
		if !exists {
			t.Fatal("measured generation TTFT must export ai.latency.ttft_ms")
		}
		if got := value.AsInt64(); got != wantTTFT.Milliseconds() {
			t.Fatalf("generation TTFT = %dms, want %dms", got, wantTTFT.Milliseconds())
		}
	}
	// The official key is a seconds-based histogram metric name, not a span attribute. Metrics
	// instrumentation may record it separately; this adapter must not attach millisecond data to it.
	if _, exists := attributes["gen_ai.server.time_to_first_token"]; exists {
		t.Fatal("generation span must not misuse the GenAI TTFT metric name as an attribute")
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
		"Feature": {}, "StartedAt": {}, "CompletedAt": {}, "Identity": {}, "Provider": {}, "RequestedModel": {}, "ActualModel": {}, "FinishReason": {}, "Usage": {},
		"TotalLatency": {}, "TTFT": {}, "Outcome": {}, "FailureStatus": {}, "PromptTemplateVersion": {}, "PromptHash": {},
		"PayloadMode": {}, "PayloadRedacted": {}, "SmokeRunID": {},
	}
	for index := range typeOfInput.NumField() {
		field := typeOfInput.Field(index)
		if _, exists := allowed[field.Name]; !exists {
			t.Fatalf("GenerationSpanInput must expose only low-sensitivity facts; unexpected field %q", field.Name)
		}
	}
}

func deriveGenerationInput(base GenerationSpanInput, update func(GenerationSpanInput) GenerationSpanInput) GenerationSpanInput {
	return update(base)
}

func ptrDuration(value time.Duration) *time.Duration {
	return &value
}
