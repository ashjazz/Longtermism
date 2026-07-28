package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appeval "github.com/ashjazz/Longtermism/internal/eval"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestBuildChatRuntimeDoesNoProviderOrFilesystemWorkWhenDisabled(t *testing.T) {
	buildCalls := 0
	openCalls := 0
	runtime, err := BuildChatRuntime(
		context.Background(),
		ChatRuntimeConfig{Enabled: false},
		&ObservabilityBootstrap{},
		ChatRuntimeDependencies{
			BuildProvider: func(context.Context, LLMProviderConfigInput, LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
				buildCalls++
				return offlineLLMProvider{}, LLMProviderConfigSnapshot{}, nil
			},
			OpenEvidence: func(appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error) {
				openCalls++
				return &chatRuntimeEvidenceStoreStub{}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildChatRuntime() disabled error = %v", err)
	}
	if runtime.Enabled || runtime.Handler != nil || buildCalls != 0 || openCalls != 0 {
		t.Fatalf("disabled runtime = enabled:%t handler:%t build:%d open:%d, want zero-side-effect gate", runtime.Enabled, runtime.Handler != nil, buildCalls, openCalls)
	}
}

func TestBuildDefaultChatRuntimeKeepsCheckedInDisabledConfigurationOffline(t *testing.T) {
	runtime, err := buildDefaultChatRuntime(context.Background(), &ObservabilityBootstrap{})
	if err != nil {
		t.Fatalf("buildDefaultChatRuntime() error = %v", err)
	}
	if runtime.Enabled || runtime.Handler != nil {
		t.Fatalf("default runtime enabled=%t handler=%t, want checked-in offline gate", runtime.Enabled, runtime.Handler != nil)
	}
}

func TestBuildChatRuntimeBuildsOneProviderAndOwnsOnlyEvidenceLifecycle(t *testing.T) {
	buildCalls := 0
	store := &chatRuntimeEvidenceStoreStub{}
	config := validChatRuntimeConfig()
	runtime, err := BuildChatRuntime(
		context.Background(),
		config,
		initializedChatTestBootstrap(t),
		ChatRuntimeDependencies{
			BuildProvider: func(_ context.Context, input LLMProviderConfigInput, _ LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
				buildCalls++
				if !input.ChatEnabled {
					t.Fatal("enabled runtime must explicitly enable provider construction")
				}
				return offlineLLMProvider{}, LLMProviderConfigSnapshot{
					Enabled:      true,
					Provider:     "openai",
					DefaultModel: "model-t096",
				}, nil
			},
			OpenEvidence: func(input appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error) {
				if input != config.Evidence {
					t.Fatalf("evidence config = %#v, want %#v", input, config.Evidence)
				}
				return store, nil
			},
			NewAITraceID: func() string { return "ai-t096" },
			NewEvalRunID: func() string { return "eval-t096" },
		},
	)
	if err != nil {
		t.Fatalf("BuildChatRuntime() error = %v", err)
	}
	if buildCalls != 1 || !runtime.Enabled || runtime.Handler == nil || runtime.Limit != config.RateLimit {
		t.Fatalf("runtime = build:%d enabled:%t handler:%t limit:%#v", buildCalls, runtime.Enabled, runtime.Handler != nil, runtime.Limit)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("evidence Close calls = %d, want 1", store.closeCalls)
	}
}

func TestBuildChatRuntimeRejectsSemanticConfigurationBeforeExternalConstruction(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChatRuntimeConfig)
	}{
		{name: "prompt identity missing", mutate: func(config *ChatRuntimeConfig) { config.PromptTemplateVersion = "" }},
		{name: "prompt identity mismatched", mutate: func(config *ChatRuntimeConfig) { config.PromptTemplateVersion = "other-v2" }},
		{name: "route limit invalid", mutate: func(config *ChatRuntimeConfig) { config.RateLimit.Rate = 0 }},
		{name: "threshold invalid", mutate: func(config *ChatRuntimeConfig) { config.EvalThreshold = 0 }},
		{name: "payload policy invalid", mutate: func(config *ChatRuntimeConfig) { config.PayloadMode = "unknown" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validChatRuntimeConfig()
			tt.mutate(&config)
			buildCalls := 0
			_, err := BuildChatRuntime(context.Background(), config, initializedChatTestBootstrap(t), ChatRuntimeDependencies{
				BuildProvider: func(context.Context, LLMProviderConfigInput, LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
					buildCalls++
					return offlineLLMProvider{}, LLMProviderConfigSnapshot{}, nil
				},
			})
			if err == nil || buildCalls != 0 {
				t.Fatalf("invalid config error=%v build_calls=%d, want fail-fast before provider", err, buildCalls)
			}
		})
	}
}

func TestBuildChatRuntimeFailsClosedAcrossConstructionBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		context   context.Context
		bootstrap *ObservabilityBootstrap
		mutate    func(*ChatRuntimeConfig)
		build     func(context.Context, LLMProviderConfigInput, LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error)
		open      func(appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error)
	}{
		{name: "nil context", context: nil, bootstrap: initializedChatTestBootstrap(t)},
		{name: "nil bootstrap", context: context.Background(), bootstrap: nil},
		{name: "inactive telemetry lifecycle", context: context.Background(), bootstrap: &ObservabilityBootstrap{}},
		{
			name: "provider failure", context: context.Background(), bootstrap: initializedChatTestBootstrap(t),
			build: func(context.Context, LLMProviderConfigInput, LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
				return nil, LLMProviderConfigSnapshot{}, errors.New("private provider failure")
			},
		},
		{
			name: "invalid provider snapshot", context: context.Background(), bootstrap: initializedChatTestBootstrap(t),
			build: func(context.Context, LLMProviderConfigInput, LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
				return offlineLLMProvider{}, LLMProviderConfigSnapshot{}, nil
			},
		},
		{
			name: "invalid evaluator identity", context: context.Background(), bootstrap: initializedChatTestBootstrap(t),
			mutate: func(config *ChatRuntimeConfig) { config.Dataset.Name = "" },
		},
		{
			name: "evidence open failure", context: context.Background(), bootstrap: initializedChatTestBootstrap(t),
			open: func(appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error) {
				return nil, errors.New("private filesystem failure")
			},
		},
		{
			name: "nil evidence store", context: context.Background(), bootstrap: initializedChatTestBootstrap(t),
			open: func(appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error) {
				return nil, nil
			},
		},
	}
	validBuild := func(context.Context, LLMProviderConfigInput, LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
		return offlineLLMProvider{}, LLMProviderConfigSnapshot{Enabled: true, Provider: "openai", DefaultModel: "model-t096"}, nil
	}
	validOpen := func(appeval.LocalEvidenceStoreConfig) (chatRuntimeEvidenceStore, error) {
		return &chatRuntimeEvidenceStoreStub{}, nil
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validChatRuntimeConfig()
			if tt.mutate != nil {
				tt.mutate(&config)
			}
			build := tt.build
			if build == nil {
				build = validBuild
			}
			buildCalls := 0
			countedBuild := func(ctx context.Context, input LLMProviderConfigInput, dependencies LLMProviderDependencies) (llm.Provider, LLMProviderConfigSnapshot, error) {
				buildCalls++
				return build(ctx, input, dependencies)
			}
			open := tt.open
			if open == nil {
				open = validOpen
			}
			if _, err := BuildChatRuntime(tt.context, config, tt.bootstrap, ChatRuntimeDependencies{
				BuildProvider: countedBuild,
				OpenEvidence:  open,
			}); err == nil {
				t.Fatal("BuildChatRuntime() error = nil, want fail closed")
			}
			if tt.name == "invalid evaluator identity" && buildCalls != 0 {
				t.Fatalf("provider build calls = %d, want evaluator validation before secret/provider boundary", buildCalls)
			}
		})
	}
}

func TestNewChatHTTPHandlerAdaptsStandardHTTPResponses(t *testing.T) {
	server := newChatRouteTestServer(t)
	server.BindHandler("POST:/t096/standard", newChatHTTPHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Context() == nil {
			t.Fatal("standard handler request context is nil")
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("created"))
	})))
	server.BindHandler("POST:/t096/nil", newChatHTTPHandler(nil))

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/t096/standard", nil))
	if response.Code != http.StatusCreated || response.Body.String() != "created" {
		t.Fatalf("standard adapter response = %d %q, want 201 created", response.Code, response.Body.String())
	}
	nilResponse := httptest.NewRecorder()
	server.ServeHTTP(nilResponse, httptest.NewRequest(http.MethodPost, "/t096/nil", nil))
	if nilResponse.Code != http.StatusInternalServerError {
		t.Fatalf("nil handler response = %d, want 500", nilResponse.Code)
	}
}

func TestChatRuntimeHelpersKeepIdentityAndModelFactsExplicit(t *testing.T) {
	firstID := newChatExecutionID()
	secondID := newChatExecutionID()
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("chat execution IDs = %q,%q, want distinct opaque identities", firstID, secondID)
	}
	canonicalize := exactConfiguredModel("model-t096")
	if canonical, ok := canonicalize("model-t096"); !ok || canonical != "model-t096" {
		t.Fatalf("canonical model = %q,%t, want configured model", canonical, ok)
	}
	if canonical, ok := canonicalize("untrusted-model"); ok || canonical != "" {
		t.Fatalf("untrusted model = %q,%t, want rejected", canonical, ok)
	}
	if exportedChatPayloadMode(obs.PayloadModeContentRaw) != obs.PayloadModeContentRedacted {
		t.Fatal("raw local payload must be represented as redacted at the export boundary")
	}
	if exportedChatPayloadMode(obs.PayloadModeMetadataOnly) != obs.PayloadModeMetadataOnly {
		t.Fatal("metadata-only payload mode must remain unchanged")
	}
	if bootstrapHTTPMiddleware(&ObservabilityBootstrap{}) != nil || bootstrapHTTPMiddleware(nil) != nil {
		t.Fatal("empty bootstrap middleware must remain nil")
	}
	if allowed, err := allowChatRequest(context.Background(), nil); err == nil || allowed {
		t.Fatalf("nil limiter result = %t,%v, want fail closed", allowed, err)
	}
	dependencies := defaultChatRuntimeDependencies(ChatRuntimeDependencies{})
	if dependencies.BuildProvider == nil ||
		dependencies.OpenEvidence == nil ||
		dependencies.NewAITraceID == nil ||
		dependencies.NewEvalRunID == nil ||
		dependencies.RequestIDFromCtx == nil ||
		dependencies.TracerName == "" {
		t.Fatal("default chat runtime dependencies are incomplete")
	}
	if err := (*ChatRuntime)(nil).Close(); err != nil {
		t.Fatalf("nil runtime Close() error = %v", err)
	}
}

func validChatRuntimeConfig() ChatRuntimeConfig {
	return ChatRuntimeConfig{
		Enabled:               true,
		RateLimit:             ChatRateLimitConfig{Rate: 10, Period: time.Minute},
		PromptTemplateVersion: chatPromptTemplateVersion,
		PayloadMode:           obs.PayloadModeMetadataOnly,
		Dataset:               aieval.DatasetIdentity{Name: "dataset-t096", Version: "v1"},
		SampleID:              "sample-t096",
		MetricName:            "completion-contract",
		EvalThreshold:         1,
		Evidence:              appeval.LocalEvidenceStoreConfig{Path: "unused-t096.jsonl", Retention: time.Hour},
	}
}

func initializedChatTestBootstrap(t *testing.T) *ObservabilityBootstrap {
	t.Helper()
	lifecycle := NewObservabilityProviderLifecycle(ObservabilityProviderLifecycleConfig{})
	if err := lifecycle.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize test telemetry lifecycle: %v", err)
	}
	if !lifecycle.Status().Initialized {
		t.Fatal("test telemetry lifecycle did not initialize")
	}
	return &ObservabilityBootstrap{Lifecycle: lifecycle}
}

type chatRuntimeEvidenceStoreStub struct {
	closeCalls int
}

func (*chatRuntimeEvidenceStoreStub) Append(context.Context, aieval.EvaluationEvidence) error {
	return nil
}

func (store *chatRuntimeEvidenceStoreStub) Close() error {
	store.closeCalls++
	return nil
}
