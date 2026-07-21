package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	"github.com/gogf/gf/v2/os/gcfg"
)

// The middleware is the sole JSONL producer in the live server. A smoke marker must be
// projected from its header here; otherwise Loki cannot prove that a log belongs to this run.
func TestHTTPCompletionIdentityProjectsOnlyValidatedInfraSmokeMarkers(t *testing.T) {
	tests := []struct {
		name           string
		marker         string
		wantSmokeRunID string
	}{
		{name: "projects valid marker", marker: "run-smoke-marker", wantSmokeRunID: "run-smoke-marker"},
		{name: "rejects short marker", marker: "short"},
		{name: "rejects unsafe marker", marker: "run marker"},
		{name: "rejects oversized marker", marker: "run-" + strings.Repeat("a", 125)},
		{name: "does not mark an omitted optional marker"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8000/api/v1/observability/infra-smoke", nil)
			request.Header.Set(v1observability.SmokeRunIDHeader, tt.marker)
			request = request.WithContext(context.WithValue(request.Context(), routeTemplateContextKey{}, "/api/v1/observability/infra-smoke"))
			identity := httpCompletionIdentity(request)
			if identity.IsSmokeRun != (tt.wantSmokeRunID != "") || identity.SmokeRunID != tt.wantSmokeRunID {
				t.Fatalf("smoke identity = %#v, want marker %q", identity, tt.wantSmokeRunID)
			}
		})
	}
}

// TestGrafanaSmokeConfigExampleIsStandaloneAndLoopbackBound prevents the local runbook from
// drifting into a partial override that GoFrame would never load. The selected file must contain
// every safety-critical value needed for a local smoke run, including a loopback-only HTTP entry.
func TestGrafanaSmokeConfigExampleIsStandaloneAndLoopbackBound(t *testing.T) {
	t.Parallel()

	adapter, err := gcfg.NewAdapterFile(filepath.Join("..", "..", "manifest", "config", "config.grafana-smoke.example.yaml"))
	if err != nil {
		t.Fatalf("load Grafana smoke configuration example: %v", err)
	}
	config := gcfg.NewWithAdapter(adapter)

	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "application only listens on loopback", key: "server.address", want: "127.0.0.1:8000"},
		{name: "observability is enabled", key: "observability.enabled", want: "true"},
		{name: "collector mode is selected", key: "observability.mode", want: "collector"},
		{name: "application only knows the local collector", key: "observability.collector.endpoint", want: "127.0.0.1:4317"},
		{name: "completion logs stay under the ignored project runtime directory", key: "observability.logs.path", want: "resource/log/observability"},
		{name: "traces are enabled", key: "observability.signals.traces_enabled", want: "true"},
		{name: "metrics are enabled", key: "observability.signals.metrics_enabled", want: "true"},
		{name: "smoke route is explicitly enabled", key: "observability.smoke.enabled", want: "true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.MustGet(context.Background(), tt.key).String()
			if got != tt.want {
				t.Fatalf("%s = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestOpenHTTPCompletionLogCreatesParentDirectory reproduces the local-host startup failure:
// OpenFile creates the log file but not its parent directory. Local smoke must remain runnable
// without asking developers to create or elevate access to /var/log manually.
func TestOpenHTTPCompletionLogCreatesParentDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "observability")
	file, err := openHTTPCompletionLog(path, "application.jsonl")
	if err != nil {
		t.Fatalf("open completion log: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	info, err := os.Stat(filepath.Join(path, "application.jsonl"))
	if err != nil {
		t.Fatalf("stat created completion log: %v", err)
	}
	if info.IsDir() {
		t.Fatal("completion log path is a directory")
	}
}

func TestOpenHTTPCompletionLogRejectsInvalidFilesystemInputs(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	notDirectory := filepath.Join(temporary, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("fixture"), 0600); err != nil {
		t.Fatalf("write non-directory fixture: %v", err)
	}

	tests := []struct {
		name string
		path string
		file string
	}{
		{name: "missing directory path", path: "", file: "application.jsonl"},
		{name: "missing file name", path: temporary, file: ""},
		{name: "parent path is a regular file", path: notDirectory, file: "application.jsonl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := openHTTPCompletionLog(tt.path, tt.file)
			if err == nil {
				_ = file.Close()
				t.Fatal("openHTTPCompletionLog() error = nil, want non-nil")
			}
		})
	}
}

func TestResolveHTTPCompletionLogPathRejectsUnknownConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		fallback string
		want     string
		wantErr  bool
	}{
		{name: "host smoke path", path: "resource/log/observability", fallback: "/var/log/longtermism", want: "resource/log/observability"},
		{name: "container profile path", path: "/var/log/longtermism", fallback: "/var/log/longtermism", want: "/var/log/longtermism"},
		{name: "unknown path is rejected", path: "/tmp/unreviewed", fallback: "/var/log/longtermism", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHTTPCompletionLogPath(tt.path, tt.fallback)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveHTTPCompletionLogPath() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("resolveHTTPCompletionLogPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveHTTPCompletionLogFileRejectsTraversal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		wantErr bool
	}{
		{name: "expected JSONL file", file: "application.jsonl"},
		{name: "parent traversal", file: "../../outside.jsonl", wantErr: true},
		{name: "absolute path", file: "/tmp/outside.jsonl", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHTTPCompletionLogFile(tt.file)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveHTTPCompletionLogFile() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != "application.jsonl" {
				t.Fatalf("resolveHTTPCompletionLogFile() = %q, want application.jsonl", got)
			}
		})
	}
}

func TestOpenHTTPCompletionLogRestoresGroupReadPermission(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	logPath := filepath.Join(path, "application.jsonl")
	if err := os.WriteFile(logPath, []byte("historical log"), 0600); err != nil {
		t.Fatalf("write restrictive log fixture: %v", err)
	}
	if err := os.Chmod(logPath, 0600); err != nil {
		t.Fatalf("chmod restrictive log fixture: %v", err)
	}

	file, err := openHTTPCompletionLog(path, "application.jsonl")
	if err != nil {
		t.Fatalf("open restrictive completion log: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat repaired completion log: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0640); got != want {
		t.Fatalf("completion log mode = %04o, want %04o", got, want)
	}
}

func TestOpenHTTPCompletionLogRejectsSymbolicLink(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	target := filepath.Join(temporary, "outside.jsonl")
	if err := os.WriteFile(target, []byte("must remain unchanged"), 0600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	logPath := filepath.Join(temporary, "application.jsonl")
	if err := os.Symlink(target, logPath); err != nil {
		t.Fatalf("create log symlink: %v", err)
	}

	file, err := openHTTPCompletionLog(temporary, "application.jsonl")
	if err == nil {
		_ = file.Close()
		t.Fatal("openHTTPCompletionLog() error = nil, want symlink rejection")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if got, want := string(content), "must remain unchanged"; got != want {
		t.Fatalf("symlink target content = %q, want %q", got, want)
	}
}
