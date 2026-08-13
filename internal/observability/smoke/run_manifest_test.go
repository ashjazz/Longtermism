package smoke

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestChatRunManifestStorePublishesOnceAndConsumesOnce(t *testing.T) {
	store, err := OpenChatRunManifestStore(filepath.Join(t.TempDir(), "manifests"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	input := ChatRunManifestInput{SmokeRunID: "run-t182-manifest", RequestID: "req-t182-manifest", AITraceID: "ai-t182-manifest", ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"}
	if err := store.Write(context.Background(), input); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := filepath.Join(store.root, input.SmokeRunID+".json")
	if info, err := os.Lstat(path); err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("manifest mode/type = %v, %v", info, err)
	}
	if err := store.Write(context.Background(), input); err == nil {
		t.Fatal("duplicate write must not overwrite")
	}

	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			_, consumeErr := store.Consume(context.Background(), input.SmokeRunID)
			results <- consumeErr
		}()
	}
	start.Done()
	succeeded := 0
	for range 2 {
		if <-results == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful consumers = %d, want 1", succeeded)
	}
}

func TestChatRunManifestStoreRejectsUnsafeFilesAndIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "manifests")
	store, err := OpenChatRunManifestStore(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.Write(context.Background(), ChatRunManifestInput{SmokeRunID: "../escape"}); err == nil {
		t.Fatal("unsafe identity accepted")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "run-t182-symlink.json")); err != nil {
		t.Fatal(err)
	}
	input := ChatRunManifestInput{SmokeRunID: "run-t182-symlink", RequestID: "req-t182-symlink", AITraceID: "ai-t182-symlink", ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"}
	if err := store.Write(context.Background(), input); err == nil {
		t.Fatal("symlink target accepted")
	}
	if got, _ := os.ReadFile(target); string(got) != "safe" {
		t.Fatal("symlink target changed")
	}
}

func TestChatRunManifestStoreKeepsOperationsBoundToOpenedDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "manifests")
	store, err := OpenChatRunManifestStore(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// 生产风险：仅在启动时 Lstat 路径不足以抵御运行中的目录替换。store 必须始终
	// 相对已验证的目录句柄操作，不能跟随后来植入到原路径的 symlink。
	original := filepath.Join(parent, "opened-manifests")
	attacker := filepath.Join(parent, "attacker")
	if err := os.Rename(root, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, root); err != nil {
		t.Fatal(err)
	}
	input := ChatRunManifestInput{SmokeRunID: "run-t182-root-swap", RequestID: "req-t182-root-swap", AITraceID: "ai-t182-root-swap", ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"}
	if err := store.Write(context.Background(), input); err != nil {
		t.Fatalf("write through bound directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(attacker, input.SmokeRunID+".json")); !os.IsNotExist(err) {
		t.Fatal("root replacement received a protected manifest")
	}
	if _, err := os.Lstat(filepath.Join(original, input.SmokeRunID+".json")); err != nil {
		t.Fatal("opened directory did not receive the protected manifest")
	}
	if _, err := store.Consume(context.Background(), input.SmokeRunID); err != nil {
		t.Fatalf("consume through bound directory: %v", err)
	}
}

func TestChatRunManifestStoreSerializesCloseWithDirectoryOperations(t *testing.T) {
	store, err := OpenChatRunManifestStore(filepath.Join(t.TempDir(), "manifests"))
	if err != nil {
		t.Fatal(err)
	}
	input := ChatRunManifestInput{SmokeRunID: "run-t182-close-race", RequestID: "req-t182-close-race", AITraceID: "ai-t182-close-race", ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"}
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_ = store.Write(context.Background(), input)
		close(done)
	}()
	<-started
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := store.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
	if err := store.Write(context.Background(), input); !errors.Is(err, errChatRunManifest) {
		t.Fatal("operation after close must fail closed")
	}
}
