package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPrivacyArtifactStorePublishesTypedManifestLast(t *testing.T) {
	root := filepath.Join(t.TempDir(), "privacy-artifacts")
	store := t188OpenStore(t, root)
	input := t188ArtifactInput(t)

	refs, err := store.Write(context.Background(), input)
	if err != nil {
		t.Fatalf("Write() failed with class %q", t188ErrorClass(err))
	}
	manifest, err := store.Resolve(context.Background(), t188ResolveRequest(refs.ManifestRef, input))
	if err != nil {
		t.Fatalf("Resolve() failed with class %q", t188ErrorClass(err))
	}
	if manifest.SchemaVersion != "1" || manifest.RunID != input.RunID || manifest.Marker != input.Marker ||
		manifest.RequestID != input.RequestID || manifest.AITraceID != input.AITraceID ||
		manifest.ServiceTraceID != input.ServiceTraceID || manifest.SpanID != input.SpanID ||
		!manifest.StartedAt.Equal(input.StartedAt.UTC()) || !manifest.Deadline.Equal(input.Deadline.UTC()) {
		t.Fatal("typed manifest did not preserve the exact fixture identity and window")
	}

	wantKinds := []PrivacyArtifactKind{
		PrivacyArtifactKindAPISummary,
		PrivacyArtifactKindApplicationLogProjection,
		PrivacyArtifactKindCollectorCompositeProof,
		PrivacyArtifactKindChatFixtureReport,
	}
	if len(manifest.Artifacts) != len(wantKinds) {
		t.Fatalf("manifest artifacts = %d, want exactly four", len(manifest.Artifacts))
	}
	seenRefs := map[string]bool{refs.ManifestRef: true}
	seenKinds := make(map[PrivacyArtifactKind]bool, len(wantKinds))
	wantRefs := map[PrivacyArtifactKind]string{
		PrivacyArtifactKindAPISummary:               refs.APISummaryRef,
		PrivacyArtifactKindApplicationLogProjection: refs.ApplicationLogRef,
		PrivacyArtifactKindCollectorCompositeProof:  refs.CollectorArtifactRef,
		PrivacyArtifactKindChatFixtureReport:        refs.ChatReportRef,
	}
	for index, binding := range manifest.Artifacts {
		if seenKinds[binding.Kind] || seenRefs[binding.Ref] || !t188SafeBasename(binding.Ref) ||
			binding.Ref != wantRefs[binding.Kind] || !t188SHA256Digest(binding.SHA256) || binding.SizeBytes <= 0 {
			t.Fatalf("artifact binding %d is not a unique typed basename/hash/size fact", index)
		}
		seenKinds[binding.Kind] = true
		seenRefs[binding.Ref] = true
		payload, err := os.ReadFile(filepath.Join(root, binding.Ref))
		if err != nil || int64(len(payload)) != binding.SizeBytes || t188Digest(payload) != binding.SHA256 {
			t.Fatalf("artifact %d does not match its on-disk bytes", index)
		}
		document, err := store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, binding.Kind, input))
		if err != nil || document.Kind != binding.Kind || !bytes.Equal(document.Content, payload) {
			t.Fatalf("Read(%q) did not return the bound typed bytes", binding.Kind)
		}
		assertT188TypedArtifactDocument(t, payload, binding.Kind, input)
	}
	if len(seenRefs) != 5 || len(seenKinds) != len(wantKinds) {
		t.Fatal("writer refs must expose one manifest plus the four typed artifacts")
	}

	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 5 {
		t.Fatalf("published directory entries = %d, want manifest plus four artifacts", len(entries))
	}
	for _, entry := range entries {
		info, statErr := os.Lstat(filepath.Join(root, entry.Name()))
		if statErr != nil {
			t.Fatalf("published file %q cannot be inspected", entry.Name())
		}
		stat, ok := info.Sys().(*unix.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
			t.Fatalf("published file %q is not owned regular mode-0600 nlink=1", entry.Name())
		}
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal("artifact root cannot be inspected")
	}
	rootStat, ok := rootInfo.Sys().(*unix.Stat_t)
	if !ok || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 || int(rootStat.Uid) != os.Geteuid() {
		t.Fatal("artifact root must be an owned real mode-0700 directory")
	}
	assertT188TreeContainsNoForbiddenBytes(t, root)
}

// TestPrivacyArtifactStoreKeepsManifestInvisibleUntilEveryArtifactIsDurable 使用包内
// failpoint 逐个截断发布流程。manifest 是唯一 commit point，因此任一前置落盘失败都不能
// 留下可解析状态；准备发布 manifest 时，四个 typed artifact 必须已经完整可读。
func TestPrivacyArtifactStoreKeepsManifestInvisibleUntilEveryArtifactIsDurable(t *testing.T) {
	stages := []privacyArtifactPublishStage{
		privacyArtifactPublishAPISummary,
		privacyArtifactPublishApplicationLog,
		privacyArtifactPublishCollectorComposite,
		privacyArtifactPublishChatReport,
		privacyArtifactPublishManifest,
	}
	for _, failStage := range stages {
		t.Run(string(failStage), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "privacy-artifacts")
			violations := make(chan string, 1)
			store, err := openPrivacyArtifactStoreForTest(root, privacyArtifactStoreTestHooks{
				BeforePublish: func(stage privacyArtifactPublishStage) error {
					hasManifest, artifactCount, inspectErr := t188InspectPublishedTree(root)
					if inspectErr != nil || hasManifest || (stage == privacyArtifactPublishManifest && artifactCount != 4) {
						select {
						case violations <- "publish order violated":
						default:
						}
						return errT188InjectedPublishFailure
					}
					if stage == failStage {
						return errT188InjectedPublishFailure
					}
					return nil
				},
			})
			if err != nil {
				t.Fatalf("test store open failed: %q", t188ErrorClass(err))
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.Write(context.Background(), t188ArtifactInput(t)); err == nil {
				t.Fatal("injected publish failure was ignored")
			} else {
				assertT188LowSensitiveError(t, err)
			}
			select {
			case violation := <-violations:
				t.Fatal(violation)
			default:
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatal("failed transaction retained a manifest, artifact, or temporary file")
			}
		})
	}
}

func TestPrivacyArtifactStoreBindsOneFixtureOnceAcrossConcurrentHandles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "privacy-artifacts")
	first := t188OpenStore(t, root)
	second := t188OpenStore(t, root)
	input := t188ArtifactInput(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, store := range []*PrivacyArtifactStore{first, second} {
		go func(store *PrivacyArtifactStore) {
			<-start
			_, err := store.Write(ctx, input)
			results <- err
		}(store)
	}
	close(start)
	succeeded := 0
	for range 2 {
		var err error
		select {
		case err = <-results:
		case <-ctx.Done():
			t.Fatal("concurrent one-time binding did not finish within its deadline")
		}
		if err == nil {
			succeeded++
		} else {
			assertT188LowSensitiveError(t, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent successful bindings = %d, want exactly one", succeeded)
	}
	before := t188DirectorySnapshot(t, root)
	if _, err := first.Write(context.Background(), input); err == nil {
		t.Fatal("a published fixture was overwritten by a later binding")
	}
	after := t188DirectorySnapshot(t, root)
	if !bytes.Equal(before, after) {
		t.Fatal("failed duplicate binding changed the committed artifact set")
	}
	variants := []PrivacyFixtureArtifactInput{input, input}
	variants[0].Marker = "marker-t188-different"
	variants[0].ChatReport = t188ChatReport(t, variants[0], "chat", nil)
	variants[1].RunID = "run-t188-different"
	variants[1].ChatReport = t188ChatReport(t, variants[1], "chat", nil)
	for _, variant := range variants {
		if _, err := first.Write(context.Background(), variant); err == nil {
			t.Fatal("one-time binding was bypassed by changing only run or marker")
		}
		if changed := t188DirectorySnapshot(t, root); !bytes.Equal(before, changed) {
			t.Fatal("rejected one-time binding changed committed bytes")
		}
	}
	newFixture := input
	newFixture.RunID = "run-t188-second"
	newFixture.Marker = "marker-t188-second"
	newFixture.RequestID = "request-t188-second"
	newFixture.AITraceID = "ai-trace-t188-second"
	newFixture.ChatReport = t188ChatReport(t, newFixture, "chat", nil)
	if _, err := first.Write(context.Background(), newFixture); err != nil {
		t.Fatal("a completely new fixture must not be blocked by prior one-time bindings")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 10 {
		t.Fatal("losing writer left partial or temporary artifacts")
	}
	assertT188TreeContainsNoForbiddenBytes(t, root)
}

func TestPrivacyArtifactStoreFailsClosedWithoutPublishingSensitiveOrPartialFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PrivacyFixtureArtifactInput)
	}{
		{name: "raw response", mutate: func(input *PrivacyFixtureArtifactInput) { input.ApplicationLogProjection.Body = t188RawResponse }},
		{name: "canary", mutate: func(input *PrivacyFixtureArtifactInput) { input.ApplicationLogProjection.Body = t188Canary }},
		{name: "authorization", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.ApplicationLogProjection.Body = "Authorization: Bearer " + t188Authorization
		}},
		{name: "credential", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.ApplicationLogProjection.Body = "sk-proj-t188forbiddencredential000000"
		}},
		{name: "token", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.ApplicationLogProjection.Body = "token=t188-forbidden-token"
		}},
		{name: "recognized PII", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.ApplicationLogProjection.Body = "privacy-t188@example.com"
		}},
		{name: "API map key", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.APIScanSummary[t188Canary] = 0
		}},
		{name: "application route", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.ApplicationLogProjection.Attributes["route"] = "/" + t188Authorization
		}},
		{name: "collector component", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.CollectorCompositeProof.ComponentIdentity = t188RawResponse
		}},
		{name: "collector admission", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.CollectorCompositeProof.ExportAdmissionCorrelation = "token=" + t188Authorization
		}},
		{name: "dynamic canary in free collector fact", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.CollectorCompositeProof.ExportAdmissionCorrelation = "admission-" + input.ForbiddenCanary
		}},
		{name: "invalid window", mutate: func(input *PrivacyFixtureArtifactInput) { input.Deadline = input.StartedAt }},
		{name: "oversized window", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.Deadline = input.StartedAt.Add(time.Minute + time.Nanosecond)
		}},
		{name: "missing chat report", mutate: func(input *PrivacyFixtureArtifactInput) { input.ChatReport = nil }},
		{name: "current privacy report", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.ChatReport = t188ChatReport(t, *input, "privacy", nil)
		}},
		{name: "foreign chat report", mutate: func(input *PrivacyFixtureArtifactInput) {
			foreign := *input
			foreign.RunID = "run-t188-foreign"
			input.ChatReport = t188ChatReport(t, foreign, "chat", nil)
		}},
		{name: "oversized typed artifact", mutate: func(input *PrivacyFixtureArtifactInput) {
			input.ApplicationLogProjection.Body = strings.Repeat("x", maximumPrivacyArtifactBytes+1)
		}},
		{name: "missing run", mutate: func(input *PrivacyFixtureArtifactInput) { input.RunID = "" }},
		{name: "missing canary policy", mutate: func(input *PrivacyFixtureArtifactInput) { input.ForbiddenCanary = "" }},
		{name: "invalid canary policy", mutate: func(input *PrivacyFixtureArtifactInput) { input.ForbiddenCanary = "bad\ncanary" }},
		{name: "missing request", mutate: func(input *PrivacyFixtureArtifactInput) { input.RequestID = "" }},
		{name: "missing AI trace", mutate: func(input *PrivacyFixtureArtifactInput) { input.AITraceID = "" }},
		{name: "invalid service trace", mutate: func(input *PrivacyFixtureArtifactInput) { input.ServiceTraceID = "short" }},
		{name: "invalid span", mutate: func(input *PrivacyFixtureArtifactInput) { input.SpanID = "short" }},
		{name: "zero start", mutate: func(input *PrivacyFixtureArtifactInput) { input.StartedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "privacy-artifacts")
			store := t188OpenStore(t, root)
			input := t188ArtifactInput(t)
			tt.mutate(&input)
			if _, err := store.Write(context.Background(), input); err == nil {
				t.Fatal("unsafe fixture input was persisted")
			} else {
				reportBytes, _ := json.Marshal(input.ChatReport)
				assertT188LowSensitiveError(t, err, input.RunID, input.Marker, input.ApplicationLogProjection.Body,
					fmt.Sprint(input.ApplicationLogProjection.Attributes["route"]), input.CollectorCompositeProof.ComponentIdentity,
					input.CollectorCompositeProof.ExportAdmissionCorrelation, string(reportBytes))
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed write published a manifest, artifact, or temporary file")
			}
			assertT188TreeContainsNoForbiddenBytes(t, root)
		})
	}
	t.Run("nil context", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "privacy-artifacts")
		store := t188OpenStore(t, root)
		if _, err := store.Write(nil, t188ArtifactInput(t)); err == nil {
			t.Fatal("nil context was accepted")
		}
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Fatal("nil context published artifact state")
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "privacy-artifacts")
		store := t188OpenStore(t, root)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.Write(ctx, t188ArtifactInput(t)); err == nil {
			t.Fatal("canceled context was accepted")
		}
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Fatal("canceled context published artifact state")
		}
	})
}

// TestPrivacyArtifactStoreDoesNotExposeCallerReportedProof 防止调用方用 bool/hash/path
// 自报“已经读取并验证”。可信性只能来自 concrete store 完成 open/fstat/bounded-read/hash/decode。
func TestPrivacyArtifactStoreDoesNotExposeCallerReportedProof(t *testing.T) {
	for _, value := range []any{PrivacyFixtureArtifactInput{}, PrivacyArtifactResolveRequest{}, PrivacyArtifactReadRequest{}} {
		typeOf := reflect.TypeOf(value)
		for _, name := range []string{"Hash", "SHA256", "HashVerified", "Attempted", "Protected", "Verified", "ArtifactRef", "Path", "RawBody"} {
			if _, exists := typeOf.FieldByName(name); exists {
				t.Fatalf("caller input %s exposes forgeable proof field %s", typeOf.Name(), name)
			}
		}
	}
	readMethod, ok := reflect.TypeOf((*PrivacyArtifactStore)(nil)).MethodByName("Read")
	if !ok || readMethod.Type.NumOut() != 2 {
		t.Fatal("concrete store must expose the typed Read contract")
	}
	resultType := readMethod.Type.Out(0)
	if resultType.Kind() == reflect.Pointer {
		resultType = resultType.Elem()
	}
	for _, name := range []string{"HashVerified", "Attempted", "Protected", "Verified", "Path", "RawBody"} {
		if _, exists := resultType.FieldByName(name); exists {
			t.Fatalf("read result exposes forgeable/raw field %s", name)
		}
	}
}
