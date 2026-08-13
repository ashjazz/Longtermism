package smoke

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPrivacyArtifactStoreRejectsUnsafeRootAndRuntimeReplacement(t *testing.T) {
	parent := t.TempDir()
	unsafeMode := filepath.Join(parent, "unsafe-mode")
	if err := os.Mkdir(unsafeMode, 0o755); err != nil {
		t.Fatal(err)
	}
	realAncestor := filepath.Join(parent, "real-ancestor")
	if err := os.Mkdir(realAncestor, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkAncestor := filepath.Join(parent, "symlink-ancestor")
	if err := os.Symlink(realAncestor, symlinkAncestor); err != nil {
		t.Fatal(err)
	}
	finalTarget := filepath.Join(parent, "final-target")
	if err := os.Mkdir(finalTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	finalSymlink := filepath.Join(parent, "final-symlink")
	if err := os.Symlink(finalTarget, finalSymlink); err != nil {
		t.Fatal(err)
	}
	tests := []string{"", "relative/artifacts", unsafeMode, finalSymlink, filepath.Join(symlinkAncestor, "artifacts")}
	for _, root := range tests {
		if store, err := OpenPrivacyArtifactStore(root); err == nil || store != nil {
			t.Fatal("unsafe root must fail before creating a store")
		} else {
			assertT188LowSensitiveError(t, err, root)
		}
	}

	root := filepath.Join(parent, "bound-root")
	store := t188OpenStore(t, root)
	original := filepath.Join(parent, "opened-root")
	attacker := filepath.Join(parent, "attacker-root")
	if err := os.Rename(root, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, root); err != nil {
		t.Fatal(err)
	}
	input := t188ArtifactInput(t)
	refs, err := store.Write(context.Background(), input)
	if err != nil {
		t.Fatalf("bound-dirfd write failed with class %q", t188ErrorClass(err))
	}
	if entries, _ := os.ReadDir(attacker); len(entries) != 0 {
		t.Fatal("runtime root replacement received protected artifacts")
	}
	if _, err := os.Lstat(filepath.Join(original, refs.ManifestRef)); err != nil {
		t.Fatal("opened directory inode did not receive the manifest")
	}
	for _, ref := range []string{refs.ManifestRef, refs.APISummaryRef, refs.ApplicationLogRef, refs.CollectorArtifactRef, refs.ChatReportRef} {
		if err := os.WriteFile(filepath.Join(attacker, ref), []byte(`{"attacker":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := store.Resolve(context.Background(), t188ResolveRequest(refs.ManifestRef, input))
	if err != nil || manifest.RunID != input.RunID {
		t.Fatal("Resolve followed the replaced root instead of the opened directory inode")
	}
	for _, kind := range []PrivacyArtifactKind{PrivacyArtifactKindAPISummary, PrivacyArtifactKindApplicationLogProjection, PrivacyArtifactKindCollectorCompositeProof, PrivacyArtifactKindChatFixtureReport} {
		if _, err := store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, kind, input)); err != nil {
			t.Fatalf("Read(%q) followed the replaced root instead of the opened directory inode", kind)
		}
	}
}

// TestPrivacyArtifactStoreSyscallGuardsFailClosed verifies the lowest-level helpers before
// they reach openat/linkat. During shutdown, a nil directory or unsafe basename must never be
// translated into an operation relative to an unintended recycled descriptor.
func TestPrivacyArtifactStoreSyscallGuardsFailClosed(t *testing.T) {
	if validOpenPrivacyArtifactDirectory(nil) || validOpenPrivacyArtifactFile(nil, 1) || validCompletedPrivacyArtifactFile(nil, 1) {
		t.Fatal("nil descriptors must never satisfy artifact metadata validation")
	}
	if _, err := readPrivacyArtifactAt(nil, "artifact.json"); err == nil {
		t.Fatal("nil directory read was accepted")
	}
	if err := publishPrivacyArtifactAt(nil, "artifact.json", []byte(`{}`)); err == nil {
		t.Fatal("nil directory publish was accepted")
	}
	if err := unlinkPrivacyArtifactAt(nil, "artifact.json"); err == nil {
		t.Fatal("nil directory unlink was accepted")
	}
	unlockPrivacyArtifactDirectory(nil)
	var store *PrivacyArtifactStore
	if err := store.Close(); err != nil {
		t.Fatalf("nil store Close() must be harmless, got %q", t188ErrorClass(err))
	}
}

func TestPrivacyArtifactStoreRejectsUntrustedRefsAndForeignIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "privacy-artifacts")
	store := t188OpenStore(t, root)
	input := t188ArtifactInput(t)
	refs, err := store.Write(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*PrivacyArtifactResolveRequest)
	}{
		{name: "empty manifest", mutate: func(request *PrivacyArtifactResolveRequest) { request.ManifestRef = "" }},
		{name: "absolute manifest", mutate: func(request *PrivacyArtifactResolveRequest) {
			request.ManifestRef = filepath.Join(root, refs.ManifestRef)
		}},
		{name: "parent traversal", mutate: func(request *PrivacyArtifactResolveRequest) { request.ManifestRef = "../" + refs.ManifestRef }},
		{name: "nested path", mutate: func(request *PrivacyArtifactResolveRequest) { request.ManifestRef = "nested/" + refs.ManifestRef }},
		{name: "backslash path", mutate: func(request *PrivacyArtifactResolveRequest) { request.ManifestRef = `nested\` + refs.ManifestRef }},
		{name: "unregistered basename", mutate: func(request *PrivacyArtifactResolveRequest) { request.ManifestRef = "unregistered-t188.json" }},
		{name: "foreign run", mutate: func(request *PrivacyArtifactResolveRequest) { request.RunID = "run-t188-foreign" }},
		{name: "foreign marker", mutate: func(request *PrivacyArtifactResolveRequest) { request.Marker = "marker-t188-foreign" }},
		{name: "foreign request", mutate: func(request *PrivacyArtifactResolveRequest) { request.RequestID = "request-t188-foreign" }},
		{name: "foreign AI", mutate: func(request *PrivacyArtifactResolveRequest) { request.AITraceID = "ai-trace-t188-foreign" }},
		{name: "foreign trace", mutate: func(request *PrivacyArtifactResolveRequest) {
			request.ServiceTraceID = "ffffffffffffffffffffffffffffffff"
		}},
		{name: "foreign span", mutate: func(request *PrivacyArtifactResolveRequest) { request.SpanID = "ffffffffffffffff" }},
		{name: "foreign window", mutate: func(request *PrivacyArtifactResolveRequest) { request.StartedAt = request.StartedAt.Add(time.Second) }},
		{name: "foreign deadline", mutate: func(request *PrivacyArtifactResolveRequest) { request.Deadline = request.Deadline.Add(-time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := t188ResolveRequest(refs.ManifestRef, input)
			tt.mutate(&request)
			if _, err := store.Resolve(context.Background(), request); err == nil {
				t.Fatal("untrusted manifest ref or foreign identity was accepted")
			} else {
				assertT188LowSensitiveError(t, err, request.ManifestRef, request.RunID, request.Marker)
			}
		})
	}
	if _, err := store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, PrivacyArtifactKind("privacy_report"), input)); err == nil {
		t.Fatal("current privacy report must never become the prior chat-report surface")
	}
	if _, err := store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, PrivacyArtifactKind("unknown"), input)); err == nil {
		t.Fatal("unknown artifact kind was accepted")
	}
	registered := filepath.Join(root, refs.APISummaryRef)
	unregistered := filepath.Join(root, "unregistered-t188.json")
	if err := os.WriteFile(unregistered, t188ReadFile(t, registered), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(registered); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, PrivacyArtifactKindAPISummary, input)); err == nil {
		t.Fatal("reader searched the directory for an unregistered substitute artifact")
	} else {
		assertT188LowSensitiveError(t, err, unregistered)
	}
}

func TestPrivacyArtifactStoreRejectsUnsafeFileTypesPermissionsAndTampering(t *testing.T) {
	mutations := []struct {
		name     string
		isolated bool
		mutate   func(t *testing.T, path string, original []byte)
	}{
		{name: "valid final symlink", mutate: func(t *testing.T, path string, original []byte) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			target := path + "-regular-target"
			if err := os.WriteFile(target, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(target), path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			if err := os.Link(path, path+".hardlink"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", isolated: true, mutate: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unix socket", isolated: true, mutate: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			shortDir, err := os.MkdirTemp("/tmp", "t194-socket-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(shortDir) })
			shortPath := filepath.Join(shortDir, "socket")
			listener, err := net.Listen("unix", shortPath)
			if err != nil {
				t188SkipUnavailableUnixSocket(t, err)
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			if err := os.Rename(shortPath, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "device", isolated: true, mutate: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mknod(path, unix.S_IFCHR|0o600, 0); err != nil {
				t.Skip("creating a device node requires an unavailable local capability")
			}
		}},
		{name: "wrong mode", mutate: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "same-size hash mismatch", mutate: func(t *testing.T, path string, original []byte) {
			t.Helper()
			tampered := bytes.Repeat([]byte{'x'}, len(original))
			if err := os.WriteFile(path, tampered, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized", mutate: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maximumPrivacyArtifactBytes + 1); err != nil {
				t.Fatal(err)
			}
			_ = file.Close()
		}},
	}
	targets := []struct {
		name string
		kind PrivacyArtifactKind
		ref  func(PrivacyFixtureArtifactRefs) string
	}{
		{name: "manifest", ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.ManifestRef }},
		{name: "api", kind: PrivacyArtifactKindAPISummary, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.APISummaryRef }},
		{name: "application-log", kind: PrivacyArtifactKindApplicationLogProjection, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.ApplicationLogRef }},
		{name: "collector", kind: PrivacyArtifactKindCollectorCompositeProof, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.CollectorArtifactRef }},
		{name: "chat-report", kind: PrivacyArtifactKindChatFixtureReport, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.ChatReportRef }},
	}
	for _, target := range targets {
		for _, mutation := range mutations {
			t.Run(target.name+"/"+mutation.name, func(t *testing.T) {
				if mutation.isolated {
					t188RunSpecialFileSubprocess(t, target.name, mutation.name)
					return
				}
				root := filepath.Join(t.TempDir(), "privacy-artifacts")
				store, err := OpenPrivacyArtifactStore(root)
				if err != nil {
					t.Fatalf("OpenPrivacyArtifactStore() failed with class %q", t188ErrorClass(err))
				}
				input := t188ArtifactInput(t)
				refs, err := store.Write(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, target.ref(refs))
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				mutation.mutate(t, path, original)
				err = t188ReadTarget(store, refs.ManifestRef, target.kind, input)
				_ = store.Close()
				if err == nil {
					t.Fatal("unsafe or tampered manifest/artifact was accepted")
				}
				assertT188LowSensitiveError(t, err, path, input.RunID, t188Canary)
			})
		}
	}
}

func TestPrivacyArtifactStoreSpecialFileChild(t *testing.T) {
	targetName := os.Getenv("T188_SPECIAL_TARGET")
	mutationName := os.Getenv("T188_SPECIAL_MUTATION")
	if targetName == "" || mutationName == "" {
		t.Skip("subprocess-only special-file contract")
	}
	root := filepath.Join(t.TempDir(), "privacy-artifacts")
	store := t188OpenStore(t, root)
	input := t188ArtifactInput(t)
	refs, err := store.Write(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	kind, ref := t188TargetByName(t, targetName, refs)
	path := filepath.Join(root, ref)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	switch mutationName {
	case "fifo":
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	case "unix socket":
		shortDir, err := os.MkdirTemp("/tmp", "t194-socket-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(shortDir)
		shortPath := filepath.Join(shortDir, "socket")
		listener, err := net.Listen("unix", shortPath)
		if err != nil {
			t188SkipUnavailableUnixSocket(t, err)
			t.Fatal(err)
		}
		defer listener.Close()
		if err := os.Rename(shortPath, path); err != nil {
			t.Fatal(err)
		}
	case "device":
		if err := unix.Mknod(path, unix.S_IFCHR|0o600, 0); err != nil {
			t.Skip("creating a device node requires an unavailable local capability")
		}
	default:
		t.Fatal("unknown isolated mutation")
	}
	if err := t188ReadTarget(store, refs.ManifestRef, kind, input); err == nil {
		t.Fatal("special file was accepted")
	}
}

func t188SkipUnavailableUnixSocket(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.EOPNOTSUPP) {
		t.Skip("creating a Unix socket requires an unavailable local capability")
	}
}

func TestPrivacyArtifactStoreRequiresStrictManifestAndArtifactJSON(t *testing.T) {
	tests := []struct {
		name      string
		manifest  bool
		transform func([]byte) []byte
	}{
		{name: "manifest unknown field", manifest: true, transform: t188AppendUnknownField},
		{name: "manifest trailing JSON", manifest: true, transform: func(payload []byte) []byte { return append(payload, []byte(` {}`)...) }},
		{name: "manifest duplicate key", manifest: true, transform: t188DuplicateFirstKey},
		{name: "manifest nested duplicate key", manifest: true, transform: func(payload []byte) []byte { return t188DuplicateNamedKey(payload, "kind") }},
		{name: "manifest invalid UTF-8", manifest: true, transform: func(payload []byte) []byte { return append(payload, 0xff) }},
		{name: "artifact unknown field", transform: t188AppendUnknownField},
		{name: "artifact trailing JSON", transform: func(payload []byte) []byte { return append(payload, []byte(` {}`)...) }},
		{name: "artifact duplicate key", transform: t188DuplicateFirstKey},
		{name: "artifact nested duplicate key", transform: func(payload []byte) []byte { return t188DuplicateNamedKey(payload, "synthetic_canary") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "privacy-artifacts")
			store := t188OpenStore(t, root)
			input := t188ArtifactInput(t)
			refs, err := store.Write(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			ref := refs.APISummaryRef
			if tt.manifest {
				ref = refs.ManifestRef
			}
			path := filepath.Join(root, ref)
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tampered := tt.transform(payload)
			if err := os.WriteFile(path, tampered, 0o600); err != nil {
				t.Fatal(err)
			}
			if !tt.manifest {
				t188RebindArtifactBytes(t, root, refs.ManifestRef, refs.APISummaryRef, tampered)
			}
			if tt.manifest {
				_, err = store.Resolve(context.Background(), t188ResolveRequest(refs.ManifestRef, input))
			} else {
				_, err = store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, PrivacyArtifactKindAPISummary, input))
			}
			if err == nil {
				t.Fatal("non-strict JSON was accepted")
			}
			assertT188LowSensitiveError(t, err, string(payload), path)
		})
	}
}

func TestPrivacyArtifactStoreStrictlyDecodesEveryTypedArtifact(t *testing.T) {
	targets := []struct {
		kind PrivacyArtifactKind
		ref  func(PrivacyFixtureArtifactRefs) string
	}{
		{kind: PrivacyArtifactKindApplicationLogProjection, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.ApplicationLogRef }},
		{kind: PrivacyArtifactKindCollectorCompositeProof, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.CollectorArtifactRef }},
		{kind: PrivacyArtifactKindChatFixtureReport, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.ChatReportRef }},
	}
	transforms := []struct {
		name string
		fn   func([]byte) []byte
	}{
		{name: "unknown", fn: t188AppendUnknownField},
		{name: "trailing", fn: func(payload []byte) []byte { return append(payload, []byte(` {}`)...) }},
		{name: "duplicate", fn: t188DuplicateFirstKey},
	}
	for _, target := range targets {
		for _, transform := range transforms {
			t.Run(string(target.kind)+"/"+transform.name, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "privacy-artifacts")
				store := t188OpenStore(t, root)
				input := t188ArtifactInput(t)
				refs, err := store.Write(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				ref := target.ref(refs)
				path := filepath.Join(root, ref)
				payload := transform.fn(t188ReadFile(t, path))
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				t188RebindArtifactBytes(t, root, refs.ManifestRef, ref, payload)
				if _, err := store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, target.kind, input)); err == nil {
					t.Fatal("typed artifact accepted non-strict JSON")
				}
			})
		}
	}
}

func TestPrivacyArtifactStoreRejectsArtifactIdentityEvenWithMatchingManifestHash(t *testing.T) {
	targets := []struct {
		kind PrivacyArtifactKind
		ref  func(PrivacyFixtureArtifactRefs) string
	}{
		{kind: PrivacyArtifactKindAPISummary, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.APISummaryRef }},
		{kind: PrivacyArtifactKindApplicationLogProjection, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.ApplicationLogRef }},
		{kind: PrivacyArtifactKindCollectorCompositeProof, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.CollectorArtifactRef }},
		{kind: PrivacyArtifactKindChatFixtureReport, ref: func(refs PrivacyFixtureArtifactRefs) string { return refs.ChatReportRef }},
	}
	tests := []struct {
		name  string
		field string
		value any
	}{
		{name: "kind", field: "kind", value: string(PrivacyArtifactKindChatFixtureReport)},
		{name: "run", field: "run_id", value: "run-t188-foreign"},
		{name: "marker", field: "marker", value: "marker-t188-foreign"},
		{name: "request", field: "request_id", value: "request-t188-foreign"},
		{name: "AI trace", field: "ai_trace_id", value: "ai-trace-t188-foreign"},
		{name: "service trace", field: "service_trace_id", value: "ffffffffffffffffffffffffffffffff"},
		{name: "span", field: "span_id", value: "ffffffffffffffff"},
		{name: "started at", field: "started_at", value: "2020-01-01T00:00:00Z"},
		{name: "deadline", field: "deadline", value: "2020-01-01T00:00:01Z"},
	}
	for _, target := range targets {
		for _, tt := range tests {
			if tt.field == "kind" && tt.value == string(target.kind) {
				continue
			}
			t.Run(string(target.kind)+"/"+tt.name, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "privacy-artifacts")
				store := t188OpenStore(t, root)
				input := t188ArtifactInput(t)
				refs, err := store.Write(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				ref := target.ref(refs)
				path := filepath.Join(root, ref)
				payload := t188RewriteJSONObject(t, t188ReadFile(t, path), func(document map[string]any) { document[tt.field] = tt.value })
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				t188RebindArtifactBytes(t, root, refs.ManifestRef, ref, payload)
				if _, err := store.Read(context.Background(), t188ReadRequest(refs.ManifestRef, target.kind, input)); err == nil {
					t.Fatal("foreign artifact identity was trusted after only matching its manifest hash")
				} else {
					assertT188LowSensitiveError(t, err, fmt.Sprint(tt.value))
				}
			})
		}
	}
}

func TestPrivacyArtifactStoreRejectsInvalidManifestBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, manifest map[string]any, refs PrivacyFixtureArtifactRefs)
	}{
		{name: "absolute ref", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["ref"] = "/tmp/t188.json"
		}},
		{name: "parent ref", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["ref"] = "../t188.json"
		}},
		{name: "nested ref", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["ref"] = "nested/t188.json"
		}},
		{name: "backslash ref", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["ref"] = `nested\t188.json`
		}},
		{name: "duplicate ref", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 1)["ref"] = t188ManifestBinding(t, manifest, 0)["ref"]
		}},
		{name: "duplicate kind", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 1)["kind"] = t188ManifestBinding(t, manifest, 0)["kind"]
		}},
		{name: "unknown kind", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["kind"] = "unknown"
		}},
		{name: "manifest self ref", mutate: func(t *testing.T, manifest map[string]any, refs PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["ref"] = refs.ManifestRef
		}},
		{name: "bad hash", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["sha256"] = "sha256:BAD"
		}},
		{name: "bad size", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			t188ManifestBinding(t, manifest, 0)["size_bytes"] = float64(0)
		}},
		{name: "missing kind", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			delete(t188ManifestBinding(t, manifest, 0), "kind")
		}},
		{name: "fifth artifact", mutate: func(t *testing.T, manifest map[string]any, _ PrivacyFixtureArtifactRefs) {
			manifest["artifacts"] = append(manifest["artifacts"].([]any), map[string]any{"kind": "unknown", "ref": "fifth.json", "sha256": "sha256:" + strings.Repeat("a", 64), "size_bytes": float64(1)})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "privacy-artifacts")
			store := t188OpenStore(t, root)
			input := t188ArtifactInput(t)
			refs, err := store.Write(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, refs.ManifestRef)
			payload := t188RewriteJSONObject(t, t188ReadFile(t, path), func(manifest map[string]any) { tt.mutate(t, manifest, refs) })
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Resolve(context.Background(), t188ResolveRequest(refs.ManifestRef, input)); err == nil {
				t.Fatal("invalid manifest binding was accepted")
			} else {
				assertT188LowSensitiveError(t, err, string(payload))
			}
		})
	}
}
