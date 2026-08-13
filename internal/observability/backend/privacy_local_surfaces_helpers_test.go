package backend

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	observability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

type t189LocalArtifactReader struct {
	documents   map[privacyLocalArtifactKind][]byte
	forceKind   privacyLocalArtifactKind
	omitReceipt bool
	err         error
	requests    []privacyLocalArtifactReadRequest
}

func (reader *t189LocalArtifactReader) Read(_ context.Context, request privacyLocalArtifactReadRequest) (privacyLocalArtifactDocument, error) {
	reader.requests = append(reader.requests, request)
	if reader.err != nil {
		return privacyLocalArtifactDocument{}, reader.err
	}
	kind := request.Kind
	if reader.forceKind != "" {
		kind = reader.forceKind
	}
	payload := reader.documents[request.Kind]
	if len(payload) == 0 {
		return privacyLocalArtifactDocument{}, errors.New("artifact unavailable")
	}
	receipt := "sha256:" + strings.Repeat("c", 64)
	if reader.omitReceipt {
		receipt = ""
	}
	return privacyLocalArtifactDocument{kind: kind, content: append([]byte(nil), payload...), artifactSHA256: receipt}, nil
}

func t189NewSurfaces(t *testing.T, reader privacyLocalArtifactCapability) *PrivacyLocalSurfaces {
	t.Helper()
	surfaces, err := newPrivacyLocalSurfacesForTest(t189Config(), reader)
	if err != nil {
		t.Fatalf("newPrivacyLocalSurfacesForTest() failed with class %q", t189ErrorClass(err))
	}
	return surfaces
}

func t189Config() PrivacyLocalSurfacesConfig {
	return PrivacyLocalSurfacesConfig{
		RuntimeConfigDigest:            "sha256:" + strings.Repeat("a", 64),
		ExpectedPrequeueArtifactSHA256: "sha256:" + strings.Repeat("d", 64),
		CollectorComponent:             "otlphttp/loki",
		ExportAdmissionCorrelation:     "admission-t189",
	}
}

func t189WriteProductionFixture(t *testing.T, store *smoke.PrivacyArtifactStore, request PrivacyLocalSurfaceScanRequest) smoke.PrivacyFixtureArtifactRefs {
	t.Helper()
	record := t189CanonicalOTLPRecord(t, request)
	report := t189SmokeReport(t, request, "passed")
	refs, err := store.Write(context.Background(), smoke.PrivacyFixtureArtifactInput{
		RunID: request.RunID, Marker: request.Marker, ForbiddenCanary: request.ForbiddenCanary,
		RequestID: request.RequestID, AITraceID: request.AITraceID, ServiceTraceID: request.ServiceTraceID, SpanID: request.SpanID,
		StartedAt: request.StartedAt, Deadline: request.Deadline, APIScanSummary: t189ZeroCounts(),
		ApplicationLogProjection: smoke.PrivacyApplicationLogProjection{
			Timestamp: record.Timestamp, Severity: record.Severity, Body: record.Body, Attributes: record.Attributes,
		},
		CollectorCompositeProof: smoke.PrivacyCollectorCompositeProof{
			RuntimeConfigDigest: "sha256:" + strings.Repeat("a", 64), ComponentIdentity: "otlphttp/loki",
			PrequeueArtifactSHA256:     "sha256:" + strings.Repeat("d", 64),
			ExportAdmissionCorrelation: "admission-t189",
			ComponentTelemetry: smoke.PrivacyCollectorComponentTelemetry{
				ComponentIdentity: "otlphttp/loki", ObservedAt: request.StartedAt.Add(30 * time.Second),
				WindowStartedAt: request.StartedAt, WindowDeadline: request.Deadline, Enqueued: 1, Sent: 1, QueueCapacity: 100,
			},
		},
		ChatReport: report,
	})
	if err != nil {
		t.Fatalf("write production fixture: %q", t189ErrorClass(err))
	}
	return refs
}

func t189Request(surface smoke.PrivacySmokeSurface) PrivacyLocalSurfaceScanRequest {
	startedAt := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	return PrivacyLocalSurfaceScanRequest{
		RunID: "run-t189-local", Marker: "marker-t189-local", ForbiddenCanary: t189Canary,
		RequestID: "request-t189-local", AITraceID: "ai-trace-t189-local",
		ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef",
		ManifestRef: "manifest-t189-local.json", Surface: surface,
		StartedAt: startedAt, Deadline: startedAt.Add(time.Minute),
	}
}

func t189SafeDocuments(t *testing.T, request PrivacyLocalSurfaceScanRequest) map[privacyLocalArtifactKind][]byte {
	t.Helper()
	return map[privacyLocalArtifactKind][]byte{
		privacyLocalArtifactAPISummary:         t189ArtifactDocument(t, privacyLocalArtifactAPISummary, request, "api_summary", t189AnyCounts()),
		privacyLocalArtifactApplicationLog:     t189ArtifactDocument(t, privacyLocalArtifactApplicationLog, request, "application_log_projection", t189OTLPProjection(t, request)),
		privacyLocalArtifactCollectorComposite: t189ArtifactDocument(t, privacyLocalArtifactCollectorComposite, request, "collector_composite_proof", t189CollectorPayload()),
		privacyLocalArtifactChatReport:         t189ArtifactDocument(t, privacyLocalArtifactChatReport, request, "chat_fixture_report", t189ChatReportPayload(t, request, "passed")),
	}
}

func t189Kind(surface smoke.PrivacySmokeSurface) privacyLocalArtifactKind {
	switch surface {
	case smoke.PrivacySmokeSurfaceApplicationLog:
		return privacyLocalArtifactApplicationLog
	case smoke.PrivacySmokeSurfaceCollectorQueue:
		return privacyLocalArtifactCollectorComposite
	case smoke.PrivacySmokeSurfaceReport:
		return privacyLocalArtifactChatReport
	default:
		return privacyLocalArtifactAPISummary
	}
}

func t189ArtifactDocument(t *testing.T, kind privacyLocalArtifactKind, request PrivacyLocalSurfaceScanRequest, payloadKey string, payload any) []byte {
	t.Helper()
	document := map[string]any{
		"schema_version": "1", "kind": string(kind), "run_id": request.RunID, "marker": request.Marker,
		"request_id": request.RequestID, "ai_trace_id": request.AITraceID, "service_trace_id": request.ServiceTraceID,
		"span_id": request.SpanID, "started_at": request.StartedAt.Format(time.RFC3339Nano), "deadline": request.Deadline.Format(time.RFC3339Nano),
		payloadKey: payload,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func t189DocumentWithSemanticText(t *testing.T, kind privacyLocalArtifactKind, request PrivacyLocalSurfaceScanRequest, text string) []byte {
	t.Helper()
	switch kind {
	case privacyLocalArtifactApplicationLog:
		payload := t189OTLPProjection(t, request)
		payload["body"] = text
		return t189ArtifactDocument(t, kind, request, "application_log_projection", payload)
	case privacyLocalArtifactCollectorComposite:
		payload := t189CollectorPayload()
		payload["export_admission_correlation"] = text
		return t189ArtifactDocument(t, kind, request, "collector_composite_proof", payload)
	case privacyLocalArtifactChatReport:
		payload := t189ChatReportPayload(t, request, "passed")
		payload["versions"] = map[string]any{"scanner": text}
		return t189ArtifactDocument(t, kind, request, "chat_fixture_report", payload)
	default:
		t.Fatal("unsupported semantic text kind")
		return nil
	}
}

func t189EscapedCanaryDocument(t *testing.T, kind privacyLocalArtifactKind, request PrivacyLocalSurfaceScanRequest) []byte {
	t.Helper()
	encoded := t189DocumentWithSemanticText(t, kind, request, t189Canary)
	return []byte(strings.ReplaceAll(string(encoded), t189Canary, `T189_\u0053YNTHETIC_CANARY`))
}

func t189CollectorPayload() map[string]any {
	return map[string]any{
		"runtime_config_digest":        "sha256:" + strings.Repeat("a", 64),
		"prequeue_artifact_sha256":     "sha256:" + strings.Repeat("d", 64),
		"component_identity":           "otlphttp/loki",
		"export_admission_correlation": "admission-t189",
		"component_telemetry": map[string]any{
			"component_identity": "otlphttp/loki", "observed_at": "2026-08-13T10:00:30Z",
			"window_started_at": "2026-08-13T10:00:00Z", "window_deadline": "2026-08-13T10:01:00Z",
			"enqueued": 1, "sent": 1, "failed": 0, "queue_size": 0, "queue_capacity": 100, "oldest_age_ms": 0,
		},
	}
}

func t189OTLPProjection(t *testing.T, request PrivacyLocalSurfaceScanRequest) map[string]any {
	t.Helper()
	record := t189CanonicalOTLPRecord(t, request)
	return map[string]any{
		"timestamp": record.Timestamp.Format(time.RFC3339Nano), "severity": record.Severity,
		"body": record.Body, "attributes": record.Attributes,
	}
}

func t189CanonicalOTLPRecord(t *testing.T, request PrivacyLocalSurfaceScanRequest) observability.HTTPCompletionOTLPRecord {
	t.Helper()
	entry, err := observability.BuildHTTPCompletionLog(observability.HTTPCompletionLogInput{
		Timestamp: request.StartedAt.Add(time.Second), RequestID: request.RequestID,
		TraceID: request.ServiceTraceID, SpanID: request.SpanID, RouteTemplate: "/api/v1/chat",
		Method: "POST", StatusCode: 200, Duration: 120 * time.Millisecond,
		IsAIRequest: true, IsSmokeRun: true, AITraceID: request.AITraceID, SmokeRunID: request.Marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := observability.BuildHTTPCompletionOTLPRecord(entry)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func t189DifferentKind(kind privacyLocalArtifactKind) privacyLocalArtifactKind {
	if kind == privacyLocalArtifactChatReport {
		return privacyLocalArtifactAPISummary
	}
	return privacyLocalArtifactChatReport
}

func t189RewriteDocument(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if json.Unmarshal(payload, &document) != nil {
		t.Fatal("decode artifact fixture")
	}
	mutate(document)
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func t189RewriteNestedPayload(t *testing.T, payload []byte, field string, mutate func(map[string]any)) []byte {
	t.Helper()
	return t189RewriteDocument(t, payload, func(document map[string]any) {
		nested, ok := document[field].(map[string]any)
		if !ok {
			t.Fatal("typed payload fixture is missing")
		}
		mutate(nested)
	})
}

func t189ChatReportPayload(t *testing.T, request PrivacyLocalSurfaceScanRequest, status string) map[string]any {
	t.Helper()
	report := t189SmokeReport(t, request, status)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if json.Unmarshal(encoded, &payload) != nil {
		t.Fatal("decode smoke report")
	}
	return payload
}

func t189SmokeReport(t *testing.T, request PrivacyLocalSurfaceScanRequest, status string) *smoke.SmokeReport {
	t.Helper()
	failureStage, errorClass := "none", ""
	if status != "passed" {
		failureStage, errorClass = "query", "unexpected_evidence"
	}
	report, err := smoke.BuildSmokeReport(smoke.SmokeReportInput{
		RunID: request.RunID, Marker: request.Marker, Profile: "grafana", Scenario: "chat",
		RequestID: request.RequestID, AITraceID: request.AITraceID, StartedAt: request.StartedAt,
		Deadline: request.Deadline, FinishedAt: request.StartedAt.Add(time.Second),
		Checks:  []smoke.BackendCheckInput{{Backend: "api", Status: status, Duration: time.Millisecond, FailureStage: failureStage, ErrorClass: errorClass, Evidence: map[string]any{"response_status": int64(200)}}},
		Cleanup: smoke.SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func t189AnyCounts() map[string]any {
	return map[string]any{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
}

func t189AnyCountsWith(category string, value any) map[string]any {
	counts := t189AnyCounts()
	counts[category] = value
	return counts
}

func t189ZeroCounts() map[string]int {
	return map[string]int{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
}

func assertT189Counts(t *testing.T, got, nonzero map[string]int) {
	t.Helper()
	want := t189ZeroCounts()
	for key, value := range nonzero {
		want[key] = value
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %#v, want closed policy %#v", got, want)
	}
}

func assertT189LowSensitiveError(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil || (!errors.Is(err, errPrivacyLocalSurface) && t189ErrorClass(err) != "unexpected_evidence") {
		t.Fatal("local surface failure must expose one stable low-sensitive class")
	}
	message := strings.ToLower(err.Error())
	for _, value := range forbidden {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" && strings.Contains(message, value) {
			t.Fatal("local surface failure exposed canary, identity, ref, path, or raw artifact")
		}
	}
}

func t189ContainsAny(text string, values ...string) bool {
	text = strings.ToLower(text)
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func t189ErrorClass(err error) string {
	type classified interface{ Class() string }
	var target classified
	if errors.As(err, &target) {
		return target.Class()
	}
	if err == nil {
		return ""
	}
	return "unclassified"
}
