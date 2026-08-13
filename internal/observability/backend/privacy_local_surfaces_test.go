package backend

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const (
	t189Canary      = "T189_SYNTHETIC_CANARY"
	t189RawArtifact = "t189-raw-artifact-must-not-escape"
)

// TestPrivacyLocalSurfacesReadFourDistinctRegisteredArtifacts 固定四个本地 surface 的
// typed routing。每次证据都必须来自一次独立的 contained read，不能由 generic query
// 自报 attempted=true，也不能复用一份全零工件冒充四个事实面。
func TestPrivacyLocalSurfacesReadFourDistinctRegisteredArtifacts(t *testing.T) {
	request := t189Request(smoke.PrivacySmokeSurfaceAPI)
	reader := &t189LocalArtifactReader{documents: t189SafeDocuments(t, request)}
	surfaces := t189NewSurfaces(t, reader)
	tests := []struct {
		surface smoke.PrivacySmokeSurface
		kind    privacyLocalArtifactKind
		method  string
	}{
		{smoke.PrivacySmokeSurfaceAPI, privacyLocalArtifactAPISummary, "bounded_memory_scan"},
		{smoke.PrivacySmokeSurfaceApplicationLog, privacyLocalArtifactApplicationLog, "pre_export_projection"},
		{smoke.PrivacySmokeSurfaceCollectorQueue, privacyLocalArtifactCollectorComposite, "prequeue_configuration_telemetry"},
		{smoke.PrivacySmokeSurfaceReport, privacyLocalArtifactChatReport, "contained_artifact_scan"},
	}
	results := make([]PrivacyLocalSurfaceEvidence, 0, len(tests))
	for _, tt := range tests {
		request.Surface = tt.surface
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatalf("Scan(%q) failed with class %q", tt.surface, t189ErrorClass(err))
		}
		if evidence.Surface() != tt.surface || evidence.LocalProofKind() != tt.method || evidence.ScannerPolicyVersion() != "1" {
			t.Fatalf("Scan(%q) returned the wrong closed evidence identity", tt.surface)
		}
		assertT189Counts(t, evidence.Counts(), map[string]int{})
		results = append(results, evidence)
	}
	if len(reader.requests) != 4 {
		t.Fatalf("contained reads = %d, want one per local surface", len(reader.requests))
	}
	for index, read := range reader.requests {
		if read.Kind != tests[index].kind || read.ManifestRef != request.ManifestRef || read.RunID != request.RunID ||
			read.Marker != request.Marker || read.RequestID != request.RequestID || read.AITraceID != request.AITraceID ||
			read.ServiceTraceID != request.ServiceTraceID || read.SpanID != request.SpanID ||
			!read.StartedAt.Equal(request.StartedAt) || !read.Deadline.Equal(request.Deadline) {
			t.Fatalf("read %d did not preserve kind plus complete manifest identity/window", index)
		}
	}
	encoded, err := json.Marshal(results)
	forbidden := []string{t189Canary, t189RawArtifact, request.ManifestRef, request.RunID, request.RequestID, request.ServiceTraceID, "otlphttp/loki", "sha256:"}
	if err != nil || t189ContainsAny(string(encoded), forbidden...) {
		t.Fatal("evidence result exposed scanner policy or raw artifact content")
	}
}

// TestPrivacyLocalProductionPathUsesTheContainedStore 防止 production constructor 仅在
// 类型上接收 T194 store、运行时却忽略它并返回静态全零。四类正向读取和逐类破坏均从
// 真实 contained writer/resolver/reader 经过，fake capability 只保留给错误注入。
func TestPrivacyLocalProductionPathUsesTheContainedStore(t *testing.T) {
	for _, corruptedSurface := range []smoke.PrivacySmokeSurface{"", smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceReport} {
		name := "happy"
		if corruptedSurface != "" {
			name = "corrupt-" + string(corruptedSurface)
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "privacy-artifacts")
			store, err := smoke.OpenPrivacyArtifactStore(root)
			if err != nil {
				t.Fatalf("open contained store: %q", t189ErrorClass(err))
			}
			defer store.Close()
			request := t189Request(smoke.PrivacySmokeSurfaceAPI)
			refs := t189WriteProductionFixture(t, store, request)
			request.ManifestRef = refs.ManifestRef
			if corruptedSurface != "" {
				ref := map[smoke.PrivacySmokeSurface]string{
					smoke.PrivacySmokeSurfaceAPI: refs.APISummaryRef, smoke.PrivacySmokeSurfaceApplicationLog: refs.ApplicationLogRef,
					smoke.PrivacySmokeSurfaceCollectorQueue: refs.CollectorArtifactRef, smoke.PrivacySmokeSurfaceReport: refs.ChatReportRef,
				}[corruptedSurface]
				if err := os.Remove(filepath.Join(root, ref)); err != nil {
					t.Fatal(err)
				}
			}
			surfaces, err := NewPrivacyLocalSurfaces(t189Config(), store)
			if err != nil {
				t.Fatalf("production constructor failed: %q", t189ErrorClass(err))
			}
			for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceReport} {
				request.Surface = surface
				evidence, scanErr := surfaces.Scan(context.Background(), request)
				if surface == corruptedSurface {
					if scanErr == nil || !reflect.ValueOf(evidence).IsZero() {
						t.Fatal("production path ignored missing contained artifact and returned zero")
					}
					continue
				}
				if scanErr != nil {
					t.Fatalf("production Scan(%q) failed with class %q", surface, t189ErrorClass(scanErr))
				}
			}
		})
	}
}

func TestPrivacyLocalSurfacesRejectSemanticallyInvalidTypedPayloads(t *testing.T) {
	tests := []struct {
		name    string
		surface smoke.PrivacySmokeSurface
		mutate  func(map[string]any)
	}{
		{name: "application body", surface: smoke.PrivacySmokeSurfaceApplicationLog, mutate: func(payload map[string]any) { payload["body"] = "arbitrary text" }},
		{name: "application severity", surface: smoke.PrivacySmokeSurfaceApplicationLog, mutate: func(payload map[string]any) { payload["severity"] = "DEBUG" }},
		{name: "application route", surface: smoke.PrivacySmokeSurfaceApplicationLog, mutate: func(payload map[string]any) { payload["attributes"].(map[string]any)["route"] = "/dynamic/private" }},
		{name: "application method", surface: smoke.PrivacySmokeSurfaceApplicationLog, mutate: func(payload map[string]any) { payload["attributes"].(map[string]any)["method"] = "GET" }},
		{name: "application status", surface: smoke.PrivacySmokeSurfaceApplicationLog, mutate: func(payload map[string]any) { payload["attributes"].(map[string]any)["status"] = 500 }},
		{name: "application missing identity", surface: smoke.PrivacySmokeSurfaceApplicationLog, mutate: func(payload map[string]any) { delete(payload["attributes"].(map[string]any), "request_id") }},
		{name: "application unknown attribute", surface: smoke.PrivacySmokeSurfaceApplicationLog, mutate: func(payload map[string]any) { payload["attributes"].(map[string]any)["prompt"] = "invented" }},
		{name: "privacy report", surface: smoke.PrivacySmokeSurfaceReport, mutate: func(payload map[string]any) { payload["scenario"] = "privacy" }},
		{name: "failed chat report", surface: smoke.PrivacySmokeSurfaceReport, mutate: func(payload map[string]any) { payload["status"] = "failed" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := t189Request(tt.surface)
			kind := t189Kind(tt.surface)
			payloadKey := "application_log_projection"
			payload := t189OTLPProjection(t, request)
			if tt.surface == smoke.PrivacySmokeSurfaceReport {
				payloadKey, payload = "chat_fixture_report", t189ChatReportPayload(t, request, "passed")
			}
			tt.mutate(payload)
			reader := &t189LocalArtifactReader{documents: map[privacyLocalArtifactKind][]byte{kind: t189ArtifactDocument(t, kind, request, payloadKey, payload)}}
			if _, err := t189NewSurfaces(t, reader).Scan(context.Background(), request); err == nil {
				t.Fatal("semantically invalid typed local artifact became evidence")
			} else {
				encoded, _ := json.Marshal(payload)
				assertT189LowSensitiveError(t, err, string(encoded), request.RunID, request.ManifestRef)
			}
		})
	}
}

func TestPrivacyLocalAPISummaryRequiresClosedExplicitCounts(t *testing.T) {
	request := t189Request(smoke.PrivacySmokeSurfaceAPI)
	tests := []struct {
		name    string
		counts  map[string]any
		want    map[string]int
		wantErr bool
	}{
		{name: "explicit zero", counts: t189AnyCounts()},
		{name: "canary hit", counts: t189AnyCountsWith("synthetic_canary", 1), want: map[string]int{"synthetic_canary": 1}},
		{name: "credential hit", counts: t189AnyCountsWith("credential", 1), want: map[string]int{"credential": 1}},
		{name: "authorization hit", counts: t189AnyCountsWith("authorization", 1), want: map[string]int{"authorization": 1}},
		{name: "token hit", counts: t189AnyCountsWith("token", 1), want: map[string]int{"token": 1}},
		{name: "PII hit", counts: t189AnyCountsWith("recognized_pii", 1), want: map[string]int{"recognized_pii": 1}},
		{name: "missing category", counts: map[string]any{"synthetic_canary": 0}, wantErr: true},
		{name: "unknown category", counts: t189AnyCountsWith("unknown", 0), wantErr: true},
		{name: "negative", counts: t189AnyCountsWith("token", -1), wantErr: true},
		{name: "fractional", counts: t189AnyCountsWith("token", 0.5), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := t189ArtifactDocument(t, privacyLocalArtifactAPISummary, request, "api_summary", tt.counts)
			reader := &t189LocalArtifactReader{documents: map[privacyLocalArtifactKind][]byte{privacyLocalArtifactAPISummary: document}}
			evidence, err := t189NewSurfaces(t, reader).Scan(context.Background(), request)
			if tt.wantErr {
				if err == nil {
					t.Fatal("malformed API scan summary became default-zero evidence")
				}
				return
			}
			if err != nil {
				t.Fatal("valid API summary was rejected")
			}
			assertT189Counts(t, evidence.Counts(), tt.want)
		})
	}
}

func TestPrivacyLocalSurfacesDetectEveryClosedScannerCategory(t *testing.T) {
	tests := []struct {
		name, text, category string
	}{
		{name: "synthetic canary", text: t189Canary, category: "synthetic_canary"},
		{name: "credential", text: "api_key=sk-t189credential", category: "credential"},
		{name: "authorization", text: "Authorization: Bearer t189-secret", category: "authorization"},
		{name: "token", text: "token=t189-secret", category: "token"},
		{name: "recognized PII", text: "privacy-t189@example.com", category: "recognized_pii"},
	}
	for _, surface := range []smoke.PrivacySmokeSurface{
		smoke.PrivacySmokeSurfaceApplicationLog,
		smoke.PrivacySmokeSurfaceCollectorQueue,
		smoke.PrivacySmokeSurfaceReport,
	} {
		for _, tt := range tests {
			t.Run(string(surface)+"/"+tt.name, func(t *testing.T) {
				request := t189Request(surface)
				documents := t189SafeDocuments(t, request)
				kind := t189Kind(surface)
				documents[kind] = t189DocumentWithSemanticText(t, kind, request, tt.text)
				evidence, err := t189NewSurfaces(t, &t189LocalArtifactReader{documents: documents}).Scan(context.Background(), request)
				if err != nil {
					t.Fatalf("scanner rejected confirmed category instead of returning low-sensitive counts: %q", t189ErrorClass(err))
				}
				assertT189Counts(t, evidence.Counts(), map[string]int{tt.category: 1})
			})
		}
	}
}

// JSON unicode escapes are decoded before the scanner runs. Scanning raw JSON bytes alone would
// miss the exact semantic canary and create a false zero.
func TestPrivacyLocalSurfacesScanDecodedSemanticsNotOnlyRawJSON(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceReport} {
		t.Run(string(surface), func(t *testing.T) {
			request := t189Request(surface)
			kind := t189Kind(surface)
			documents := t189SafeDocuments(t, request)
			documents[kind] = t189EscapedCanaryDocument(t, kind, request)
			evidence, err := t189NewSurfaces(t, &t189LocalArtifactReader{documents: documents}).Scan(context.Background(), request)
			if err != nil {
				t.Fatal("escaped semantic content could not be scanned")
			}
			assertT189Counts(t, evidence.Counts(), map[string]int{"synthetic_canary": 1})
		})
	}
}

func TestPrivacyCollectorCompositeRequiresTrustedConfigurationBindings(t *testing.T) {
	request := t189Request(smoke.PrivacySmokeSurfaceCollectorQueue)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing digest", mutate: func(payload map[string]any) { delete(payload, "runtime_config_digest") }},
		{name: "foreign digest", mutate: func(payload map[string]any) { payload["runtime_config_digest"] = "sha256:" + strings.Repeat("b", 64) }},
		{name: "foreign prequeue hash", mutate: func(payload map[string]any) {
			payload["prequeue_artifact_sha256"] = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "missing prequeue hash", mutate: func(payload map[string]any) { delete(payload, "prequeue_artifact_sha256") }},
		{name: "foreign component", mutate: func(payload map[string]any) { payload["component_identity"] = "file/storage" }},
		{name: "foreign admission", mutate: func(payload map[string]any) { payload["export_admission_correlation"] = "admission-foreign" }},
		{name: "missing telemetry", mutate: func(payload map[string]any) { delete(payload, "component_telemetry") }},
		{name: "foreign telemetry component", mutate: func(payload map[string]any) {
			payload["component_telemetry"].(map[string]any)["component_identity"] = "file/storage"
		}},
		{name: "stale telemetry", mutate: func(payload map[string]any) {
			payload["component_telemetry"].(map[string]any)["observed_at"] = "2020-01-01T00:00:00Z"
		}},
		{name: "negative telemetry", mutate: func(payload map[string]any) { payload["component_telemetry"].(map[string]any)["failed"] = -1 }},
		{name: "queue over capacity", mutate: func(payload map[string]any) { payload["component_telemetry"].(map[string]any)["queue_size"] = 101 }},
		{name: "self-reported verification", mutate: func(payload map[string]any) { payload["queue_contents_scanned"] = true }},
		{name: "fake zero queue", mutate: func(payload map[string]any) { payload["queue_depth"] = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := t189CollectorPayload()
			tt.mutate(payload)
			document := t189ArtifactDocument(t, privacyLocalArtifactCollectorComposite, request, "collector_composite_proof", payload)
			reader := &t189LocalArtifactReader{documents: map[privacyLocalArtifactKind][]byte{privacyLocalArtifactCollectorComposite: document}}
			if _, err := t189NewSurfaces(t, reader).Scan(context.Background(), request); err == nil {
				t.Fatal("untrusted or invented Collector binding became local proof")
			} else {
				encoded, _ := json.Marshal(payload)
				assertT189LowSensitiveError(t, err, string(encoded), request.RunID, request.ManifestRef)
			}
		})
	}
}

func TestPrivacyLocalSurfacesFailClosedBeforeZeroEvidence(t *testing.T) {
	tests := []struct {
		name   string
		reader *t189LocalArtifactReader
		mutate func(*PrivacyLocalSurfaceScanRequest)
	}{
		{name: "read failure", reader: &t189LocalArtifactReader{err: errors.New(t189RawArtifact)}},
		{name: "missing document", reader: &t189LocalArtifactReader{documents: map[privacyLocalArtifactKind][]byte{}}},
		{name: "wrong kind", reader: &t189LocalArtifactReader{forceKind: privacyLocalArtifactChatReport}},
		{name: "unknown surface", reader: &t189LocalArtifactReader{}, mutate: func(request *PrivacyLocalSurfaceScanRequest) { request.Surface = smoke.PrivacySmokeSurface("unknown") }},
		{name: "remote surface", reader: &t189LocalArtifactReader{}, mutate: func(request *PrivacyLocalSurfaceScanRequest) { request.Surface = smoke.PrivacySmokeSurfaceLoki }},
		{name: "missing canary", reader: &t189LocalArtifactReader{}, mutate: func(request *PrivacyLocalSurfaceScanRequest) { request.ForbiddenCanary = "" }},
		{name: "foreign window", reader: &t189LocalArtifactReader{}, mutate: func(request *PrivacyLocalSurfaceScanRequest) { request.Deadline = request.Deadline.Add(time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := t189Request(smoke.PrivacySmokeSurfaceAPI)
			if tt.reader.documents == nil && tt.reader.err == nil && tt.reader.forceKind == "" {
				tt.reader.documents = t189SafeDocuments(t, request)
			}
			if tt.mutate != nil {
				tt.mutate(&request)
			}
			evidence, err := t189NewSurfaces(t, tt.reader).Scan(context.Background(), request)
			if err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("failed/missing/foreign read became default-zero evidence")
			}
			assertT189LowSensitiveError(t, err, request.ForbiddenCanary, request.ManifestRef, request.RunID, t189RawArtifact)
			if (request.Surface == smoke.PrivacySmokeSurface("unknown") || request.Surface == smoke.PrivacySmokeSurfaceLoki) && len(tt.reader.requests) != 0 {
				t.Fatal("unsupported surface reached the contained artifact reader")
			}
		})
	}

	request := t189Request(smoke.PrivacySmokeSurfaceAPI)
	reader := &t189LocalArtifactReader{documents: t189SafeDocuments(t, request)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if evidence, err := t189NewSurfaces(t, reader).Scan(ctx, request); err == nil || !reflect.ValueOf(evidence).IsZero() || len(reader.requests) != 0 {
		t.Fatal("canceled context produced local evidence or reached storage")
	}
}

func TestPrivacyLocalSurfacesRejectArtifactConfusionForEveryKind(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceReport} {
		t.Run(string(surface)+"/missing expected", func(t *testing.T) {
			request := t189Request(surface)
			documents := t189SafeDocuments(t, request)
			delete(documents, t189Kind(surface))
			reader := &t189LocalArtifactReader{documents: documents}
			if evidence, err := t189NewSurfaces(t, reader).Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() || len(reader.requests) != 1 {
				t.Fatal("missing expected artifact fell back to another registered kind or zero")
			}
		})
		t.Run(string(surface)+"/wrong receipt kind", func(t *testing.T) {
			request := t189Request(surface)
			reader := &t189LocalArtifactReader{documents: t189SafeDocuments(t, request), forceKind: t189DifferentKind(t189Kind(surface))}
			if evidence, err := t189NewSurfaces(t, reader).Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("foreign kind receipt became local evidence")
			}
		})
		t.Run(string(surface)+"/missing concrete read receipt", func(t *testing.T) {
			request := t189Request(surface)
			reader := &t189LocalArtifactReader{documents: t189SafeDocuments(t, request), omitReceipt: true}
			if evidence, err := t189NewSurfaces(t, reader).Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("content without a sealed read receipt became local evidence")
			}
		})
		t.Run(string(surface)+"/foreign envelope", func(t *testing.T) {
			request := t189Request(surface)
			documents := t189SafeDocuments(t, request)
			kind := t189Kind(surface)
			documents[kind] = t189RewriteDocument(t, documents[kind], func(document map[string]any) { document["run_id"] = "run-t189-foreign" })
			if evidence, err := t189NewSurfaces(t, &t189LocalArtifactReader{documents: documents}).Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("foreign typed envelope became local evidence")
			}
		})
	}
}

func TestPrivacyLocalReportRequiresNestedSameFixtureIdentityAndWindow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "run", mutate: func(report map[string]any) { report["run_id"] = "run-t189-foreign" }},
		{name: "marker", mutate: func(report map[string]any) { report["marker"] = "marker-t189-foreign" }},
		{name: "request", mutate: func(report map[string]any) { report["request_id"] = "request-t189-foreign" }},
		{name: "AI trace", mutate: func(report map[string]any) { report["ai_trace_id"] = "ai-trace-t189-foreign" }},
		{name: "started at", mutate: func(report map[string]any) { report["started_at"] = "2020-01-01T00:00:00Z" }},
		{name: "finished outside window", mutate: func(report map[string]any) { report["finished_at"] = "2030-01-01T00:00:00Z" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := t189Request(smoke.PrivacySmokeSurfaceReport)
			documents := t189SafeDocuments(t, request)
			documents[privacyLocalArtifactChatReport] = t189RewriteNestedPayload(t, documents[privacyLocalArtifactChatReport], "chat_fixture_report", tt.mutate)
			if _, err := t189NewSurfaces(t, &t189LocalArtifactReader{documents: documents}).Scan(context.Background(), request); err == nil {
				t.Fatal("foreign nested chat report became contained scan proof")
			}
		})
	}
}

func TestPrivacyLocalSurfaceProofCannotBeCallerReported(t *testing.T) {
	constructor := reflect.TypeOf(NewPrivacyLocalSurfaces)
	if constructor.Kind() != reflect.Func || constructor.NumIn() != 2 || constructor.In(1).String() != "*smoke.PrivacyArtifactStore" {
		t.Fatal("production constructor must accept only the concrete T194 contained store")
	}
	if surfaces, err := NewPrivacyLocalSurfaces(PrivacyLocalSurfacesConfig{
		RuntimeConfigDigest: "sha256:" + strings.Repeat("a", 64), CollectorComponent: "otlphttp/loki",
		ExportAdmissionCorrelation: "admission-t189",
	}, nil); err == nil || surfaces != nil {
		t.Fatal("production constructor accepted a missing concrete artifact store")
	}
	for _, value := range []any{PrivacyLocalSurfacesConfig{}, PrivacyLocalSurfaceScanRequest{}} {
		typeOf := reflect.TypeOf(value)
		for _, name := range []string{"Attempted", "QuerySent", "Read", "Verified", "HashVerified", "ArtifactRef", "Path", "RawBody", "Counts"} {
			if _, exists := typeOf.FieldByName(name); exists {
				t.Fatalf("%s exposes forgeable proof/raw field %s", typeOf.Name(), name)
			}
		}
	}
	resultType := reflect.TypeOf(PrivacyLocalSurfaceEvidence{})
	for index := 0; index < resultType.NumField(); index++ {
		field := resultType.Field(index)
		if field.PkgPath == "" || strings.Contains(strings.ToLower(field.Name), "raw") || strings.Contains(strings.ToLower(field.Name), "path") {
			t.Fatalf("local evidence exposes caller-writable or raw field %s", field.Name)
		}
	}
	wantMethods := map[string]string{
		"Surface": "smoke.PrivacySmokeSurface", "LocalProofKind": "string",
		"ScannerPolicyVersion": "string", "Counts": "map[string]int",
	}
	methodSet := reflect.PointerTo(resultType)
	if methodSet.NumMethod() != len(wantMethods) {
		t.Fatal("local evidence exposes methods outside the exact low-sensitive allowlist")
	}
	for methodIndex := 0; methodIndex < methodSet.NumMethod(); methodIndex++ {
		method := methodSet.Method(methodIndex)
		wantReturn, allowed := wantMethods[method.Name]
		if !allowed || method.Type.NumOut() != 1 || method.Type.Out(0).String() != wantReturn {
			t.Fatalf("local evidence exposes unapproved accessor %s", method.Name)
		}
	}
	counts := t189ZeroCounts()
	request := t189Request(smoke.PrivacySmokeSurfaceAPI)
	evidence, err := t189NewSurfaces(t, &t189LocalArtifactReader{documents: t189SafeDocuments(t, request)}).Scan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	returned := evidence.Counts()
	returned["token"] = 99
	if reflect.DeepEqual(returned, evidence.Counts()) || !reflect.DeepEqual(evidence.Counts(), counts) {
		t.Fatal("Counts() exposed mutable internal proof state")
	}
}

func TestPrivacyLocalSurfaceImplementationHasNoLoggingOrQueueFilesystemCapability(t *testing.T) {
	paths, err := filepath.Glob("privacy_local_surfaces*.go")
	if err != nil || len(paths) == 0 {
		t.Fatal("T195 implementation is missing")
	}
	implementationFiles := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		implementationFiles++
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal("T195 implementation cannot be inspected")
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, source, parser.AllErrors)
		if parseErr != nil {
			t.Fatal("T195 implementation must be valid Go")
		}
		for _, imported := range parsed.Imports {
			name := strings.Trim(imported.Path.Value, `"`)
			forbidden := name == "fmt" || name == "log" || name == "log/slog" || name == "os" || name == "path/filepath" ||
				name == "io/fs" || name == "syscall" || name == "golang.org/x/sys/unix" || strings.Contains(name, "glog") ||
				strings.Contains(name, "zap") || strings.Contains(name, "zerolog") || strings.Contains(name, "logger")
			if forbidden {
				t.Fatalf("T195 implementation has forbidden logging/filesystem capability %q", name)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if identifier, ok := call.Fun.(*ast.Ident); ok && (identifier.Name == "print" || identifier.Name == "println") {
					t.Fatal("T195 implementation can print sensitive artifact data")
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(selector.Sel.Name, "Print") {
					t.Fatal("T195 implementation can print sensitive artifact data")
				}
			}
			return true
		})
	}
	if implementationFiles == 0 {
		t.Fatal("T195 production implementation is missing")
	}
}

func TestPrivacyLocalSurfaceFailuresAreLowSensitiveAcrossAllKinds(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceReport} {
		t.Run(string(surface), func(t *testing.T) {
			request := t189Request(surface)
			rawFailure := errors.New(t189RawArtifact + " /private/t189 " + request.ManifestRef + " " + request.RunID + " " + request.ServiceTraceID + " " + t189Canary)
			evidence, err := t189NewSurfaces(t, &t189LocalArtifactReader{err: rawFailure}).Scan(context.Background(), request)
			if err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("raw reader failure became evidence")
			}
			assertT189LowSensitiveError(t, err, t189RawArtifact, "/private/t189", request.ManifestRef, request.RunID, request.ServiceTraceID, t189Canary)
		})
	}
}
