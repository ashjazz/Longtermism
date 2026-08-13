package smoke

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	t188Canary        = "T188_SYNTHETIC_CANARY_MUST_NOT_REACH_DISK"
	t188Authorization = "t188-independent-smoke-authorization"
	t188RawResponse   = "t188-raw-provider-response"
)

var errT188InjectedPublishFailure = errors.New("injected artifact publish failure")

func t188OpenStore(t *testing.T, root string) *PrivacyArtifactStore {
	t.Helper()
	store, err := OpenPrivacyArtifactStore(root)
	if err != nil {
		t.Fatalf("OpenPrivacyArtifactStore() failed with class %q", t188ErrorClass(err))
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func t188ArtifactInput(t *testing.T) PrivacyFixtureArtifactInput {
	t.Helper()
	startedAt := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	input := PrivacyFixtureArtifactInput{
		RunID: "run-t188-artifact", Marker: "marker-t188-artifact", RequestID: "request-t188-artifact", AITraceID: "ai-trace-t188-artifact",
		ForbiddenCanary: t188Canary,
		ServiceTraceID:  "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef",
		StartedAt: startedAt, Deadline: startedAt.Add(time.Minute),
		APIScanSummary: map[string]int{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0},
		ApplicationLogProjection: PrivacyApplicationLogProjection{
			Message: "http request completed", Route: "/api/v1/chat", Method: "POST", StatusCode: 200,
		},
		CollectorCompositeProof: PrivacyCollectorCompositeProof{
			RuntimeConfigDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ComponentIdentity:   "otlphttp/loki", ExportAdmissionCorrelation: "admission-t188-artifact",
		},
	}
	input.ChatReport = t188ChatReport(t, input, "chat", nil)
	return input
}

func t188ResolveRequest(manifestRef string, input PrivacyFixtureArtifactInput) PrivacyArtifactResolveRequest {
	return PrivacyArtifactResolveRequest{
		ManifestRef: manifestRef, RunID: input.RunID, Marker: input.Marker, RequestID: input.RequestID,
		AITraceID: input.AITraceID, ServiceTraceID: input.ServiceTraceID, SpanID: input.SpanID,
		StartedAt: input.StartedAt.UTC(), Deadline: input.Deadline.UTC(),
	}
}

func t188ReadRequest(manifestRef string, kind PrivacyArtifactKind, input PrivacyFixtureArtifactInput) PrivacyArtifactReadRequest {
	return PrivacyArtifactReadRequest{Manifest: t188ResolveRequest(manifestRef, input), Kind: kind}
}

func t188SafeBasename(value string) bool {
	return value != "" && value == filepath.Base(value) && strings.HasSuffix(value, ".json") &&
		!strings.Contains(value, "/") && !strings.Contains(value, `\`) && value != "." && value != ".."
}

func t188Digest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func t188SHA256Digest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func t188AppendUnknownField(payload []byte) []byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[len(trimmed)-1] != '}' {
		return append(payload, []byte(`{"unknown":true}`)...)
	}
	return append(append([]byte(nil), trimmed[:len(trimmed)-1]...), []byte(`,"unknown":true}`)...)
}

func t188DuplicateFirstKey(payload []byte) []byte {
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) != nil {
		return append(payload, payload...)
	}
	for key, value := range object {
		prefix := []byte(`{"` + key + `":`)
		return append(append(append(prefix, value...), ','), bytes.TrimSpace(payload)[1:]...)
	}
	return append(payload, []byte(` {}`)...)
}

func t188DuplicateNamedKey(payload []byte, key string) []byte {
	needle := []byte(`"` + key + `":`)
	index := bytes.Index(payload, needle)
	if index < 0 {
		return append(payload, []byte(` {}`)...)
	}
	valueStart := index + len(needle)
	valueEnd := valueStart
	if valueStart < len(payload) && payload[valueStart] == '"' {
		valueEnd++
		for valueEnd < len(payload) {
			if payload[valueEnd] == '"' && payload[valueEnd-1] != '\\' {
				valueEnd++
				break
			}
			valueEnd++
		}
	} else {
		for valueEnd < len(payload) && payload[valueEnd] != ',' && payload[valueEnd] != '}' {
			valueEnd++
		}
	}
	duplicate := append(append([]byte(nil), needle...), payload[valueStart:valueEnd]...)
	duplicate = append(duplicate, ',')
	return append(append(append([]byte(nil), payload[:index]...), duplicate...), payload[index:]...)
}

func assertT188TypedArtifactDocument(t *testing.T, payload []byte, kind PrivacyArtifactKind, input PrivacyFixtureArtifactInput) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal("typed artifact is not JSON")
	}
	wantIdentity := map[string]string{
		"schema_version": "1", "kind": string(kind), "run_id": input.RunID, "marker": input.Marker,
		"request_id": input.RequestID, "ai_trace_id": input.AITraceID, "service_trace_id": input.ServiceTraceID,
		"span_id": input.SpanID, "started_at": input.StartedAt.UTC().Format(time.RFC3339Nano),
		"deadline": input.Deadline.UTC().Format(time.RFC3339Nano),
	}
	for field, want := range wantIdentity {
		if document[field] != want {
			t.Fatalf("typed %q artifact did not bind %s", kind, field)
		}
	}
	var payloadField string
	var wantPayload any
	switch kind {
	case PrivacyArtifactKindAPISummary:
		payloadField, wantPayload = "api_summary", input.APIScanSummary
	case PrivacyArtifactKindApplicationLogProjection:
		payloadField, wantPayload = "application_log_projection", input.ApplicationLogProjection
	case PrivacyArtifactKindCollectorCompositeProof:
		payloadField, wantPayload = "collector_composite_proof", input.CollectorCompositeProof
	case PrivacyArtifactKindChatFixtureReport:
		payloadField, wantPayload = "chat_fixture_report", input.ChatReport
	default:
		t.Fatal("unknown typed artifact kind")
	}
	wantBytes, err := json.Marshal(wantPayload)
	if err != nil {
		t.Fatal(err)
	}
	var normalizedWant any
	if json.Unmarshal(wantBytes, &normalizedWant) != nil || !reflect.DeepEqual(document[payloadField], normalizedWant) {
		t.Fatalf("typed %q artifact did not preserve its allowlisted payload", kind)
	}
}

func t188InspectPublishedTree(root string) (bool, int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, 0, err
	}
	hasManifest := false
	artifactCount := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return false, 0, err
		}
		var document map[string]json.RawMessage
		if json.Unmarshal(payload, &document) != nil {
			continue
		}
		if document["artifacts"] != nil {
			hasManifest = true
		}
		if document["kind"] != nil {
			artifactCount++
		}
	}
	return hasManifest, artifactCount, nil
}

func t188ReadTarget(store *PrivacyArtifactStore, manifestRef string, kind PrivacyArtifactKind, input PrivacyFixtureArtifactInput) error {
	if kind == "" {
		_, err := store.Resolve(context.Background(), t188ResolveRequest(manifestRef, input))
		return err
	}
	_, err := store.Read(context.Background(), t188ReadRequest(manifestRef, kind, input))
	return err
}

func t188RunSpecialFileSubprocess(t *testing.T, target, mutation string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrivacyArtifactStoreSpecialFileChild$")
	command.Env = append(os.Environ(), "T188_SPECIAL_TARGET="+target, "T188_SPECIAL_MUTATION="+mutation)
	if output, err := command.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			t.Fatal("special-file reader blocked instead of failing closed")
		}
		t.Fatalf("special-file subprocess failed: %s", bytes.TrimSpace(output))
	}
}

func t188TargetByName(t *testing.T, target string, refs PrivacyFixtureArtifactRefs) (PrivacyArtifactKind, string) {
	t.Helper()
	switch target {
	case "manifest":
		return "", refs.ManifestRef
	case "api":
		return PrivacyArtifactKindAPISummary, refs.APISummaryRef
	case "application-log":
		return PrivacyArtifactKindApplicationLogProjection, refs.ApplicationLogRef
	case "collector":
		return PrivacyArtifactKindCollectorCompositeProof, refs.CollectorArtifactRef
	case "chat-report":
		return PrivacyArtifactKindChatFixtureReport, refs.ChatReportRef
	default:
		t.Fatal("unknown special-file target")
		return "", ""
	}
}

func t188ReadFile(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func t188RewriteJSONObject(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func t188ManifestBinding(t *testing.T, manifest map[string]any, index int) map[string]any {
	t.Helper()
	bindings, ok := manifest["artifacts"].([]any)
	if !ok || index < 0 || index >= len(bindings) {
		t.Fatal("manifest fixture does not contain the requested binding")
	}
	binding, ok := bindings[index].(map[string]any)
	if !ok {
		t.Fatal("manifest binding fixture is not an object")
	}
	return binding
}

func t188RebindArtifactBytes(t *testing.T, root, manifestRef, artifactRef string, payload []byte) {
	t.Helper()
	manifestPath := filepath.Join(root, manifestRef)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(manifestBytes, &document) != nil {
		t.Fatal("decode manifest fixture")
	}
	bindings, ok := document["artifacts"].([]any)
	if !ok {
		t.Fatal("manifest artifacts fixture is not an array")
	}
	for _, raw := range bindings {
		binding, ok := raw.(map[string]any)
		if ok && binding["ref"] == artifactRef {
			binding["sha256"] = t188Digest(payload)
			binding["size_bytes"] = len(payload)
		}
	}
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal("encode manifest fixture")
	}
	if err := os.WriteFile(manifestPath, updated, 0o600); err != nil {
		t.Fatal("rewrite manifest fixture")
	}
}

func t188DirectorySnapshot(t *testing.T, root string) []byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			payload, err := os.ReadFile(filepath.Join(root, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			snapshot = append(snapshot, entry.Name()...)
			snapshot = append(snapshot, payload...)
		}
	}
	return snapshot
}

func t188ChatReport(t *testing.T, input PrivacyFixtureArtifactInput, scenario string, evidence map[string]any) *SmokeReport {
	t.Helper()
	if evidence == nil {
		evidence = map[string]any{"response_status": int64(200)}
	}
	report, err := BuildSmokeReport(SmokeReportInput{
		RunID: input.RunID, Marker: input.Marker, Profile: "grafana", Scenario: scenario,
		RequestID: input.RequestID, AITraceID: input.AITraceID,
		StartedAt: input.StartedAt, Deadline: input.Deadline, FinishedAt: input.StartedAt.Add(time.Second),
		Checks:  []BackendCheckInput{{Backend: "api", Status: "passed", FailureStage: "none", Evidence: evidence}},
		Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func assertT188TreeContainsNoForbiddenBytes(t *testing.T, root string) {
	t.Helper()
	forbidden := []string{t188Canary, t188Authorization, t188RawResponse, "sk-proj-t188", "privacy-t188@example.com", "t188-forbidden-token"}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		if info.Size() > maximumPrivacyArtifactBytes {
			return errors.New("artifact exceeds bounded leak scan")
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range forbidden {
			if bytes.Contains(bytes.ToLower(payload), bytes.ToLower([]byte(secret))) {
				return errors.New("forbidden bytes reached the artifact tree")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("privacy artifact tree contains forbidden or unbounded data")
	}
}

func assertT188LowSensitiveError(t *testing.T, err error, dynamicForbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected low-sensitive artifact error")
	}
	type classified interface{ Class() string }
	var target classified
	if !errors.Is(err, errPrivacyArtifactStore) && (!errors.As(err, &target) || target.Class() != "artifact_unavailable") {
		t.Fatal("artifact failure must expose one stable class")
	}
	text := strings.ToLower(err.Error())
	forbidden := append([]string{t188Canary, t188Authorization, t188RawResponse, "authorization:", "bearer ", "../", "/private/", "/tmp/"}, dynamicForbidden...)
	for _, value := range forbidden {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && strings.Contains(text, value) {
			t.Fatal("artifact error exposed a path, identity, or sensitive value")
		}
	}
}

func t188ErrorClass(err error) string {
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
