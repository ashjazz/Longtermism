package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const maxChatRunManifestBytes = 4096

var (
	errChatRunManifest = errors.New("chat run manifest operation failed")
	safeManifestID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$`)
	hexTraceID         = regexp.MustCompile(`^[a-f0-9]{32}$`)
	hexSpanID          = regexp.MustCompile(`^[a-f0-9]{16}$`)
)

// ChatRunManifestInput is deliberately payload-free. It binds the protected marker to the
// public correlation pair and native OTel identity without persisting prompts or credentials.
type ChatRunManifestInput struct {
	SmokeRunID     string `json:"smoke_run_id"`
	RequestID      string `json:"request_id"`
	AITraceID      string `json:"ai_trace_id"`
	ServiceTraceID string `json:"service_trace_id"`
	SpanID         string `json:"span_id"`
}

type ChatRunManifestWriter interface {
	Write(context.Context, ChatRunManifestInput) error
}

type ChatRunManifestStore struct {
	root      string
	directory *os.File
	mu        sync.RWMutex
	closed    bool
}

func OpenChatRunManifestStore(root string) (*ChatRunManifestStore, error) {
	clean := filepath.Clean(root)
	if root == "" || !filepath.IsAbs(clean) {
		return nil, errChatRunManifest
	}
	_, statErr := os.Lstat(clean)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, errChatRunManifest
	}
	if created {
		if err := os.MkdirAll(clean, 0o700); err != nil {
			return nil, errChatRunManifest
		}
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errChatRunManifest
	}
	if created && os.Chmod(clean, 0o700) != nil || !created && info.Mode().Perm() != 0o700 {
		return nil, errChatRunManifest
	}
	directoryFD, err := unix.Open(clean, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errChatRunManifest
	}
	directory := os.NewFile(uintptr(directoryFD), clean)
	if directory == nil {
		_ = unix.Close(directoryFD)
		return nil, errChatRunManifest
	}
	return &ChatRunManifestStore{root: clean, directory: directory}, nil
}

func (store *ChatRunManifestStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed || store.directory == nil {
		return nil
	}
	store.closed = true
	return store.directory.Close()
}

func (store *ChatRunManifestStore) Write(ctx context.Context, input ChatRunManifestInput) error {
	if store == nil {
		return errChatRunManifest
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed || ctx == nil || ctx.Err() != nil || !validChatRunManifest(input) {
		return errChatRunManifest
	}
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > maxChatRunManifestBytes {
		return errChatRunManifest
	}
	token, err := randomManifestToken()
	if err != nil {
		return errChatRunManifest
	}
	temporary := ".tmp-" + token
	final := input.SmokeRunID + ".json"
	file, err := store.openFileAt(temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return errChatRunManifest
	}
	cleanup := func() { _ = file.Close(); _ = store.unlinkAt(temporary) }
	if _, err = file.Write(payload); err != nil || file.Sync() != nil || file.Close() != nil {
		cleanup()
		return errChatRunManifest
	}
	if err = unix.Linkat(store.directoryFD(), temporary, store.directoryFD(), final, 0); err != nil {
		_ = store.unlinkAt(temporary)
		return errChatRunManifest
	}
	_ = store.unlinkAt(temporary)
	if err = store.directory.Sync(); err != nil {
		return errChatRunManifest
	}
	return nil
}

// Consume claims the manifest with an atomic rename before reading it. Concurrent readers can
// therefore never both observe the same run identity.
func (store *ChatRunManifestStore) Consume(ctx context.Context, smokeRunID string) (ChatRunManifestInput, error) {
	if store == nil {
		return ChatRunManifestInput{}, errChatRunManifest
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed || ctx == nil || ctx.Err() != nil || !safeManifestID.MatchString(smokeRunID) {
		return ChatRunManifestInput{}, errChatRunManifest
	}
	token, err := randomManifestToken()
	if err != nil {
		return ChatRunManifestInput{}, errChatRunManifest
	}
	final := smokeRunID + ".json"
	claim := ".claim-" + token
	if err = unix.Renameat(store.directoryFD(), final, store.directoryFD(), claim); err != nil {
		return ChatRunManifestInput{}, errChatRunManifest
	}
	claimed := true
	defer func() {
		if claimed {
			_ = store.unlinkAt(claim)
			_ = store.directory.Sync()
		}
	}()
	file, err := store.openFileAt(claim, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ChatRunManifestInput{}, errChatRunManifest
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxChatRunManifestBytes {
		_ = file.Close()
		return ChatRunManifestInput{}, errChatRunManifest
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxChatRunManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest ChatRunManifestInput
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validChatRunManifest(manifest) || manifest.SmokeRunID != smokeRunID {
		_ = file.Close()
		return ChatRunManifestInput{}, errChatRunManifest
	}
	if file.Close() != nil || store.unlinkAt(claim) != nil {
		return ChatRunManifestInput{}, errChatRunManifest
	}
	claimed = false
	if err = store.directory.Sync(); err != nil {
		return ChatRunManifestInput{}, errChatRunManifest
	}
	return manifest, nil
}

func validChatRunManifest(input ChatRunManifestInput) bool {
	return safeManifestID.MatchString(input.SmokeRunID) && safeManifestID.MatchString(input.RequestID) &&
		safeManifestID.MatchString(input.AITraceID) && hexTraceID.MatchString(input.ServiceTraceID) &&
		hexSpanID.MatchString(input.SpanID) && !strings.Contains(strings.ToLower(input.RequestID+input.AITraceID), "credential")
}

func randomManifestToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (store *ChatRunManifestStore) directoryFD() int {
	if store == nil || store.directory == nil {
		return -1
	}
	return int(store.directory.Fd())
}

func (store *ChatRunManifestStore) openFileAt(name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(store.directoryFD(), name, flags|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errChatRunManifest
	}
	return file, nil
}

func (store *ChatRunManifestStore) unlinkAt(name string) error {
	return unix.Unlinkat(store.directoryFD(), name, 0)
}
