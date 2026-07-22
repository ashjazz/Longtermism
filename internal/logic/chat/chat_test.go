// Package chat fixes the application-layer contract for the first non-streaming chat flow.
//
// The controller owns HTTP validation and envelopes, while this usecase owns the point where
// a chat becomes an AI request. In particular, ai_trace_id must exist before the provider can
// fail, but it remains a domain identity rather than an OTel or platform trace identifier.
package chat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestChatUsecaseReturnsProviderFactsAndCreatesAIIdentityBeforeProviderCall(t *testing.T) {
	provider := &scriptedProvider{
		chat: func(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
			identity, ok := obs.CorrelationIdentityFromContext(ctx)
			if !ok {
				t.Fatal("provider context must carry the correlation identity")
			}
			if identity.AITraceID != "ai-t070-success" {
				t.Fatalf("AITraceID at provider call = %q, want identity created before the call", identity.AITraceID)
			}
			if identity.ServiceTraceID != "otel-trace-t070" || identity.SpanID != "otel-span-t070" {
				t.Fatalf("provider identity = %#v, want existing service identities preserved", identity)
			}
			if request.Model != "server-configured-model" {
				t.Fatalf("provider request model = %q, want server-owned configured model", request.Model)
			}
			if !reflect.DeepEqual(request.Messages, []llm.Message{{Role: llm.RoleUser, Content: "Explain the evidence loop."}}) {
				t.Fatalf("provider request messages = %#v", request.Messages)
			}
			return &llm.ChatResponse{
				Content:      "Observe facts, evaluate evidence, then regress changes.",
				Model:        "provider-actual-model",
				FinishReason: llm.FinishLength,
				Usage: llm.Usage{
					InputTokens:      11,
					OutputTokens:     17,
					ReasoningTokens:  5,
					CacheReadTokens:  3,
					CacheWriteTokens: 2,
					TotalTokens:      38,
				},
			}, nil
		},
	}
	usecase := NewChatUsecase(ChatUsecaseDependencies{
		Provider:       provider,
		RequestedModel: "server-configured-model",
		NewAITraceID:   func() string { return "ai-t070-success" },
	})
	ctx := obs.ContextWithCorrelationIdentity(context.Background(), obs.NewCorrelationIdentity(
		"req-t070-success",
		obs.WithServiceSpan("otel-trace-t070", "otel-span-t070"),
		obs.WithAITraceID("untrusted-ai-trace-id"),
	))

	result, err := usecase.Execute(ctx, ChatCommand{Message: "Explain the evidence loop."})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "Observe facts, evaluate evidence, then regress changes." {
		t.Fatalf("result content = %q", result.Content)
	}
	if result.Model != "provider-actual-model" {
		t.Fatalf("result model = %q, want actual provider model rather than requested model", result.Model)
	}
	if result.FinishReason != llm.FinishLength {
		t.Fatalf("result finish reason = %q, want %q", result.FinishReason, llm.FinishLength)
	}
	wantUsage := llm.Usage{InputTokens: 11, OutputTokens: 17, ReasoningTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2, TotalTokens: 38}
	if result.Usage != wantUsage {
		t.Fatalf("result usage = %#v, want %#v", result.Usage, wantUsage)
	}
	wantIdentity := obs.NewCorrelationIdentity("req-t070-success", obs.WithServiceSpan("otel-trace-t070", "otel-span-t070"), obs.WithAITraceID("ai-t070-success"))
	if result.Identity != wantIdentity {
		t.Fatalf("result identity = %#v, want %#v", result.Identity, wantIdentity)
	}
}

// Provider failures happen after the usecase has created its domain identity. Keeping both the
// stable cause and the identity lets the controller select 502/429/504 without manufacturing a
// new correlation value after a failure has already occurred.
func TestChatUsecasePreservesFailureClassAndAIIdentity(t *testing.T) {
	tests := []struct {
		name   string
		cause  error
		wantIs []error
	}{
		{name: "upstream unavailable", cause: fmt.Errorf("provider unavailable: %w", llm.ErrUpstream), wantIs: []error{llm.ErrUpstream}},
		{name: "provider rate limited", cause: errors.Join(llm.ErrUpstream, llm.ErrRateLimit), wantIs: []error{llm.ErrUpstream, llm.ErrRateLimit}},
		{name: "provider timeout", cause: errors.Join(llm.ErrUpstream, context.DeadlineExceeded), wantIs: []error{llm.ErrUpstream, context.DeadlineExceeded}},
		{name: "sanitizes provider detail", cause: errors.Join(llm.ErrUpstream, errors.New("provider body forbidden-provider-detail-t070 api-key=forbidden-key-t070")), wantIs: []error{llm.ErrUpstream}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &scriptedProvider{
				chat: func(ctx context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
					identity, ok := obs.CorrelationIdentityFromContext(ctx)
					if !ok || identity.AITraceID != "ai-t070-failure" {
						t.Fatalf("provider must observe a pre-created AI identity, got %#v present=%t", identity, ok)
					}
					return nil, tt.cause
				},
			}
			usecase := NewChatUsecase(ChatUsecaseDependencies{
				Provider:       provider,
				RequestedModel: "server-configured-model",
				NewAITraceID:   func() string { return "ai-t070-failure" },
			})
			ctx := obs.ContextWithCorrelationIdentity(context.Background(), obs.NewCorrelationIdentity("req-t070-failure"))

			result, err := usecase.Execute(ctx, ChatCommand{Message: "Will this retain correlation on failure?"})
			if err == nil {
				t.Fatal("Execute() error = nil, want preserved provider failure classification")
			}
			for _, want := range tt.wantIs {
				if !errors.Is(err, want) {
					t.Fatalf("Execute() error must preserve errors.Is(_, %v)", want)
				}
			}
			if strings.Contains(err.Error(), "forbidden-provider-detail-t070") || strings.Contains(err.Error(), "forbidden-key-t070") {
				t.Fatal("usecase error must not include raw provider details or credentials")
			}
			if result.Identity.RequestID != "req-t070-failure" || result.Identity.AITraceID != "ai-t070-failure" {
				t.Fatalf("failure result identity = %#v, want request and pre-created AI identity", result.Identity)
			}
		})
	}
}

// The usecase is a telemetry producer. It must export only correlation and model facts, never
// the raw user message or provider output that passes through its business result.
func TestChatUsecaseRecordsOnlyLowSensitivityTelemetryFacts(t *testing.T) {
	const messageMarker = "forbidden-user-message-t070"
	const outputMarker = "forbidden-provider-output-t070"
	telemetry := &recordingTelemetry{}
	usecase := NewChatUsecase(ChatUsecaseDependencies{
		Provider: &scriptedProvider{chat: func(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{Content: outputMarker, Model: "provider-actual-model", FinishReason: llm.FinishStop, Usage: llm.Usage{InputTokens: 13, OutputTokens: 7, TotalTokens: 20}}, nil
		}},
		RequestedModel: "server-configured-model",
		NewAITraceID:   func() string { return "ai-t070-private" },
		Telemetry:      telemetry,
	})
	ctx := obs.ContextWithCorrelationIdentity(context.Background(), obs.NewCorrelationIdentity("req-t070-private", obs.WithServiceSpan("otel-trace-private", "otel-span-private")))

	if _, err := usecase.Execute(ctx, ChatCommand{Message: messageMarker}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(telemetry.traces) != 1 {
		t.Fatalf("telemetry trace count = %d, want one", len(telemetry.traces))
	}
	trace := telemetry.traces[0]
	if trace.TraceID != "ai-t070-private" || trace.RequestID != "req-t070-private" || trace.ServiceTraceID != "otel-trace-private" || trace.SpanID != "otel-span-private" {
		t.Fatal("telemetry trace must retain explicit correlation identities")
	}
	if trace.Model != "provider-actual-model" || trace.InputTokens != 13 || trace.OutputTokens != 7 || trace.OutcomeStatus != "success" {
		t.Fatal("telemetry trace must preserve low-sensitivity provider outcome facts")
	}
	serialized := fmt.Sprintf("%#v", trace)
	if strings.Contains(serialized, messageMarker) || strings.Contains(serialized, outputMarker) {
		t.Fatal("telemetry trace must not contain raw user input or provider output")
	}
}

// IDs belong to an execution, not a usecase instance. Reusing one cached value would merge
// independent chat trajectories and their later evaluation evidence into the wrong trace.
func TestChatUsecaseGeneratesFreshAIIdentityForEachExecution(t *testing.T) {
	var providerAITraceIDs []string
	provider := &scriptedProvider{chat: func(ctx context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
		identity, ok := obs.CorrelationIdentityFromContext(ctx)
		if !ok {
			t.Fatal("provider context must contain a generated identity")
		}
		providerAITraceIDs = append(providerAITraceIDs, identity.AITraceID)
		return &llm.ChatResponse{Model: "provider-actual-model", FinishReason: llm.FinishStop}, nil
	}}
	ids := []string{"ai-t070-first", "ai-t070-second"}
	usecase := NewChatUsecase(ChatUsecaseDependencies{
		Provider:       provider,
		RequestedModel: "server-configured-model",
		NewAITraceID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
	ctx := obs.ContextWithCorrelationIdentity(context.Background(), obs.NewCorrelationIdentity("req-t070-repeat"))

	first, firstErr := usecase.Execute(ctx, ChatCommand{Message: "first"})
	second, secondErr := usecase.Execute(ctx, ChatCommand{Message: "second"})
	if firstErr != nil || secondErr != nil {
		t.Fatalf("Execute() errors = first:%v second:%v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(providerAITraceIDs, []string{"ai-t070-first", "ai-t070-second"}) {
		t.Fatalf("provider AI identities = %#v, want a fresh ID per execution", providerAITraceIDs)
	}
	if first.Identity.AITraceID != "ai-t070-first" || second.Identity.AITraceID != "ai-t070-second" {
		t.Fatal("results must retain their own execution-specific AI identities")
	}
}

func TestChatUsecaseRecordsSanitizedFailureTelemetry(t *testing.T) {
	const messageMarker = "forbidden-failure-message-t070"
	const providerMarker = "forbidden-failure-provider-detail-t070"
	telemetry := &recordingTelemetry{}
	usecase := NewChatUsecase(ChatUsecaseDependencies{
		Provider: &scriptedProvider{chat: func(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, errors.Join(llm.ErrUpstream, errors.New(providerMarker+" api-key=forbidden-failure-key-t070"))
		}},
		RequestedModel: "server-configured-model",
		NewAITraceID:   func() string { return "ai-t070-failure-private" },
		Telemetry:      telemetry,
	})
	ctx := obs.ContextWithCorrelationIdentity(context.Background(), obs.NewCorrelationIdentity("req-t070-failure-private"))

	result, err := usecase.Execute(ctx, ChatCommand{Message: messageMarker})
	if err == nil {
		t.Fatal("Execute() error = nil, want provider failure")
	}
	if !errors.Is(err, llm.ErrUpstream) {
		t.Fatal("failure must retain the upstream sentinel for resilience handling")
	}
	if strings.Contains(err.Error(), messageMarker) || strings.Contains(err.Error(), providerMarker) || strings.Contains(err.Error(), "forbidden-failure-key-t070") {
		t.Fatal("business error must not expose raw user input, provider failure detail, or credentials")
	}
	if result.Identity.AITraceID != "ai-t070-failure-private" || len(telemetry.traces) != 1 {
		t.Fatal("provider failure must retain its generated identity and record one telemetry fact")
	}
	trace := telemetry.traces[0]
	if trace.TraceID != "ai-t070-failure-private" || trace.OutcomeStatus != string(obs.FailureUpstream) {
		t.Fatal("failure telemetry must retain the identity and stable upstream failure class")
	}
	serialized := fmt.Sprintf("%#v", trace)
	if strings.Contains(serialized, messageMarker) || strings.Contains(serialized, providerMarker) || strings.Contains(serialized, "forbidden-failure-key-t070") {
		t.Fatal("failure telemetry must not contain raw user input, provider detail, or credentials")
	}
}

// A telemetry exporter is a side channel. Its explicit delivery failure must remain diagnosable
// without converting a completed model response into an upstream failure or dropping model facts.
func TestChatUsecaseKeepsBusinessResultWhenTelemetryFails(t *testing.T) {
	provider := &scriptedProvider{
		chat: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Content:      "business result survives telemetry failure",
				Model:        "provider-actual-model",
				FinishReason: llm.FinishStop,
				Usage:        llm.Usage{InputTokens: 2, OutputTokens: 5, TotalTokens: 7},
			}, nil
		},
	}
	telemetry := &failingTelemetry{err: errors.New("synthetic exporter unavailable")}
	diagnostics := &recordingTelemetryDiagnostics{}
	usecase := NewChatUsecase(ChatUsecaseDependencies{
		Provider:       provider,
		RequestedModel: "server-configured-model",
		NewAITraceID:   func() string { return "ai-t070-telemetry" },
		Telemetry:      telemetry,
		Diagnostics:    diagnostics,
	})
	ctx := obs.ContextWithCorrelationIdentity(context.Background(), obs.NewCorrelationIdentity("req-t070-telemetry"))

	result, err := usecase.Execute(ctx, ChatCommand{Message: "Do telemetry failures change this?"})
	if err != nil {
		t.Fatalf("Execute() error = %v, want telemetry failure isolated", err)
	}
	if telemetry.calls != 1 {
		t.Fatalf("telemetry record calls = %d, want one attempted record", telemetry.calls)
	}
	if !reflect.DeepEqual(diagnostics.failures, []ChatTelemetryFailure{{Component: "generation", ErrorClass: "telemetry_export_failed"}}) {
		t.Fatalf("telemetry diagnostics = %#v, want one low-sensitivity failure fact", diagnostics.failures)
	}
	if result.Content != "business result survives telemetry failure" || result.Model != "provider-actual-model" || result.Usage.TotalTokens != 7 {
		t.Fatalf("business result = %#v, want unchanged provider facts", result)
	}
	if result.Identity.AITraceID != "ai-t070-telemetry" {
		t.Fatalf("business result identity = %#v, want the pre-created AI identity", result.Identity)
	}
}

type scriptedProvider struct {
	chat func(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error)
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Capabilities(string) llm.ProviderCapabilities {
	return llm.ProviderCapabilities{}
}

func (p *scriptedProvider) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	return p.chat(ctx, request)
}

func (p *scriptedProvider) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	return nil, errors.New("streaming is outside the non-streaming chat usecase contract")
}

type recordingTelemetry struct {
	traces []obs.Trace
}

func (t *recordingTelemetry) Record(_ context.Context, trace obs.Trace) error {
	t.traces = append(t.traces, trace)
	return nil
}

type failingTelemetry struct {
	calls int
	err   error
}

func (t *failingTelemetry) Record(context.Context, obs.Trace) error {
	t.calls++
	return t.err
}

type recordingTelemetryDiagnostics struct {
	failures []ChatTelemetryFailure
}

func (d *recordingTelemetryDiagnostics) RecordTelemetryFailure(_ context.Context, failure ChatTelemetryFailure) {
	d.failures = append(d.failures, failure)
}
