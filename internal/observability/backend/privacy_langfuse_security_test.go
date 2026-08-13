package backend

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	appobs "github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestPrivacyLangfuseSurfacesReuseLoopbackNoProxyNoRedirectTransport(t *testing.T) {
	request := t191Request(smoke.PrivacySmokeSurfaceLangfuseTrace)
	trace, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: "http://127.0.0.1:1", Credential: t191Credential})
	if err != nil {
		t.Fatal(err)
	}
	score, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: "http://127.0.0.1:1", Credential: t191Credential, ProjectionStore: &t191ProjectionStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if traceTransport, ok := trace.query.httpClient.Transport.(*http.Transport); !ok || traceTransport.Proxy != nil {
		t.Fatal("trace transport can use environment proxy")
	}
	if scoreTransport, ok := score.query.httpClient.Transport.(*http.Transport); !ok || scoreTransport.Proxy != nil {
		t.Fatal("score transport can use environment proxy")
	}

	t.Run("dial-time DNS revalidation", func(t *testing.T) {
		serverCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { serverCalls++ }))
		defer server.Close()
		localhostURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
		resolveCalls := 0
		resolve := func(context.Context, string) ([]net.IP, error) {
			resolveCalls++
			if resolveCalls <= 3 {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
			return []net.IP{net.ParseIP("192.0.2.10")}, nil
		}
		trace, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: localhostURL, Credential: t191Credential, ResolveHost: resolve})
		if err != nil {
			t.Fatal(err)
		}
		score, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: localhostURL, Credential: t191Credential, ResolveHost: resolve, ProjectionStore: &t191ProjectionStore{records: []localeval.ScoreProjectionSnapshot{t191Snapshot(request)}}})
		if err != nil {
			t.Fatal(err)
		}
		adapter, err := newPrivacyLangfuseSurfacesForTest(trace, score)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = adapter.Scan(context.Background(), request); err == nil || serverCalls != 0 || resolveCalls < 4 {
			t.Fatal("DNS rebinding reached Langfuse")
		}
	})

	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		t.Run(string(surface)+" redirect", func(t *testing.T) {
			request := t191Request(surface)
			targetCalls := 0
			targetAuthorization := ""
			target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, incoming *http.Request) {
				targetCalls++
				targetAuthorization = incoming.Header.Get("Authorization")
			}))
			defer target.Close()
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				writer.Header().Set("Location", target.URL)
				writer.WriteHeader(http.StatusTemporaryRedirect)
			})
			adapter, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			if _, err := adapter.Scan(context.Background(), request); err == nil || targetCalls != 0 || targetAuthorization != "" {
				t.Fatal("protected client followed redirect")
			}
		})
	}
}

func TestPrivacyLangfuseProtectedClientsRejectUnsafeConfiguration(t *testing.T) {
	unsafe := []struct{ baseURL, credential string }{
		{"http://127.0.0.1:1", ""}, {"http://127.0.0.1:1", "   "},
		{"http://192.0.2.10:3000", t191Credential}, {"http://user@127.0.0.1:3000", t191Credential},
		{"http://127.0.0.1:3000/path", t191Credential}, {"http://127.0.0.1:3000?query=1", t191Credential},
		{"http://127.0.0.1:3000#fragment", t191Credential},
	}
	for _, tt := range unsafe {
		if client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: tt.baseURL, Credential: tt.credential}); err == nil || client != nil {
			t.Fatal("unsafe trace client configuration was accepted")
		}
		if client, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{
			BaseURL: tt.baseURL, Credential: tt.credential, ProjectionStore: &t191ProjectionStore{},
		}); err == nil || client != nil {
			t.Fatal("unsafe score client configuration was accepted")
		}
	}
}

func TestPrivacyLangfuseSurfacesRejectMalformedBoundedOrUnsafeResponses(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		request := t191Request(surface)
		for _, tt := range []struct {
			name   string
			status int
			body   string
		}{
			{"authentication", http.StatusUnauthorized, t191Raw}, {"upstream", http.StatusBadGateway, t191Raw},
			{"malformed", http.StatusOK, `{"data":`}, {"trailing", http.StatusOK, `{} {}`},
			{"oversize", http.StatusOK, `{"value":"` + strings.Repeat("a", maximumBackendResponseSize) + `"}`},
		} {
			t.Run(string(surface)+"/"+tt.name, func(t *testing.T) {
				handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
					writer.WriteHeader(tt.status)
					_, _ = writer.Write([]byte(tt.body))
				})
				adapter, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
				defer closeServer()
				evidence, err := adapter.Scan(context.Background(), request)
				if err == nil || !reflect.ValueOf(evidence).IsZero() {
					t.Fatal("unsafe Langfuse response became evidence")
				}
				assertT191LowSensitive(t, err, request)
			})
		}
	}
}

func TestPrivacyLangfuseFailuresDoNotExposeEndpointQueryOrPlatformBody(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		t.Run(string(surface), func(t *testing.T) {
			request := t191Request(surface)
			rawQuery := ""
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				rawQuery = incoming.URL.RawQuery
				writer.WriteHeader(http.StatusBadGateway)
				_, _ = writer.Write([]byte(t191Raw))
			}))
			defer server.Close()
			trace, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: server.URL, Credential: t191Credential})
			if err != nil {
				t.Fatal(err)
			}
			store := &t191ProjectionStore{records: []localeval.ScoreProjectionSnapshot{t191Snapshot(request)}}
			score, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: t191Credential, ProjectionStore: store})
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := newPrivacyLangfuseSurfacesForTest(trace, score)
			if err != nil {
				t.Fatal(err)
			}
			if evidence, scanErr := adapter.Scan(context.Background(), request); scanErr == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("transport failure became evidence")
			} else {
				decodedQuery, _ := url.QueryUnescape(rawQuery)
				assertT191LowSensitiveValues(t, scanErr, request, server.URL, rawQuery, decodedQuery)
			}
		})
	}
}

func TestPrivacyLangfuseEvidenceIsSealedAndConstructorIsConcrete(t *testing.T) {
	constructor := reflect.TypeOf(NewPrivacyLangfuseSurfaces)
	want := reflect.TypeOf((func(*LangfuseChatSmokeQueryClient, *LangfuseScoreSmokeBackend, *localeval.ScoreProjectionStore) (*PrivacyLangfuseSurfaces, error))(nil))
	if constructor != want || constructor.IsVariadic() {
		t.Fatal("constructor accepts a forgeable or generic dependency")
	}
	evidenceType := reflect.TypeOf(PrivacyLangfuseSurfaceEvidence{})
	if evidenceType.NumField() == 0 {
		t.Fatal("evidence has no sealed proof state")
	}
	for index := 0; index < evidenceType.NumField(); index++ {
		if evidenceType.Field(index).IsExported() {
			t.Fatal("evidence exposes caller-writable proof state")
		}
	}
	wantMethods := map[string]reflect.Type{
		"Surface": reflect.TypeOf((*PrivacyLangfuseSurfaceEvidence).Surface), "EvidenceMethod": reflect.TypeOf((*PrivacyLangfuseSurfaceEvidence).EvidenceMethod),
		"ScannerPolicyVersion": reflect.TypeOf((*PrivacyLangfuseSurfaceEvidence).ScannerPolicyVersion), "Counts": reflect.TypeOf((*PrivacyLangfuseSurfaceEvidence).Counts),
		"MarshalJSON": reflect.TypeOf((*PrivacyLangfuseSurfaceEvidence).MarshalJSON),
	}
	publicType := reflect.TypeOf((*PrivacyLangfuseSurfaceEvidence)(nil))
	if publicType.NumMethod() != len(wantMethods) {
		t.Fatal("evidence exposes an unsafe accessor")
	}
	for name, signature := range wantMethods {
		method, ok := publicType.MethodByName(name)
		if !ok || method.Type != signature {
			t.Fatalf("unsafe evidence method %q", name)
		}
	}
	if _, err := json.Marshal(PrivacyLangfuseSurfaceEvidence{}); err == nil {
		t.Fatal("evidence is JSON serializable")
	}
	requestType := reflect.TypeOf(PrivacyLangfuseScanRequest{})
	for index := 0; index < requestType.NumField(); index++ {
		name := strings.ToLower(requestType.Field(index).Name)
		for _, forbidden := range []string{"attempt", "querysent", "verified", "protected", "raw", "url", "query", "count"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("request exposes forgeable proof field %q", requestType.Field(index).Name)
			}
		}
	}
}

// TestPrivacyLangfuseProductionConstructorBindsTheConcreteProjectionStore prevents a
// split-brain proof: the score client and privacy adapter must consult the same durable
// store instance, rather than two stores which merely implement the same lookup method.
func TestPrivacyLangfuseProductionConstructorBindsTheConcreteProjectionStore(t *testing.T) {
	first, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: filepath.Join(t.TempDir(), "first.json")})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: filepath.Join(t.TempDir(), "second.json")})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	trace, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: "http://127.0.0.1:1", Credential: t191Credential})
	if err != nil {
		t.Fatal(err)
	}
	score, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{
		BaseURL: "http://127.0.0.1:1", Credential: t191Credential, ProjectionStore: first,
	})
	if err != nil {
		t.Fatal(err)
	}
	if adapter, err := NewPrivacyLangfuseSurfaces(trace, score, first); err != nil || adapter == nil {
		t.Fatal("matching concrete production dependencies were rejected")
	}
	for _, dependencies := range []struct {
		trace *LangfuseChatSmokeQueryClient
		score *LangfuseScoreSmokeBackend
		store *localeval.ScoreProjectionStore
	}{
		{nil, score, first}, {trace, nil, first}, {trace, score, nil}, {trace, score, second},
	} {
		if adapter, err := NewPrivacyLangfuseSurfaces(dependencies.trace, dependencies.score, dependencies.store); err == nil || adapter != nil {
			t.Fatal("invalid or split-brain production dependencies were accepted")
		}
	}
}

func TestPrivacyLangfuseProductionConstructorScansThroughTheDurableStore(t *testing.T) {
	request := t191Request(smoke.PrivacySmokeSurfaceLangfuseScore)
	request.Deadline = time.Now().UTC().Add(5 * time.Second)
	request.StartedAt = request.Deadline.Add(-30 * time.Second)
	store, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: filepath.Join(t.TempDir(), "production.json")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projection := t191DurableProjection(t, request)
	if err := store.SaveInitial(context.Background(), request.Marker, projection, 2); err != nil {
		t.Fatal(err)
	}
	projection, err = projection.Transition(langfuse.ScoreProjectionStatusSending)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), request.Marker, projection); err != nil {
		t.Fatal(err)
	}
	projection, err = projection.Transition(langfuse.ScoreProjectionStatusSent)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), request.Marker, projection); err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		requests++
		if incoming.URL.Path == "/api/public/observations" {
			traceRequest := request
			traceRequest.Surface = smoke.PrivacySmokeSurfaceLangfuseTrace
			writeT191JSON(writer, t191TraceResponse(traceRequest, "safe", 1))
			return
		}
		if incoming.URL.Path != "/api/public/v3/scores" || incoming.URL.Query().Get("id") != projection.ProjectionID {
			http.Error(writer, "unexpected query", http.StatusBadRequest)
			return
		}
		response := t191ScoreResponse(request, "safe", 1)
		response["data"].([]any)[0].(map[string]any)["id"] = projection.ProjectionID
		writeT191JSON(writer, response)
	}))
	defer server.Close()
	trace, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: server.URL, Credential: t191Credential})
	if err != nil {
		t.Fatal(err)
	}
	score, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: t191Credential, ProjectionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewPrivacyLangfuseSurfaces(trace, score, store)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := adapter.Scan(context.Background(), request)
	if err != nil || requests != 1 {
		t.Fatalf("production Scan = (%v, requests=%d)", err, requests)
	}
	assertT191Counts(t, evidence.Counts(), "")
	request.Surface = smoke.PrivacySmokeSurfaceLangfuseTrace
	evidence, err = adapter.Scan(context.Background(), request)
	if err != nil || requests != 2 {
		t.Fatalf("production trace Scan = (%v, requests=%d)", err, requests)
	}
	assertT191Counts(t, evidence.Counts(), "")
}

func t191DurableProjection(t *testing.T, request PrivacyLangfuseScanRequest) langfuse.ScoreProjection {
	t.Helper()
	threshold := 0.8
	identity := appobs.NewCorrelationIdentity(request.RequestID,
		appobs.WithServiceSpan(request.ServiceTraceID, request.SpanID),
		appobs.WithAITraceID(request.AITraceID), appobs.WithEvalRunID(t191EvalRunID))
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: identity, Dataset: aieval.DatasetIdentity{Name: "chat-golden", Version: "v1"},
		SampleID: "sample-t191", MetricName: "answer_relevance", Score: 0.91, Threshold: &threshold,
	})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := langfuse.MapTraceToProjection(langfuse.TraceMapperInput{
		Span:        langfuse.OTLPSpanSnapshot{TraceID: request.ServiceTraceID, SpanID: request.SpanID, Name: "ai.generation", ObservationType: appobs.ObservationTypeGeneration},
		PayloadMode: appobs.PayloadModeMetadataOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := langfuse.NewScoreTarget(trace, langfuse.ScoreTargetKindObservation)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := langfuse.NewScoreProjection(langfuse.ScoreProjectionInput{Target: target, Evidence: evidence, MaxAttempts: 2, CreatedAt: request.StartedAt})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func TestPrivacyLangfuseImplementationHasNoNetworkLoggingOrFilesystemCapability(t *testing.T) {
	matches, err := filepath.Glob("privacy_langfuse*.go")
	if err != nil || len(matches) == 0 {
		t.Fatal("privacy Langfuse implementation source is missing")
	}
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, imported := range parsed.Imports {
			name := strings.Trim(imported.Path.Value, `"`)
			for _, forbidden := range []string{"net/http", "net", "net/url", "crypto/tls", "os/exec", "os", "path/filepath", "io/fs", "syscall", "golang.org/x/sys/unix"} {
				if name == forbidden {
					t.Fatalf("%s imports forbidden capability %q", path, name)
				}
			}
			if strings.Contains(strings.ToLower(name), "log") || strings.Contains(name, "zap") || strings.Contains(name, "zerolog") {
				t.Fatalf("%s imports logging capability %q", path, name)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if identifier, ok := call.Fun.(*ast.Ident); ok && (identifier.Name == "print" || identifier.Name == "println") {
					t.Fatalf("%s can print sensitive content", path)
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					for _, name := range []string{"Print", "Printf", "Info", "Infof", "Warn", "Warnf", "Error", "Errorf", "Debug", "Debugf", "Fatal", "Panic"} {
						if selector.Sel.Name == name {
							t.Fatalf("%s can log sensitive content", path)
						}
					}
				}
			}
			return true
		})
	}
}
