package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	v1chat "github.com/ashjazz/Longtermism/api/v1/chat"
	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/backend"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

var infrastructureCommandDeadline = time.Date(2026, time.July, 20, 0, 1, 0, 0, time.UTC)

// TestRunInfraCommandContract fixes the CLI boundary before its real Grafana assembly exists.
// The command may report a completed low-sensitivity report, but it must never become a second
// owner of the run identity or expose backend credentials/raw response data in CI output.
func TestRunInfraCommandContract(t *testing.T) {
	passedReport := newInfrastructureCommandReport(t, "passed")
	failedReport := newInfrastructureCommandReport(t, "failed")
	skippedReport := newInfrastructureCommandReport(t, "skipped")

	tests := []struct {
		name           string
		args           []string
		resolveConfig  func(context.Context) (infraCommandConfig, error)
		newRunnerErr   error
		runnerResult   *smoke.SmokeReport
		runnerErr      error
		writerPath     string
		writerErr      error
		wantExitCode   int
		wantStatus     string
		wantConfigCall int
		wantNewCall    int
		wantRunnerCall int
		wantWriteCall  int
		wantStdout     bool
		forbidden      []string
	}{
		{
			name:           "passed report writes a low sensitivity summary",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			runnerResult:   passedReport,
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   0,
			wantStatus:     "passed",
			wantRunnerCall: 1,
			wantWriteCall:  1,
			wantStdout:     true,
			forbidden:      []string{"run-cli-contract", "marker-cli-contract", "super-secret", "raw backend response"},
		},
		{
			name:           "failed report is persisted before returning verification failure",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			runnerResult:   failedReport,
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			wantStatus:     "failed",
			wantRunnerCall: 1,
			wantWriteCall:  1,
			wantStdout:     true,
			forbidden:      []string{"run-cli-contract", "marker-cli-contract", "super-secret", "raw backend response"},
		},
		{
			name:           "skipped report is persisted but never treated as success",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			runnerResult:   skippedReport,
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			wantStatus:     "skipped",
			wantRunnerCall: 1,
			wantWriteCall:  1,
			wantStdout:     true,
			forbidden:      []string{"run-cli-contract", "marker-cli-contract", "super-secret", "raw backend response"},
		},
		{
			name:           "runner operational error has no report or sensitive diagnostic",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			runnerErr:      errors.New("raw backend response Authorization: Bearer super-secret"),
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			wantRunnerCall: 1,
			wantWriteCall:  0,
			forbidden:      []string{"super-secret", "Authorization", "raw backend response"},
		},
		{
			name:           "nil report without error is an operational failure",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			wantRunnerCall: 1,
			wantWriteCall:  0,
			forbidden:      []string{"run-cli-contract", "marker-cli-contract", "super-secret"},
		},
		{
			name: "missing configuration fails before runner construction",
			args: []string{"-profile", "grafana"},
			resolveConfig: func(context.Context) (infraCommandConfig, error) {
				return infraCommandConfig{}, errMissingInfrastructureCommandConfig
			},
			wantConfigCall: 1,
			wantNewCall:    0,
			wantExitCode:   2,
			wantRunnerCall: 0,
			wantWriteCall:  0,
			forbidden:      []string{"super-secret", "Authorization", "http://", "https://"},
		},
		{
			name: "configuration error never echoes endpoint or credential",
			args: []string{"-profile", "grafana"},
			resolveConfig: func(context.Context) (infraCommandConfig, error) {
				return infraCommandConfig{}, errors.New("https://grafana.example Authorization: Bearer super-secret")
			},
			wantConfigCall: 1,
			wantNewCall:    0,
			wantExitCode:   2,
			forbidden:      []string{"super-secret", "Authorization", "https://grafana.example"},
		},
		{
			name:           "caller cannot provide runner owned marker or run identity",
			args:           []string{"-profile", "grafana", "-marker", "marker-cli-contract", "-run-id", "run-cli-contract"},
			resolveConfig:  validInfrastructureCommandConfig,
			wantConfigCall: 0,
			wantNewCall:    0,
			wantExitCode:   2,
			wantRunnerCall: 0,
			wantWriteCall:  0,
			forbidden:      []string{"marker-cli-contract", "run-cli-contract", "super-secret"},
		},
		{
			name:           "runner construction error has a stable non-sensitive diagnostic",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			newRunnerErr:   errors.New("https://grafana.example/api Authorization: Bearer super-secret"),
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			forbidden:      []string{"super-secret", "Authorization", "https://grafana.example"},
		},
		{
			name:           "report write error does not use stdout as a raw report fallback",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			runnerResult:   passedReport,
			writerErr:      errors.New("raw backend response Authorization: Bearer super-secret"),
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			wantRunnerCall: 1,
			wantWriteCall:  1,
			forbidden:      []string{"super-secret", "Authorization", "raw backend response"},
		},
		{
			name:           "untrusted report path is never printed",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			runnerResult:   passedReport,
			writerPath:     "/private/tmp/super-secret-report.json",
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			wantRunnerCall: 1,
			wantWriteCall:  1,
			forbidden:      []string{"/private/tmp", "super-secret-report.json"},
		},
		{
			name:           "report path cannot escape the ignored directory",
			args:           []string{"-profile", "grafana"},
			resolveConfig:  validInfrastructureCommandConfig,
			runnerResult:   passedReport,
			writerPath:     "../super-secret-report.json",
			wantConfigCall: 1,
			wantNewCall:    1,
			wantExitCode:   1,
			wantRunnerCall: 1,
			wantWriteCall:  1,
			forbidden:      []string{"../", "super-secret-report.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			runner := &fakeInfrastructureCommandRunner{report: tt.runnerResult, err: tt.runnerErr}
			writer := &fakeInfrastructureReportWriter{path: "build/observability/smoke-reports/infra-report.json", err: tt.writerErr}
			if tt.writerPath != "" {
				writer.path = tt.writerPath
			}
			configCalls := 0
			newRunnerCalls := 0

			exitCode := runInfra(context.Background(), tt.args, &stdout, &stderr, infraCommandDependencies{
				ResolveConfig: func(ctx context.Context) (infraCommandConfig, error) {
					configCalls++
					return tt.resolveConfig(ctx)
				},
				NewRunner: func(infraCommandConfig) (infraCommandRunner, error) {
					newRunnerCalls++
					if tt.newRunnerErr != nil {
						return nil, tt.newRunnerErr
					}
					return runner, nil
				},
				WriteReport: writer.Write,
			})

			if exitCode != tt.wantExitCode {
				t.Fatalf("runInfra() exit code = %d, want %d; stderr=%q", exitCode, tt.wantExitCode, stderr.String())
			}
			if configCalls != tt.wantConfigCall {
				t.Fatalf("configuration resolution calls = %d, want %d", configCalls, tt.wantConfigCall)
			}
			if newRunnerCalls != tt.wantNewCall {
				t.Fatalf("runner construction calls = %d, want %d", newRunnerCalls, tt.wantNewCall)
			}
			if runner.calls != tt.wantRunnerCall {
				t.Fatalf("runner calls = %d, want %d", runner.calls, tt.wantRunnerCall)
			}
			if writer.calls != tt.wantWriteCall {
				t.Fatalf("report write calls = %d, want %d", writer.calls, tt.wantWriteCall)
			}
			if tt.wantRunnerCall > 0 && runner.request.Profile != "grafana" {
				t.Fatalf("runner profile = %q, want grafana", runner.request.Profile)
			}
			if tt.wantRunnerCall > 0 && !runner.request.Deadline.Equal(infrastructureCommandDeadline) {
				t.Fatalf("runner deadline = %s, want %s", runner.request.Deadline, infrastructureCommandDeadline)
			}
			if tt.wantWriteCall > 0 && writer.directory != "build/observability/smoke-reports" {
				t.Fatalf("report directory = %q, want ignored smoke report directory", writer.directory)
			}

			if tt.wantStdout {
				assertInfrastructureCommandOutput(t, stdout.Bytes(), tt.wantStatus, writer.path)
			} else if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			assertNoSensitiveCommandOutput(t, stdout.String()+stderr.String(), tt.forbidden)
		})
	}
}

func validInfrastructureCommandConfig(context.Context) (infraCommandConfig, error) {
	return infraCommandConfig{
		Profile:         "grafana",
		Deadline:        infrastructureCommandDeadline,
		ReportDirectory: "build/observability/smoke-reports",
	}, nil
}

type fakeInfrastructureCommandRunner struct {
	report  *smoke.SmokeReport
	err     error
	calls   int
	request smoke.InfrastructureSmokeRequest
}

func (r *fakeInfrastructureCommandRunner) Run(_ context.Context, request smoke.InfrastructureSmokeRequest) (*smoke.SmokeReport, error) {
	r.calls++
	r.request = request
	return r.report, r.err
}

type fakeInfrastructureReportWriter struct {
	path      string
	err       error
	calls     int
	directory string
}

func (w *fakeInfrastructureReportWriter) Write(directory string, _ *smoke.SmokeReport) (string, error) {
	w.calls++
	w.directory = directory
	return w.path, w.err
}

func newInfrastructureCommandReport(t *testing.T, status string) *smoke.SmokeReport {
	t.Helper()

	checkStatus := "passed"
	failureStage := "none"
	errorClass := ""
	matchedSpans := int64(1)
	if status == "failed" {
		checkStatus = "failed"
		failureStage = "query"
		errorClass = "query_failed"
		matchedSpans = 0
	}
	if status == "skipped" {
		checkStatus = "skipped"
	}

	report, err := smoke.BuildSmokeReport(smoke.SmokeReportInput{
		RunID:      "run-cli-contract",
		Marker:     "marker-cli-contract",
		Profile:    "grafana",
		Scenario:   "infra",
		StartedAt:  time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC),
		Deadline:   time.Date(2026, time.July, 20, 0, 1, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, time.July, 20, 0, 0, 1, 0, time.UTC),
		Checks: []smoke.BackendCheckInput{{
			Backend: "tempo", Status: checkStatus, FailureStage: failureStage, ErrorClass: errorClass,
			Evidence: map[string]any{"matched_spans": matchedSpans},
		}},
		Cleanup: smoke.SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
	if err != nil {
		t.Fatalf("BuildSmokeReport() error = %v", err)
	}
	return report
}

func assertInfrastructureCommandOutput(t *testing.T, output []byte, wantStatus, wantPath string) {
	t.Helper()

	var decoded infraCommandOutput
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatalf("stdout is not infra command output JSON: %v; stdout=%s", err, output)
	}
	if decoded.Status != wantStatus || decoded.ReportPath != wantPath {
		t.Fatalf("command output = %#v, want status=%q report_path=%q", decoded, wantStatus, wantPath)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output, &fields); err != nil {
		t.Fatalf("stdout is not an object: %v", err)
	}
	if len(fields) != 2 || fields["status"] == nil || fields["report_path"] == nil {
		t.Fatalf("stdout fields = %#v, want only status and report_path", fields)
	}
}

func assertNoSensitiveCommandOutput(t *testing.T, output string, forbidden []string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(output, value) {
			t.Fatalf("command output leaked forbidden value %q: %s", value, output)
		}
	}
}

// TestProtectedInfrastructureSmokeTriggerContract fixes the only HTTP effect the command may
// perform. The runner owns the marker; the CLI merely forwards it through the versioned API
// contract under the same bounded context, never through a query parameter or request body.
func TestProtectedInfrastructureSmokeTriggerContract(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	identity := smoke.InfrastructureSmokeIdentity{RunID: "run-t065d-contract", Marker: "marker-t065d-contract"}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/observability/infra-smoke" || request.URL.RawQuery != "" || request.ContentLength != 0 || len(body) != 0 {
			t.Errorf("request = method:%s path:%s query:%q, want bounded GET contract", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		if marker := request.Header.Get(v1observability.SmokeRunIDHeader); marker != identity.Marker {
			t.Errorf("marker header = %q, want runner-owned marker %q", marker, identity.Marker)
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(`{"code":0,"message":"ok","data":{"status":"ok"},"meta":{"request_id":"req-t065d"}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	baseClient := server.Client()
	client := *baseClient
	client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if got, ok := request.Context().Deadline(); !ok || !got.Equal(deadline) {
			t.Errorf("client request deadline = %s present:%v, want %s", got, ok, deadline)
		}
		return baseClient.Transport.RoundTrip(request)
	})
	trigger, err := newProtectedInfrastructureSmokeTrigger(server.URL, &client)
	if err != nil {
		t.Fatalf("newProtectedInfrastructureSmokeTrigger() error = %v", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := trigger(ctx, identity); err != nil {
		t.Fatalf("protected trigger error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("protected trigger requests = %d, want 1", requests)
	}
}

func TestProtectedInfrastructureSmokeTriggerRejectsInvalidResponses(t *testing.T) {
	identity := smoke.InfrastructureSmokeIdentity{RunID: "run-t065d-contract", Marker: "marker-t065d-contract"}
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "disabled route", status: http.StatusNotFound, body: `{"code":404}`},
		{name: "rate limited route", status: http.StatusTooManyRequests, body: `{"code":429}`},
		{name: "server failure", status: http.StatusBadGateway, body: `{"code":502}`},
		{name: "malformed response", status: http.StatusOK, body: `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()
			trigger, err := newProtectedInfrastructureSmokeTrigger(server.URL, server.Client())
			if err != nil {
				t.Fatalf("newProtectedInfrastructureSmokeTrigger() error = %v", err)
			}
			if err := trigger(context.Background(), identity); err == nil || strings.Contains(err.Error(), "{") {
				t.Fatalf("trigger error = %v, want a stable non-sensitive failure", err)
			}
		})
	}
}

// TestInfrastructureReportWriterRejectsEscapes protects ignored smoke artifacts from becoming a
// generic file-write primitive. The writer must reject lexical and symlink escapes before any
// external file can be created or overwritten.
func TestInfrastructureReportWriterRejectsEscapes(t *testing.T) {
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	report := newInfrastructureCommandReport(t, "passed")

	tests := []struct {
		name      string
		prepare   func(t *testing.T, workspace, outside string)
		wantError bool
	}{
		{name: "normal report stays below fixed root", prepare: func(_ *testing.T, _, _ string) {}},
		{name: "fixed root symlink", prepare: func(t *testing.T, workspace, outside string) {
			if err := os.MkdirAll(filepath.Join(workspace, "build", "observability"), 0750); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(workspace, "build", "observability", "smoke-reports")
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
		}, wantError: true},
		{name: "intermediate symlink", prepare: func(t *testing.T, workspace, outside string) {
			link := filepath.Join(workspace, "build")
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
		}, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			tt.prepare(t, workspace, outside)
			writer, err := newContainedInfrastructureReportWriter(workspace)
			if err != nil {
				t.Fatalf("newContainedInfrastructureReportWriter() error = %v", err)
			}
			path, err := writer.Write(report)
			if tt.wantError {
				if err == nil {
					t.Fatal("Write() error = nil, want containment rejection")
				}
				contents, readErr := os.ReadFile(sentinel)
				if readErr != nil || string(contents) != "unchanged" {
					t.Fatalf("outside sentinel was changed: %q error:%v", contents, readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			root := filepath.Join(workspace, infrastructureSmokeReportDirectory)
			relative, err := filepath.Rel(root, path)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				t.Fatalf("report path = %q, escaped directory %q", path, root)
			}
		})
	}
}

// TestInfrastructureReportWriterRechecksBeforeWrite guards the time between composition and
// persistence: an attacker must not be able to replace a previously safe component with a
// symlink and turn the ignored report directory into an external write target.
func TestInfrastructureReportWriterRechecksBeforeWrite(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	writer, err := newContainedInfrastructureReportWriter(workspace)
	if err != nil {
		t.Fatalf("newContainedInfrastructureReportWriter() error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "build")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(newInfrastructureCommandReport(t, "passed")); err == nil {
		t.Fatal("Write() error = nil, want write-time symlink rejection")
	}
	after, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("outside directory entries = %v, want unchanged %v", after, before)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// TestDefaultInfrastructureAssemblyGuards keeps the production composition root honest without
// starting Docker or querying a backend. The default command is allowed to construct clients only
// after every required reference is present and every Grafana-facing endpoint is loopback-only.
func TestDefaultInfrastructureAssemblyGuards(t *testing.T) {
	tests := []struct {
		name       string
		overrides  map[string]string
		wantConfig bool
		wantRunner bool
	}{
		{
			name:       "missing query reference fails before client construction",
			overrides:  map[string]string{},
			wantConfig: false,
		},
		{
			name: "remote application endpoint is rejected before client construction",
			overrides: map[string]string{
				"LONGTERMISM_SMOKE_APP_BASE_URL":              "https://example.invalid",
				"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL": "http://127.0.0.1:9090",
				"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL":       "http://127.0.0.1:3100",
				"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL":      "http://127.0.0.1:3200",
				"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL":   "http://127.0.0.1:3001",
				"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL": "test-langfuse-credential",
				"LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL":   "http://127.0.0.1:8000",
				"LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL": "test-ai-plane-credential",
			},
			wantConfig: false,
		},
		{
			name: "complete loopback references construct the runner without network IO",
			overrides: map[string]string{
				"LONGTERMISM_SMOKE_APP_BASE_URL":              "http://127.0.0.1:8000",
				"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL": "http://127.0.0.1:9090",
				"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL":       "http://127.0.0.1:3100",
				"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL":      "http://127.0.0.1:3200",
				"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL":   "http://127.0.0.1:3001",
				"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL": "test-langfuse-credential",
				"LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL":   "http://127.0.0.1:8000",
				"LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL": "test-ai-plane-credential",
			},
			wantConfig: true,
			wantRunner: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDefaultInfrastructureEnvironment(t, tt.overrides)
			config, err := resolveDefaultInfrastructureCommandConfig(context.Background())
			if (err == nil) != tt.wantConfig {
				t.Fatalf("resolveDefaultInfrastructureCommandConfig() error = %v, want config=%v", err, tt.wantConfig)
			}
			if !tt.wantConfig {
				if !errors.Is(err, errMissingInfrastructureCommandConfig) {
					t.Fatalf("configuration error = %v, want stable missing-config error", err)
				}
				return
			}
			if config.Profile != "grafana" || config.ReportDirectory != infrastructureSmokeReportDirectory {
				t.Fatalf("config = %#v, want fixed grafana profile and report directory", config)
			}
			if remaining := time.Until(config.Deadline); remaining <= 0 || remaining > infrastructureSmokeTimeout {
				t.Fatalf("config deadline remaining = %s, want bounded future deadline", remaining)
			}
			runner, runnerErr := newDefaultInfrastructureCommandRunner(config)
			if (runnerErr == nil) != tt.wantRunner || (runnerErr == nil && runner == nil) {
				t.Fatalf("newDefaultInfrastructureCommandRunner() runner=%T error=%v, want runner=%v", runner, runnerErr, tt.wantRunner)
			}
		})
	}
}

func setDefaultInfrastructureEnvironment(t *testing.T, overrides map[string]string) {
	t.Helper()
	keys := []string{
		"LONGTERMISM_SMOKE_APP_BASE_URL",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL",
		"LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL",
	}
	for _, key := range keys {
		t.Setenv(key, overrides[key])
	}
}

func TestLocalSmokeURLAndTransportGuards(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{name: "loopback IPv4 is accepted", endpoint: "http://127.0.0.1:8000"},
		{name: "localhost is resolved and accepted", endpoint: "http://localhost:8000"},
		{name: "remote host is rejected", endpoint: "https://example.invalid", wantErr: true},
		{name: "non HTTP scheme is rejected", endpoint: "ftp://127.0.0.1:8000", wantErr: true},
		{name: "non loopback IP is rejected", endpoint: "http://127.0.0.2:8000", wantErr: true},
		{name: "credentials are rejected", endpoint: "http://user:secret@127.0.0.1:8000", wantErr: true},
		{name: "path override is rejected", endpoint: "http://127.0.0.1:8000/private", wantErr: true},
		{name: "query override is rejected", endpoint: "http://127.0.0.1:8000?target=private", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLocalSmokeBaseURL(tt.endpoint)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateLocalSmokeBaseURL(%q) error = %v, wantErr %v", tt.endpoint, err, tt.wantErr)
			}
		})
	}

	transport := newLocalSmokeTransport()
	if transport.Proxy != nil {
		t.Fatal("local smoke transport must not inherit an ambient proxy")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "example.invalid:443"); err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("remote dial error = %v, want non-loopback rejection", err)
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:not-a-port"); err == nil {
		t.Fatal("malformed loopback dial error = nil, want address rejection")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "localhost:1"); err == nil {
		t.Fatal("closed localhost port dial error = nil, want connection failure after loopback resolution")
	}
}

func TestDefaultCommandHelpersRejectMissingReportsAndCanceledWaits(t *testing.T) {
	if _, err := newContainedInfrastructureReportWriter(""); !errors.Is(err, errMissingInfrastructureCommandConfig) {
		t.Fatalf("empty workspace error = %v, want missing configuration", err)
	}
	writer, err := newContainedInfrastructureReportWriter(t.TempDir())
	if err != nil {
		t.Fatalf("newContainedInfrastructureReportWriter() error = %v", err)
	}
	if _, err := writer.Write(nil); err == nil {
		t.Fatal("Write(nil) error = nil, want missing report rejection")
	}

	clock := systemPollerClock{}
	if clock.Now().IsZero() {
		t.Fatal("systemPollerClock.Now() returned zero time")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.Wait(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("systemPollerClock.Wait() error = %v, want context cancellation", err)
	}

	dependencies := defaultInfraCommandDependencies()
	if dependencies.ResolveConfig == nil || dependencies.NewRunner == nil || dependencies.WriteReport == nil {
		t.Fatal("default infra command dependencies must provide every composition port")
	}

	workspace := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	dependencies = defaultInfraCommandDependencies()
	path, err := dependencies.WriteReport(infrastructureSmokeReportDirectory, newInfrastructureCommandReport(t, "passed"))
	if err != nil {
		t.Fatalf("default report writer error = %v", err)
	}
	if !isTrustedInfrastructureReportPath(infrastructureSmokeReportDirectory, path) {
		t.Fatalf("default report path = %q, want contained relative artifact", path)
	}
	if _, err := os.Stat(filepath.Join(workspace, path)); err != nil {
		t.Fatalf("default report artifact stat error = %v", err)
	}
}

func TestProtectedInfrastructureSmokeTriggerFailsClosedBeforeNetwork(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		identity smoke.InfrastructureSmokeIdentity
	}{
		{name: "invalid base URL", baseURL: "http://127.0.0.1:8000/unsafe"},
		{name: "runner identity is incomplete", baseURL: "http://127.0.0.1:8000", identity: smoke.InfrastructureSmokeIdentity{RunID: "run"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trigger, err := newProtectedInfrastructureSmokeTrigger(tt.baseURL, nil)
			if tt.name == "invalid base URL" {
				if !errors.Is(err, errProtectedInfrastructureTrigger) || trigger != nil {
					t.Fatalf("invalid base URL trigger=%v error=%v, want stable rejection", trigger, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("newProtectedInfrastructureSmokeTrigger() error = %v", err)
			}
			if err := trigger(context.Background(), tt.identity); !errors.Is(err, errProtectedInfrastructureTrigger) {
				t.Fatalf("trigger error = %v, want stable preflight rejection", err)
			}
		})
	}
}

// TestPrivacyProductionBackendAcceptsOnlyConcreteCapabilities keeps generic T180 fake ports out
// of the live composition root. Each dependency seals a real contained read or protected query;
// composition is allowed to project those proofs, never to accept caller-reported booleans.
func TestPrivacyProductionBackendAcceptsOnlyConcreteCapabilities(t *testing.T) {
	constructor := reflect.TypeOf(backend.NewPrivacySmokeBackend)
	want := reflect.TypeOf((func(
		*smoke.PrivacyArtifactStore,
		*backend.PrivacyLocalSurfaces,
		*backend.PrivacyGrafanaSurfaces,
		*backend.PrivacyLangfuseSurfaces,
		time.Duration,
	) (*backend.PrivacySmokeBackend, error))(nil))
	if constructor != want || constructor.IsVariadic() {
		t.Fatal("production privacy backend accepts a generic or forgeable proof dependency")
	}

	composition := reflect.TypeOf(newPrivacyCommandRunner)
	wantInputs := []reflect.Type{
		reflect.TypeOf((*smoke.ProtectedPrivacyFixtureTrigger)(nil)),
		reflect.TypeOf((*smoke.ChatRunManifestStore)(nil)),
		reflect.TypeOf((*backend.PrivacySmokeBackend)(nil)),
		reflect.TypeOf((*smoke.PollerClock)(nil)).Elem(),
	}
	if composition.Kind() != reflect.Func || composition.IsVariadic() || composition.NumIn() != len(wantInputs) {
		t.Fatal("production privacy composition root has a generic or incomplete dependency graph")
	}
	for index, wantInput := range wantInputs {
		if composition.In(index) != wantInput {
			t.Fatalf("production privacy composition input %d = %v, want %v", index, composition.In(index), wantInput)
		}
	}
	writer := reflect.TypeOf((*smoke.PrivacyFixtureArtifactWriter)(nil)).Elem()
	if !reflect.TypeOf((*backend.PrivacySmokeBackend)(nil)).Implements(writer) {
		t.Fatal("production backend does not expose its already-bound contained store as the fixture writer")
	}
}

// ---------------------------------------------------------------------------
// T181（RED）：chat/score/privacy live CLI composition 契约。
//
// 真实 live 场景必须显式 `--live` opt-in、固定 grafana profile、runner-owned
// identity；缺 opt-in 或任一 endpoint/credential/evidence/run-manifest reference
// 都在 runner/client/transport 之前退出且请求数为 0；退出码 passed=0、
// failed/skipped/runtime=1、usage/config=2；报告先安全持久化，stdout 严格只含
// scenario/status/可信 report path。
//
// 本测试钉死的符号（runLiveScenario、liveScenarioConfig、resolveDefaultLiveScenarioConfig
// 等）由 T108 在 cmd/obs-smoke/main.go 实现；在 T108 前本文件保持编译失败（RED）。
// ---------------------------------------------------------------------------

const liveScenarioTestCredential = "live-scenario-read-credential"

var liveScenarioTestDeadline = time.Date(2026, time.July, 21, 0, 1, 0, 0, time.UTC)

type liveScenarioTestRunner struct {
	report *smoke.SmokeReport
	err    error
	calls  int
}

func (r *liveScenarioTestRunner) Run(context.Context) (*smoke.SmokeReport, error) {
	r.calls++
	return r.report, r.err
}

func newLiveScenarioTestReport(t *testing.T, scenario, status string) *smoke.SmokeReport {
	t.Helper()
	var privacyEvidence []smoke.PrivacySmokeReportEvidenceInput
	if scenario == "privacy" {
		privacyEvidence = livePrivacyProofSet(status)
	}
	rebuilt, err := smoke.BuildSmokeReport(smoke.SmokeReportInput{
		RunID:           "run-live-" + scenario,
		Marker:          "marker-live-" + scenario,
		Profile:         "grafana",
		Scenario:        scenario,
		StartedAt:       liveScenarioTestDeadline.Add(-time.Second),
		Deadline:        liveScenarioTestDeadline,
		FinishedAt:      liveScenarioTestDeadline.Add(-time.Second),
		Checks:          reportChecksForLiveTest(status, scenario),
		Cleanup:         smoke.SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
		PrivacyEvidence: privacyEvidence,
	})
	if err != nil {
		t.Fatalf("BuildSmokeReport() error = %v", err)
	}
	return rebuilt
}

// livePrivacyProofSet 以 schema 固定顺序构造八 surface 证明集：privacy 报告必须携带
// 完整证明，不能以普通 check 冒充。
func livePrivacyProofSet(status string) []smoke.PrivacySmokeReportEvidenceInput {
	surfaces := []struct {
		surface smoke.PrivacySmokeSurface
		method  string
	}{
		{smoke.PrivacySmokeSurfaceAPI, "bounded_memory_scan"},
		{smoke.PrivacySmokeSurfaceApplicationLog, "projection_and_exact_query"},
		{smoke.PrivacySmokeSurfaceCollectorQueue, "configuration_and_telemetry"},
		{smoke.PrivacySmokeSurfaceTempo, "bounded_trace_document"},
		{smoke.PrivacySmokeSurfaceLoki, "exact_structured_query"},
		{smoke.PrivacySmokeSurfaceLangfuseTrace, "bounded_platform_document"},
		{smoke.PrivacySmokeSurfaceLangfuseScore, "bounded_platform_document"},
		{smoke.PrivacySmokeSurfaceReport, "contained_artifact_scan"},
	}
	evidence := make([]smoke.PrivacySmokeReportEvidenceInput, 0, len(surfaces))
	for _, item := range surfaces {
		evidence = append(evidence, smoke.PrivacySmokeReportEvidenceInput{
			Surface: item.surface, EvidenceMethod: item.method, Status: status,
			ScannerPolicyVersion: "1",
			Counts: map[string]int{
				"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0,
			},
			CollectorProofVerified: item.surface == smoke.PrivacySmokeSurfaceCollectorQueue,
		})
	}
	return evidence
}

func reportChecksForLiveTest(status, scenario string) []smoke.BackendCheckInput {
	checkStatus := "passed"
	failureStage := "none"
	if status == "failed" {
		checkStatus, failureStage = "failed", "query"
	}
	if status == "skipped" {
		checkStatus = "skipped"
	}
	backendByScenario := map[string]struct {
		backend string
		key     string
	}{
		"chat":    {backend: "langfuse_trace", key: "matched_traces"},
		"score":   {backend: "langfuse_score", key: "matched_scores"},
		"privacy": {backend: "privacy", key: "forbidden_marker_hits"},
	}
	pin := backendByScenario[scenario]
	return []smoke.BackendCheckInput{{Backend: pin.backend, Status: checkStatus, FailureStage: failureStage, Evidence: map[string]any{pin.key: int64(0)}}}
}

func TestRunLiveScenarioCommandContract(t *testing.T) {
	passedChat := newLiveScenarioTestReport(t, "chat", "passed")
	failedScore := newLiveScenarioTestReport(t, "score", "failed")
	skippedScore := newLiveScenarioTestReport(t, "score", "skipped")
	passedPrivacy := newLiveScenarioTestReport(t, "privacy", "passed")

	tests := []struct {
		name           string
		scenario       string
		args           []string
		resolveErr     error
		newRunnerErr   error
		runnerErr      error
		runnerResult   *smoke.SmokeReport
		writerPath     string
		writerErr      error
		wantExitCode   int
		wantConfigCall int
		wantNewCall    int
		wantRunnerCall int
		wantWriteCall  int
		wantStdoutJSON bool
		wantStatus     string
		forbidden      []string
	}{
		{
			name:     "passed chat run persists the report before the summary",
			scenario: "chat", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedChat, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 0, wantStdoutJSON: true, wantStatus: "passed",
			forbidden: []string{"marker-live-chat", liveScenarioTestCredential, "127.0.0.1", "raw backend response"},
		},
		{
			name:     "failed score report is persisted before the verification failure",
			scenario: "score", args: []string{"--live", "-profile", "grafana"},
			runnerResult: failedScore, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 1, wantStdoutJSON: true, wantStatus: "failed",
			forbidden: []string{"marker-live-score", liveScenarioTestCredential},
		},
		{
			name:     "skipped score report is never treated as success",
			scenario: "score", args: []string{"--live", "-profile", "grafana"},
			runnerResult: skippedScore, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 1, wantStdoutJSON: true, wantStatus: "skipped",
			forbidden: []string{"marker-live-score", liveScenarioTestCredential},
		},
		{
			name:     "passed privacy composition persists the report before the summary",
			scenario: "privacy", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedPrivacy, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 0, wantStdoutJSON: true, wantStatus: "passed",
			forbidden: []string{"marker-live-privacy", liveScenarioTestCredential, "synthetic_canary"},
		},
		{
			name:     "runner operational error has no report and no sensitive stdout",
			scenario: "chat", args: []string{"--live", "-profile", "grafana"},
			runnerErr:      errors.New("raw backend response Authorization: Bearer live-secret"),
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantExitCode: 1,
			forbidden: []string{"live-secret", "Authorization", "raw backend response"},
		},
		{
			name:     "nil report without error is an operational failure",
			scenario: "chat", args: []string{"--live", "-profile", "grafana"},
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantExitCode: 1,
			forbidden: []string{"marker-live-chat"},
		},
		{
			name:     "missing live opt-in exits before any composition call",
			scenario: "chat", args: []string{"-profile", "grafana"},
			runnerResult: passedChat, wantExitCode: 2,
		},
		{
			name:     "non grafana profile is a usage failure",
			scenario: "chat", args: []string{"--live", "-profile", "signoz"},
			runnerResult: passedChat, wantExitCode: 2,
		},
		{
			name:     "caller supplied marker flags are rejected",
			scenario: "chat", args: []string{"--live", "-profile", "grafana", "-marker", "forged-marker"},
			runnerResult: passedChat, wantExitCode: 2,
		},
		{
			name:     "unknown scenario is a usage failure",
			scenario: "teleport", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedChat, wantExitCode: 2,
		},
		{
			name:     "missing scenario configuration fails before runner construction",
			scenario: "privacy", args: []string{"--live", "-profile", "grafana"},
			resolveErr:   errMissingInfrastructureCommandConfig,
			runnerResult: passedChat, wantConfigCall: 1, wantExitCode: 2,
		},
		{
			name:     "runner construction failure is a runtime failure",
			scenario: "score", args: []string{"--live", "-profile", "grafana"},
			newRunnerErr: errors.New("score composition unavailable"), wantConfigCall: 1,
			wantNewCall: 1, wantExitCode: 1,
		},
		{
			name:     "report writer failure is a runtime failure",
			scenario: "chat", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedChat, writerErr: errors.New("write failed"),
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantWriteCall: 1, wantExitCode: 1,
		},
		{
			name:     "escaped report path is rejected",
			scenario: "chat", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedChat, writerPath: filepath.Join("..", "outside.json"),
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantWriteCall: 1, wantExitCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			runner := &liveScenarioTestRunner{report: tt.runnerResult, err: tt.runnerErr}
			configCalls, newCalls, writeCalls := 0, 0, 0
			reportWrittenBeforeSummary := true
			dependencies := liveScenarioCommandDependencies{
				ResolveConfig: func(_ context.Context, scenario string) (liveScenarioConfig, error) {
					configCalls++
					return liveScenarioConfig{Scenario: scenario, Profile: "grafana", Deadline: liveScenarioTestDeadline}, tt.resolveErr
				},
				NewRunner: func(config liveScenarioConfig) (liveScenarioCommandRunner, error) {
					newCalls++
					if tt.newRunnerErr != nil {
						return nil, tt.newRunnerErr
					}
					return runner, nil
				},
				WriteReport: func(directory string, report *smoke.SmokeReport) (string, error) {
					writeCalls++
					if tt.writerErr != nil {
						return "", tt.writerErr
					}
					if report == nil {
						return "", errors.New("missing report")
					}
					// 报告必须比 stdout 摘要更早安全持久化：写入时 stdout 必须仍然为空。
					if stdout.Len() != 0 {
						reportWrittenBeforeSummary = false
					}
					if tt.writerPath != "" {
						return tt.writerPath, nil
					}
					return filepath.Join("build/observability/smoke-reports", tt.scenario+"-report.json"), nil
				},
			}

			exitCode := runLiveScenario(context.Background(), tt.scenario, tt.args, stdout, stderr, dependencies)

			if exitCode != tt.wantExitCode {
				t.Fatalf("runLiveScenario() exit = %d, want %d (stderr: %s)", exitCode, tt.wantExitCode, stderr.String())
			}
			if configCalls != tt.wantConfigCall || newCalls != tt.wantNewCall || runner.calls != tt.wantRunnerCall || writeCalls != tt.wantWriteCall {
				t.Fatalf("composition calls = config:%d new:%d runner:%d write:%d, want config:%d new:%d runner:%d write:%d",
					configCalls, newCalls, runner.calls, writeCalls, tt.wantConfigCall, tt.wantNewCall, tt.wantRunnerCall, tt.wantWriteCall)
			}
			if !reportWrittenBeforeSummary {
				t.Fatal("report was not persisted before the stdout summary")
			}
			if !tt.wantStdoutJSON {
				if stdout.Len() != 0 {
					t.Fatalf("stdout = %q, want strictly empty", stdout.String())
				}
				for _, forbidden := range tt.forbidden {
					if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
						t.Fatalf("output leaked %q: stdout=%q stderr=%q", forbidden, stdout.String(), stderr.String())
					}
				}
				return
			}
			var output map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("stdout is not JSON: %v", err)
			}
			if len(output) != 3 || output["scenario"] != tt.scenario || output["status"] != tt.wantStatus || output["report_path"] == "" {
				t.Fatalf("stdout summary = %v, want only scenario/status/trusted report path", output)
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(stdout.String(), forbidden) {
					t.Fatalf("stdout leaked %q: %s", forbidden, stdout.String())
				}
			}
		})
	}
}

func TestLiveScenarioConfigCarriesNoRunnerIdentity(t *testing.T) {
	configType := reflect.TypeOf(liveScenarioConfig{})
	if configType.Kind() != reflect.Struct || configType.NumField() != 3 {
		t.Fatalf("liveScenarioConfig = %v, want a three-field low-sensitivity orchestration snapshot", configType)
	}
	for index, want := range []struct {
		name string
		kind reflect.Kind
	}{
		{name: "Scenario", kind: reflect.String},
		{name: "Profile", kind: reflect.String},
		{name: "Deadline", kind: reflect.Struct},
	} {
		field := configType.Field(index)
		if field.Name != want.name || field.Type.Kind() != want.kind {
			t.Fatalf("liveScenarioConfig field %d = %s (%v), want %s", index, field.Name, field.Type, want.name)
		}
		if strings.Contains(strings.ToLower(field.Name), "marker") || strings.Contains(strings.ToLower(field.Name), "runid") {
			t.Fatalf("liveScenarioConfig must never carry runner-owned identity field %q", field.Name)
		}
	}
}

// resolveDefaultLiveScenarioConfig 必须在任何 runner/client/transport 之前完成预检：
// 缺任一 scenario 必填 reference 立即失败。env 名称钉死在 LONGTERMISM_SMOKE_ 前缀下，
// T108 的默认装配必须逐字使用这些引用名。
func TestDefaultLiveScenarioConfigPreflight(t *testing.T) {
	chatRefs := []string{
		"LONGTERMISM_SMOKE_APP_BASE_URL",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL",
	}
	scoreRefs := []string{
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL",
		"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH",
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH",
	}
	privacyRefs := []string{
		"LONGTERMISM_SMOKE_APP_BASE_URL",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT",
		"LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL",
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH",
		"LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST",
		"LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY",
		"LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION",
	}
	scenarios := map[string][]string{"chat": chatRefs, "score": scoreRefs, "privacy": privacyRefs}

	values := func(scenario string) map[string]string {
		refs := map[string]string{
			"LONGTERMISM_SMOKE_APP_BASE_URL":                    "http://127.0.0.1:8000",
			"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION":              "chat-live-shared-credential",
			"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT":              "/var/folders/live/manifest",
			"LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT":           "/var/folders/live/artifact",
			"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL":            "http://127.0.0.1:3200",
			"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL":             "http://127.0.0.1:3100",
			"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL":       "http://127.0.0.1:9090",
			"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL":         "http://127.0.0.1:3001",
			"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL":       "langfuse-read-credential",
			"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH":             "/var/folders/live/evidence",
			"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH":           "/var/folders/live/projection",
			"LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY":    "otlphttp/loki",
			"LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION":    "live-export-correlation",
		}
		return refs
	}

	for scenario, refs := range scenarios {
		t.Run(scenario+"_complete_references_resolve", func(t *testing.T) {
			for _, key := range refs {
				t.Setenv(key, values(scenario)[key])
			}
			config, err := resolveDefaultLiveScenarioConfig(context.Background(), scenario)
			if err != nil {
				t.Fatalf("resolveDefaultLiveScenarioConfig(%q) error = %v", scenario, err)
			}
			if config.Scenario != scenario || config.Profile != "grafana" {
				t.Fatalf("config = %#v, want fixed grafana profile", config)
			}
			if config.Deadline.IsZero() || !config.Deadline.After(time.Now()) || config.Deadline.Sub(time.Now()) > time.Minute {
				t.Fatalf("config deadline = %s, want bounded future deadline", config.Deadline)
			}
		})
		for _, missing := range refs {
			t.Run(scenario+"_missing_"+missing, func(t *testing.T) {
				for _, key := range refs {
					t.Setenv(key, values(scenario)[key])
				}
				t.Setenv(missing, "")
				if _, err := resolveDefaultLiveScenarioConfig(context.Background(), scenario); err == nil {
					t.Fatalf("resolveDefaultLiveScenarioConfig(%q) error = nil, want missing %s to fail preflight", scenario, missing)
				}
			})
		}
	}

	t.Run("remote endpoint is rejected before transport", func(t *testing.T) {
		for _, key := range chatRefs {
			t.Setenv(key, values("chat")[key])
		}
		t.Setenv("LONGTERMISM_SMOKE_APP_BASE_URL", "https://example.invalid")
		if _, err := resolveDefaultLiveScenarioConfig(context.Background(), "chat"); err == nil {
			t.Fatal("remote application endpoint passed the live preflight")
		}
	})

	t.Run("malformed collector config digest is rejected", func(t *testing.T) {
		for _, key := range privacyRefs {
			t.Setenv(key, values("privacy")[key])
		}
		t.Setenv("LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST", "not-a-digest")
		if _, err := resolveDefaultLiveScenarioConfig(context.Background(), "privacy"); err == nil {
			t.Fatal("malformed collector config digest passed the privacy preflight")
		}
	})
}

// 默认装配的 WriteReport 必须把报告安全持久化到受控目录（0600、相对可信路径、
// 文件名携带 scenario），并保持 summary 输出之前先落盘。
func TestDefaultLiveScenarioDependenciesPersistContainedReports(t *testing.T) {
	dependencies := defaultLiveScenarioCommandDependencies()
	if dependencies.ResolveConfig == nil || dependencies.NewRunner == nil || dependencies.WriteReport == nil {
		t.Fatal("default live scenario dependencies must provide every composition port")
	}

	workspace := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	path, err := dependencies.WriteReport(infrastructureSmokeReportDirectory, newLiveScenarioTestReport(t, "chat", "passed"))
	if err != nil {
		t.Fatalf("default live report writer error = %v", err)
	}
	if !isTrustedInfrastructureReportPath(infrastructureSmokeReportDirectory, path) {
		t.Fatalf("default live report path = %q, want contained relative artifact", path)
	}
	if !strings.Contains(filepath.Base(path), "chat") {
		t.Fatalf("default live report file = %q, want the scenario name in the artifact", path)
	}
	info, err := os.Stat(filepath.Join(workspace, path))
	if err != nil {
		t.Fatalf("default live report artifact stat error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("default live report mode = %04o, want 0600", got)
	}
}

// 默认 live 装配在构造阶段不得发起任何网络 I/O：store 打开在临时目录、client 只做
// loopback 校验，缺任何 reference 都在 transport 前失败。
func TestDefaultLiveScenarioAssemblyConstructsWithoutNetwork(t *testing.T) {
	config := liveScenarioConfig{Scenario: liveChatScenario, Profile: "grafana", Deadline: time.Now().UTC().Add(50 * time.Second)}

	setLiveAssemblyEnvironment(t, map[string]string{
		"LONGTERMISM_SMOKE_APP_BASE_URL":                    "http://127.0.0.1:8000",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION":              "chat-live-shared-credential",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT":              filepath.Join(t.TempDir(), "manifests"),
		"LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT":           filepath.Join(t.TempDir(), "artifacts"),
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL":            "http://127.0.0.1:3200",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL":             "http://127.0.0.1:3100",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL":       "http://127.0.0.1:9090",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL":         "http://127.0.0.1:3001",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL":       "langfuse-read-credential",
		"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH":             filepath.Join(t.TempDir(), "evidence"),
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH":           filepath.Join(t.TempDir(), "projection"),
		"LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY":    "otlphttp/loki",
		"LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION":    "live-export-correlation",
	})

	tests := []struct {
		name     string
		scenario string
	}{
		{name: "chat composition constructs clients without network IO", scenario: liveChatScenario},
		{name: "score composition opens local stores without network IO", scenario: liveScoreScenario},
		{name: "privacy composition constructs the full concrete graph", scenario: livePrivacyScenario},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.Scenario = tt.scenario
			runner, err := newDefaultLiveScenarioRunner(config)
			if err != nil || runner == nil {
				t.Fatalf("newDefaultLiveScenarioRunner(%q) runner=%v error=%v, want concrete assembly without network", tt.scenario, runner, err)
			}
			if closer, ok := runner.(interface{ Close() error }); ok {
				if err := closer.Close(); err != nil {
					t.Fatalf("live runner Close() error = %v", err)
				}
			}
		})
	}

	t.Run("unsafe langfuse endpoint fails before transport", func(t *testing.T) {
		config.Scenario = liveScoreScenario
		setLiveAssemblyEnvironment(t, map[string]string{
			"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL":   "https://example.invalid:3001",
			"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL": "langfuse-read-credential",
			"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH":       filepath.Join(t.TempDir(), "evidence"),
			"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH":     filepath.Join(t.TempDir(), "projection"),
		})
		if runner, err := newDefaultLiveScenarioRunner(config); err == nil || runner != nil {
			t.Fatalf("unsafe langfuse endpoint produced runner=%v error=%v, want preflight rejection", runner, err)
		}
	})
}

func setLiveAssemblyEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	keys := []string{
		"LONGTERMISM_SMOKE_APP_BASE_URL",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT",
		"LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL",
		"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH",
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH",
		"LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST",
		"LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY",
		"LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION",
	}
	for _, key := range keys {
		if value, ok := values[key]; ok {
			t.Setenv(key, value)
		} else {
			t.Setenv(key, "")
		}
	}
}

// 受保护 chat trigger 是 live chat 场景的身份交接边界：loopback POST + 共享 auth +
// runner marker；request/AI 身份来自响应 envelope，native 身份只来自受控 manifest。
func TestProtectedLiveChatTriggerHandsOffIdentityThroughManifest(t *testing.T) {
	marker := "live-marker-t108"
	authorization := "live-chat-shared-credential"
	manifestRoot := filepath.Join(t.TempDir(), "manifests")
	store, err := smoke.OpenChatRunManifestStore(manifestRoot)
	if err != nil {
		t.Fatalf("OpenChatRunManifestStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var writes int
	var writesMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/chat" || request.Header.Get(v1chat.ChatSmokeRunIDHeader) != marker || request.Header.Get(v1chat.ChatSmokeAuthorizationHeader) != authorization {
			t.Errorf("chat request = path:%q marker:%q auth-set:%t", request.URL.Path, request.Header.Get(v1chat.ChatSmokeRunIDHeader), request.Header.Get(v1chat.ChatSmokeAuthorizationHeader) != "")
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		// 应用侧 admission 一次性消费 marker：replay 请求在 manifest 之前被拒绝。
		writesMu.Lock()
		writes++
		replay := writes > 1
		writesMu.Unlock()
		if replay {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if err := store.Write(request.Context(), smoke.ChatRunManifestInput{
			SmokeRunID: marker, RequestID: "req-t108-live", AITraceID: "ai-t108-live",
			ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef",
		}); err != nil {
			t.Errorf("manifest write error = %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(writer, `{"code":0,"message":"OK","data":{"content":"ok","model":"server-model","finish_reason":"stop","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}},"meta":{"request_id":"req-t108-live","ai_trace_id":"ai-t108-live"}}`)
	}))
	defer server.Close()

	trigger, err := newProtectedLiveChatTrigger(server.URL, authorization, nil, store)
	if err != nil {
		t.Fatalf("newProtectedLiveChatTrigger() error = %v", err)
	}
	result, err := trigger(context.Background(), smoke.ChatSmokeIdentity{RunID: "live-run-t108", Marker: marker})
	if err != nil {
		t.Fatalf("trigger() error = %v", err)
	}
	want := smoke.ChatSmokeAPIResult{RequestID: "req-t108-live", AITraceID: "ai-t108-live", ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"}
	if result != want {
		t.Fatalf("trigger result = %#v, want %#v", result, want)
	}

	if _, err := trigger(context.Background(), smoke.ChatSmokeIdentity{RunID: "live-run-t108", Marker: marker}); err == nil {
		t.Fatal("replayed marker trigger error = nil, want one-time manifest consumption to fail closed")
	}
}

// score 场景的证据必须同时存在于本地 projection 与 eval evidence：平台结果绝不能
// 事后制造事实。
func TestLiveScoreEvidenceStoreRequiresLocalFacts(t *testing.T) {
	projectionPath := filepath.Join(t.TempDir(), "projection")
	evidencePath := filepath.Join(t.TempDir(), "evidence")
	projections, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: projectionPath})
	if err != nil {
		t.Fatalf("OpenScoreProjectionStore() error = %v", err)
	}
	t.Cleanup(func() { _ = projections.Close() })
	evidence, err := localeval.OpenLocalEvidenceStore(localeval.LocalEvidenceStoreConfig{Path: evidencePath})
	if err != nil {
		t.Fatalf("OpenLocalEvidenceStore() error = %v", err)
	}
	t.Cleanup(func() { _ = evidence.Close() })

	projection := newLiveTestProjection(t, "eval-run-t108", "req-t108", "ai-t108")
	if err := projections.SaveInitial(context.Background(), "live-run-t108", projection, 2); err != nil {
		t.Fatalf("SaveInitial() error = %v", err)
	}

	store := &liveScoreEvidenceStore{projections: projections, evidence: evidence}
	if _, err := store.Find(context.Background(), "live-run-t108"); err == nil {
		t.Fatal("Find() error = nil, want missing local eval evidence to fail before platform query")
	}

	threshold := 0.8
	builtEvidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: obs.NewCorrelationIdentity("req-t108", obs.WithServiceSpan("0123456789abcdef0123456789abcdef", "0123456789abcdef"), obs.WithAITraceID("ai-t108"), obs.WithEvalRunID("eval-run-t108")),
		Dataset:  aieval.DatasetIdentity{Name: "chat-golden", Version: "v1"}, SampleID: "sample-live", MetricName: "answer_relevance", Score: 0.91, Threshold: &threshold,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.Append(context.Background(), builtEvidence); err != nil {
		t.Fatalf("evidence Append() error = %v", err)
	}
	records, err := store.Find(context.Background(), "live-run-t108")
	if err != nil || len(records) != 1 {
		t.Fatalf("Find() = (%#v, %v), want one local evidence projection", records, err)
	}
	if records[0].EvalRunID != "eval-run-t108" || records[0].ProjectionID == "" || records[0].PlatformTraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("Find() = %#v, want the persisted local facts projected verbatim", records[0])
	}
}

func TestLiveScoreIdentityResolvesTheLatestPendingProjection(t *testing.T) {
	projections, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: filepath.Join(t.TempDir(), "projection")})
	if err != nil {
		t.Fatalf("OpenScoreProjectionStore() error = %v", err)
	}
	t.Cleanup(func() { _ = projections.Close() })
	factory := newLiveScoreIdentity(projections)
	if _, err := factory(context.Background()); err == nil {
		t.Fatal("identity factory error = nil, want no-pending-run rejection")
	}
	for _, runID := range []string{"live-run-old", "live-run-new"} {
		if err := projections.SaveInitial(context.Background(), runID, newLiveTestProjection(t, "eval-"+runID, "req-"+runID, "ai-"+runID), 2); err != nil {
			t.Fatalf("SaveInitial(%q) error = %v", runID, err)
		}
	}
	identity, err := factory(context.Background())
	if err != nil || identity.RunID != "live-run-new" || identity.Marker != identity.RunID {
		t.Fatalf("identity = (%#v, %v), want the latest pending run as runner-owned identity", identity, err)
	}
}

func TestContainedScenarioReportWriterRejectsHostilePrefixes(t *testing.T) {
	for _, prefix := range []string{"", " chat ", "../escape", "a/b", `a\b`} {
		if writer, err := newContainedScenarioReportWriter(".", prefix); err == nil {
			t.Fatalf("newContainedScenarioReportWriter(%q) error = nil, want hostile prefix rejection", prefix)
			_ = writer
		}
	}
	writer, err := newContainedScenarioReportWriter(".", "chat")
	if err != nil || writer == nil {
		t.Fatalf("newContainedScenarioReportWriter(chat) = (%v, %v), want a sealed scenario prefix", writer, err)
	}
}

// newLiveTestProjection 通过生产链构造合法 projection（evidence → mapper → target →
// projection），保证测试数据与真实装配同源。
func newLiveTestProjection(t *testing.T, evalRunID, requestID, aiTraceID string) langfuse.ScoreProjection {
	t.Helper()
	const traceID, spanID = "0123456789abcdef0123456789abcdef", "0123456789abcdef"
	threshold := 0.8
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: obs.NewCorrelationIdentity(requestID, obs.WithServiceSpan(traceID, spanID), obs.WithAITraceID(aiTraceID), obs.WithEvalRunID(evalRunID)),
		Dataset:  aieval.DatasetIdentity{Name: "chat-golden", Version: "v1"}, SampleID: "sample-live", MetricName: "answer_relevance", Score: 0.91, Threshold: &threshold,
	})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := langfuse.MapTraceToProjection(langfuse.TraceMapperInput{
		Span:        langfuse.OTLPSpanSnapshot{TraceID: traceID, SpanID: spanID, Name: "ai.generation", ObservationType: obs.ObservationTypeGeneration},
		PayloadMode: obs.PayloadModeMetadataOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := langfuse.NewScoreTarget(trace, langfuse.ScoreTargetKindObservation)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := langfuse.NewScoreProjection(langfuse.ScoreProjectionInput{Target: target, Evidence: evidence, MaxAttempts: 2, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func TestLivePrivacyIdentityIsRunnerOwned(t *testing.T) {
	runID, marker, canary, err := newLivePrivacyIdentity()
	if err != nil || runID == "" || marker == "" || canary == "" {
		t.Fatalf("newLivePrivacyIdentity() = (%q,%q,%q,%v), want three distinct runner-owned values", runID, marker, canary, err)
	}
	if runID == marker || marker == canary || runID == canary {
		t.Fatalf("identity values must be distinct: %q %q %q", runID, marker, canary)
	}
}

// ---------------------------------------------------------------------------
// T199：infra AI-negative 事实源 CLI 契约。
//
// 默认装配里的 AIPlaneSmokeQueryClient 必须消费真实、只读、受保护的
// /api/v1/observability/smoke/marker-count 事实源：每个 Query 恰好一次有界 GET，
// 只携带精确 marker+window 与最小权限 credential，结果只来自服务端的有界 count，
// 任何 hostile/禁用/未认证响应都不得被改写成 0。
// ---------------------------------------------------------------------------

const aiPlaneMarkerCountCLICredential = "cli-ai-plane-credential"

type aiPlaneMarkerCountTestServer struct {
	t     *testing.T
	mu    sync.Mutex
	calls int
	path  string
	query url.Values
	auth  string
	mode  string
}

func newAIPlaneMarkerCountTestServer(t *testing.T, mode string) (*aiPlaneMarkerCountTestServer, *httptest.Server) {
	t.Helper()
	fixture := &aiPlaneMarkerCountTestServer{t: t, mode: mode}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		fixture.calls++
		fixture.path = request.URL.Path
		fixture.query = request.URL.Query()
		fixture.auth = request.Header.Get("Authorization")
		fixture.mu.Unlock()
		switch mode {
		case "count-two":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":0,"message":"OK","data":{"count":2},"meta":{"request_id":"req-server"}}`))
		case "count-zero":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":0,"message":"OK","data":{"count":0},"meta":{"request_id":"req-server"}}`))
		case "unauthorized":
			writer.WriteHeader(http.StatusUnauthorized)
		case "unavailable":
			writer.WriteHeader(http.StatusServiceUnavailable)
		case "not-json":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`raw fact source output`))
		case "missing-count":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"code":0,"message":"OK","data":{}}`))
		case "negative-count":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"code":0,"message":"OK","data":{"count":-1}}`))
		default:
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	return fixture, server
}

func newInfraCommandConfigWithAIPlane(aiPlaneURL string) infraCommandConfig {
	return infraCommandConfig{
		Profile:         "grafana",
		Deadline:        time.Now().UTC().Add(infrastructureSmokeTimeout),
		ReportDirectory: infrastructureSmokeReportDirectory,
		ApplicationURL:  "http://127.0.0.1:8000",
		PrometheusURL:   "http://127.0.0.1:9090",
		LokiURL:         "http://127.0.0.1:3100",
		TempoURL:        "http://127.0.0.1:3200",
		LangfuseURL:     "http://127.0.0.1:3001",
		LangfuseAuth:    "test-langfuse-credential",
		AIPlaneURL:      aiPlaneURL,
		AIPlaneAuth:     aiPlaneMarkerCountCLICredential,
	}
}

func (fixture *aiPlaneMarkerCountTestServer) snapshot() (int, string, url.Values, string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.calls, fixture.path, fixture.query, fixture.auth
}

// 组装后的默认 backend 必须向真实 marker-count 端点发出一次精确的有界查询：
// 固定路径、只有 marker/started_at/deadline 三个参数、最小权限 credential，
// 且返回 count 完全来自服务端，绝不本地固定。
func TestDefaultAssemblyAIPlaneClientQueriesTheProtectedMarkerCountEndpoint(t *testing.T) {
	fixture, server := newAIPlaneMarkerCountTestServer(t, "count-two")
	runner, err := newDefaultInfrastructureCommandRunner(newInfraCommandConfigWithAIPlane(server.URL))
	if err != nil {
		t.Fatalf("newDefaultInfrastructureCommandRunner() error = %v", err)
	}
	assembled, ok := runner.(*defaultInfrastructureCommandRunner)
	if !ok || assembled.backend == nil {
		t.Fatalf("default runner backend = %T, want the assembled infrastructure backend", runner)
	}

	now := time.Now()
	target := smoke.PollMarkerTarget{
		Marker: "run-t199-cli", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Second),
	}
	count, err := assembled.backend.QueryAIPlane(context.Background(), target)
	if err != nil || count != 2 {
		t.Fatalf("QueryAIPlane() = (%d, %v), want the served count 2", count, err)
	}
	calls, path, query, auth := fixture.snapshot()
	if calls != 1 || path != "/api/v1/observability/smoke/marker-count" {
		t.Fatalf("marker-count request = calls:%d path:%q, want one exact endpoint query", calls, path)
	}
	if auth != "Basic "+aiPlaneMarkerCountCLICredential {
		t.Fatalf("marker-count credential = %q, want the minimal read credential", auth)
	}
	if len(query) != 3 || query.Get("marker") != target.Marker || query.Get("started_at") != target.StartedAt.UTC().Format(time.RFC3339Nano) || query.Get("deadline") != target.Deadline.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("marker-count query = %v, want exactly marker+started_at+deadline", query)
	}
}

// 真实负向证据只有一次成功的有界查询才能给出 0；禁用/未认证/不可用/畸形响应
// 都必须以错误浮出，绝不能被组装层改写成 0 或 skipped。
func TestDefaultAssemblyAIPlaneClientNeverFabricatesZeroFromHostileServers(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "real zero is only valid evidence", mode: "count-zero"},
		{name: "unauthenticated endpoint is an error", mode: "unauthorized"},
		{name: "unavailable endpoint is an error", mode: "unavailable"},
		{name: "non JSON response is an error", mode: "not-json"},
		{name: "missing count is an error", mode: "missing-count"},
		{name: "negative count is an error", mode: "negative-count"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, server := newAIPlaneMarkerCountTestServer(t, tt.mode)
			runner, err := newDefaultInfrastructureCommandRunner(newInfraCommandConfigWithAIPlane(server.URL))
			if err != nil {
				t.Fatalf("newDefaultInfrastructureCommandRunner() error = %v", err)
			}
			assembled := runner.(*defaultInfrastructureCommandRunner)
			now := time.Now()
			target := smoke.PollMarkerTarget{Marker: "run-t199-hostile", StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Second)}
			count, queryErr := assembled.backend.QueryAIPlane(context.Background(), target)
			if tt.mode == "count-zero" {
				if queryErr != nil || count != 0 {
					t.Fatalf("QueryAIPlane() = (%d, %v), want the served bounded zero", count, queryErr)
				}
				return
			}
			if queryErr == nil || count != 0 {
				t.Fatalf("QueryAIPlane() = (%d, %v), want a stable error with no fabricated count", count, queryErr)
			}
		})
	}
}

// 远程或畸形的 AI-plane 端点必须在任何网络 I/O 之前失败：装配层先校验
// loopback-only 边界，禁用一个可以悄悄外连的诊断客户端。
func TestDefaultAssemblyRejectsUnsafeAIPlaneEndpointBeforeNetwork(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "remote endpoint", url: "https://example.invalid:8443"},
		{name: "endpoint without port", url: "https://127.0.0.1"},
		{name: "endpoint with userinfo", url: "http://user:secret@127.0.0.1:8000"},
		{name: "endpoint with path override", url: "http://127.0.0.1:8000/private"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := newDefaultInfrastructureCommandRunner(newInfraCommandConfigWithAIPlane(tt.url))
			if err == nil || runner != nil {
				t.Fatalf("newDefaultInfrastructureCommandRunner() runner=%v error=%v, want preflight rejection", runner, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T130：resilience 场景 CLI 契约（exporter-failure / persistent-queue /
// score-worker-failure / resilience full aggregate）。
//
// 这些场景会暂停/重启真实 compose 服务，属于破坏性操作：必须显式 --live
// opt-in、runner-owned 唯一 marker、任何退出路径都保留 schema report 与
// cleanup 证据，退出码与 live 场景同一约定（passed=0、failed/skipped=1、
// usage/config=2）。本组测试先用 fake 依赖钉死编排语义，默认装配的预检与
// fail-fast 行为单独覆盖。
// ---------------------------------------------------------------------------

var resilienceScenarioTestDeadline = time.Now().UTC().Add(5 * time.Minute)

// newResilienceTestReport 构造与 runner 输出同构的低敏报告：scenario 使用
// report 层密封的下划线形式，check backend 取该场景的主证据后端。
func newResilienceTestReport(t *testing.T, reportScenario, status, marker string) *smoke.SmokeReport {
	t.Helper()
	checkStatus, failureStage, errorClass := "passed", "none", ""
	if status == "failed" {
		checkStatus, failureStage, errorClass = "failed", "query", "unexpected_evidence"
	}
	if status == "skipped" {
		checkStatus = "skipped"
	}
	backendByScenario := map[string]string{
		"exporter_failure":     "collector",
		"persistent_queue":     "collector",
		"score_worker_failure": "langfuse_score",
		"full":                 "collector",
	}
	rebuilt, err := smoke.BuildSmokeReport(smoke.SmokeReportInput{
		RunID:      "run-" + marker,
		Marker:     marker,
		Profile:    "grafana",
		Scenario:   reportScenario,
		StartedAt:  resilienceScenarioTestDeadline.Add(-2 * time.Second),
		Deadline:   resilienceScenarioTestDeadline,
		FinishedAt: resilienceScenarioTestDeadline.Add(-1 * time.Second),
		Checks:     []smoke.BackendCheckInput{{Backend: backendByScenario[reportScenario], Status: checkStatus, FailureStage: failureStage, ErrorClass: errorClass}},
		Cleanup:    smoke.SmokeCleanupInput{Status: "completed", TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
	if err != nil {
		t.Fatalf("BuildSmokeReport() error = %v", err)
	}
	return rebuilt
}

func TestRunResilienceScenarioCommandContract(t *testing.T) {
	passedExporter := newResilienceTestReport(t, "exporter_failure", "passed", "marker-exporter-passed")
	passedQueue := newResilienceTestReport(t, "persistent_queue", "passed", "marker-queue-passed")
	failedQueue := newResilienceTestReport(t, "persistent_queue", "failed", "marker-queue-failed")
	skippedScore := newResilienceTestReport(t, "score_worker_failure", "skipped", "marker-score-skipped")
	passedFull := newResilienceTestReport(t, "full", "passed", "marker-full-passed")

	tests := []struct {
		name           string
		scenario       string
		args           []string
		resolveErr     error
		resolveEcho    func() resilienceScenarioConfig
		newRunnerErr   error
		runnerErr      error
		runnerResult   *smoke.SmokeReport
		writerPath     string
		writerErr      error
		wantExitCode   int
		wantConfigCall int
		wantNewCall    int
		wantRunnerCall int
		wantWriteCall  int
		wantStdoutJSON bool
		wantStatus     string
		forbidden      []string
	}{
		{
			name:     "passed exporter failure run persists the report before the summary",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "grafana", "-target", "tempo"},
			runnerResult: passedExporter, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 0, wantStdoutJSON: true, wantStatus: "passed",
			forbidden: []string{"marker-exporter-passed", liveScenarioTestCredential},
		},
		{
			name:     "failed persistent queue report is persisted before the verification failure",
			scenario: "persistent-queue", args: []string{"--live", "-profile", "grafana"},
			runnerResult: failedQueue, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 1, wantStdoutJSON: true, wantStatus: "failed",
			forbidden: []string{"marker-queue-failed"},
		},
		{
			name:     "skipped score worker report is never treated as success",
			scenario: "score-worker-failure", args: []string{"--live", "-profile", "grafana", "-case", "shutdown"},
			runnerResult: skippedScore, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 1, wantStdoutJSON: true, wantStatus: "skipped",
			forbidden: []string{"marker-score-skipped"},
		},
		{
			name:     "passed full resilience aggregate persists the report before the summary",
			scenario: "resilience", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedFull, wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1,
			wantWriteCall: 1, wantExitCode: 0, wantStdoutJSON: true, wantStatus: "passed",
			forbidden: []string{"marker-full-passed"},
		},
		{
			name:     "runner operational error has no report and no sensitive stdout",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "grafana", "-target", "loki"},
			runnerErr:      errors.New("docker compose pause leaked Authorization: Bearer live-secret"),
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantExitCode: 1,
			forbidden: []string{"live-secret", "Authorization", "docker compose"},
		},
		{
			name:     "nil report without error is an operational failure",
			scenario: "persistent-queue", args: []string{"--live", "-profile", "grafana"},
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantExitCode: 1,
			forbidden: []string{"marker-queue-failed"},
		},
		{
			name:     "missing live opt-in exits before any composition call",
			scenario: "exporter-failure", args: []string{"-profile", "grafana", "-target", "tempo"},
			runnerResult: passedExporter, wantExitCode: 2,
		},
		{
			name:     "non grafana profile is a usage failure",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "signoz", "-target", "tempo"},
			runnerResult: passedExporter, wantExitCode: 2,
		},
		{
			name:     "caller supplied marker flags are rejected",
			scenario: "persistent-queue", args: []string{"--live", "-profile", "grafana", "-marker", "forged-marker"},
			runnerResult: passedQueue, wantExitCode: 2,
		},
		{
			name:     "caller supplied run-id flags are rejected",
			scenario: "score-worker-failure", args: []string{"--live", "-profile", "grafana", "-case", "queue-full", "-run-id", "forged-run"},
			runnerResult: skippedScore, wantExitCode: 2,
		},
		{
			name:     "unknown scenario is a usage failure",
			scenario: "chaos", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedFull, wantExitCode: 2,
		},
		{
			name:     "exporter failure without target is a usage failure",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedExporter, wantExitCode: 2,
		},
		{
			name:     "exporter failure with unknown target is a usage failure",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "grafana", "-target", "grafana"},
			runnerResult: passedExporter, wantExitCode: 2,
		},
		{
			name:     "exporter failure with score case flag is a usage failure",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "grafana", "-target", "tempo", "-case", "shutdown"},
			runnerResult: passedExporter, wantExitCode: 2,
		},
		{
			name:     "score worker failure without case is a usage failure",
			scenario: "score-worker-failure", args: []string{"--live", "-profile", "grafana"},
			runnerResult: skippedScore, wantExitCode: 2,
		},
		{
			name:     "score worker failure with unknown case is a usage failure",
			scenario: "score-worker-failure", args: []string{"--live", "-profile", "grafana", "-case", "restart"},
			runnerResult: skippedScore, wantExitCode: 2,
		},
		{
			name:     "persistent queue with target flag is a usage failure",
			scenario: "persistent-queue", args: []string{"--live", "-profile", "grafana", "-target", "tempo"},
			runnerResult: failedQueue, wantExitCode: 2,
		},
		{
			name:     "extra positional arguments are a usage failure",
			scenario: "persistent-queue", args: []string{"--live", "-profile", "grafana", "extra"},
			runnerResult: failedQueue, wantExitCode: 2,
		},
		{
			name:     "missing scenario configuration fails before runner construction",
			scenario: "resilience", args: []string{"--live", "-profile", "grafana"},
			resolveErr:   errMissingInfrastructureCommandConfig,
			runnerResult: passedFull, wantConfigCall: 1, wantExitCode: 2,
		},
		{
			name:     "resolved configuration must echo the requested scenario and selector",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "grafana", "-target", "tempo"},
			resolveEcho: func() resilienceScenarioConfig {
				return resilienceScenarioConfig{Scenario: "exporter-failure", Profile: "grafana", Deadline: resilienceScenarioTestDeadline, Target: "langfuse"}
			},
			runnerResult: passedExporter, wantConfigCall: 1, wantExitCode: 2,
		},
		{
			name:     "runner construction failure is a runtime failure",
			scenario: "score-worker-failure", args: []string{"--live", "-profile", "grafana", "-case", "langfuse-api"},
			newRunnerErr: errors.New("score worker composition unavailable"), wantConfigCall: 1,
			wantNewCall: 1, wantExitCode: 1,
		},
		{
			name:     "report writer failure is a runtime failure",
			scenario: "exporter-failure", args: []string{"--live", "-profile", "grafana", "-target", "langfuse"},
			runnerResult: passedExporter, writerErr: errors.New("write failed"),
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantWriteCall: 1, wantExitCode: 1,
		},
		{
			name:     "escaped report path is rejected",
			scenario: "resilience", args: []string{"--live", "-profile", "grafana"},
			runnerResult: passedFull, writerPath: filepath.Join("..", "outside.json"),
			wantConfigCall: 1, wantNewCall: 1, wantRunnerCall: 1, wantWriteCall: 1, wantExitCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			runner := &liveScenarioTestRunner{report: tt.runnerResult, err: tt.runnerErr}
			configCalls, newCalls, writeCalls := 0, 0, 0
			reportWrittenBeforeSummary := true
			dependencies := resilienceScenarioDependencies{
				ResolveConfig: func(_ context.Context, request resilienceScenarioRequest) (resilienceScenarioConfig, error) {
					configCalls++
					if tt.resolveEcho != nil {
						return tt.resolveEcho(), tt.resolveErr
					}
					return resilienceScenarioConfig{Scenario: request.Scenario, Profile: "grafana", Deadline: resilienceScenarioTestDeadline, Target: request.Target, Case: request.Case}, tt.resolveErr
				},
				NewRunner: func(resilienceScenarioConfig) (resilienceScenarioRunner, error) {
					newCalls++
					if tt.newRunnerErr != nil {
						return nil, tt.newRunnerErr
					}
					return runner, nil
				},
				WriteReport: func(directory string, report *smoke.SmokeReport) (string, error) {
					writeCalls++
					if tt.writerErr != nil {
						return "", tt.writerErr
					}
					if report == nil {
						return "", errors.New("missing report")
					}
					// 报告必须比 stdout 摘要更早安全持久化：写入时 stdout 必须仍然为空。
					if stdout.Len() != 0 {
						reportWrittenBeforeSummary = false
					}
					if tt.writerPath != "" {
						return tt.writerPath, nil
					}
					return filepath.Join("build/observability/smoke-reports", report.Scenario()+"-report.json"), nil
				},
			}

			exitCode := runResilienceScenario(context.Background(), tt.scenario, tt.args, stdout, stderr, dependencies)

			if exitCode != tt.wantExitCode {
				t.Fatalf("runResilienceScenario() exit = %d, want %d (stderr: %s)", exitCode, tt.wantExitCode, stderr.String())
			}
			if configCalls != tt.wantConfigCall || newCalls != tt.wantNewCall || runner.calls != tt.wantRunnerCall || writeCalls != tt.wantWriteCall {
				t.Fatalf("composition calls = config:%d new:%d runner:%d write:%d, want config:%d new:%d runner:%d write:%d",
					configCalls, newCalls, runner.calls, writeCalls, tt.wantConfigCall, tt.wantNewCall, tt.wantRunnerCall, tt.wantWriteCall)
			}
			if !reportWrittenBeforeSummary {
				t.Fatal("report was not persisted before the stdout summary")
			}
			if !tt.wantStdoutJSON {
				if stdout.Len() != 0 {
					t.Fatalf("stdout = %q, want strictly empty", stdout.String())
				}
				for _, forbidden := range tt.forbidden {
					if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
						t.Fatalf("output leaked %q: stdout=%q stderr=%q", forbidden, stdout.String(), stderr.String())
					}
				}
				return
			}
			var output map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("stdout is not JSON: %v", err)
			}
			if len(output) != 3 || output["scenario"] != tt.scenario || output["status"] != tt.wantStatus || output["report_path"] == "" {
				t.Fatalf("stdout summary = %v, want only scenario/status/trusted report path", output)
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(stdout.String(), forbidden) {
					t.Fatalf("stdout leaked %q: %s", forbidden, stdout.String())
				}
			}
		})
	}
}

// resilienceScenarioConfig 只允许携带低敏编排快照与注入目标选择器：
// marker/run-id 永远由 runner 生成，绝不能进入配置对象。
func TestResilienceScenarioConfigCarriesNoRunnerIdentity(t *testing.T) {
	configType := reflect.TypeOf(resilienceScenarioConfig{})
	if configType.Kind() != reflect.Struct || configType.NumField() != 5 {
		t.Fatalf("resilienceScenarioConfig = %v, want a five-field low-sensitivity orchestration snapshot", configType)
	}
	for index, want := range []struct {
		name string
		kind reflect.Kind
	}{
		{name: "Scenario", kind: reflect.String},
		{name: "Profile", kind: reflect.String},
		{name: "Deadline", kind: reflect.Struct},
		{name: "Target", kind: reflect.String},
		{name: "Case", kind: reflect.String},
	} {
		field := configType.Field(index)
		if field.Name != want.name || field.Type.Kind() != want.kind {
			t.Fatalf("resilienceScenarioConfig field %d = %s (%v), want %s", index, field.Name, field.Type, want.name)
		}
		if strings.Contains(strings.ToLower(field.Name), "marker") || strings.Contains(strings.ToLower(field.Name), "runid") {
			t.Fatalf("resilienceScenarioConfig must never carry runner-owned identity field %q", field.Name)
		}
	}
}

// 编排必须把调用方的 context 原样交给 runner：main 里的 signal trap 通过
// 取消该 context 触发 runner 的报告路径，任何中途替换 ctx 都会破坏
// "中断后仍保留 cleanup 证据" 的语义。
func TestRunResilienceScenarioForwardsCallerContext(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var received context.Context
	dependencies := resilienceScenarioDependencies{
		ResolveConfig: func(_ context.Context, request resilienceScenarioRequest) (resilienceScenarioConfig, error) {
			return resilienceScenarioConfig{Scenario: request.Scenario, Profile: "grafana", Deadline: resilienceScenarioTestDeadline}, nil
		},
		NewRunner: func(resilienceScenarioConfig) (resilienceScenarioRunner, error) {
			return runnerFunc(func(ctx context.Context) (*smoke.SmokeReport, error) {
				received = ctx
				return newResilienceTestReport(t, "persistent_queue", "failed", "marker-canceled-queue"), nil
			}), nil
		},
		WriteReport: func(string, *smoke.SmokeReport) (string, error) {
			return filepath.Join("build/observability/smoke-reports", "persistent_queue-report.json"), nil
		},
	}
	if exitCode := runResilienceScenario(canceled, "persistent-queue", []string{"--live", "-profile", "grafana"}, &bytes.Buffer{}, &bytes.Buffer{}, dependencies); exitCode != 1 {
		t.Fatalf("runResilienceScenario(canceled ctx) exit = %d, want failed-report exit 1", exitCode)
	}
	if received == nil || received.Err() == nil {
		t.Fatal("runner must observe the caller-supplied canceled context")
	}
}

// runnerFunc 把闭包适配成 resilience 场景 runner，供聚合契约测试注入脚本化行为。
type runnerFunc func(context.Context) (*smoke.SmokeReport, error)

func (f runnerFunc) Run(ctx context.Context) (*smoke.SmokeReport, error) { return f(ctx) }

// TestResilienceFullRunnerContract 钉死 full aggregate 的核心语义：
// 7 个子场景各恰好执行一次、子报告先于聚合报告持久化、任一子失败不阻断
// 后续子场景（cleanup trap）、marker 唯一性强制、运行错误与写盘失败保留
// 保守残留证据。
func TestResilienceFullRunnerContract(t *testing.T) {
	subScenarioTable := []resilienceScenarioConfig{
		{Scenario: "exporter-failure", Target: "tempo"},
		{Scenario: "exporter-failure", Target: "loki"},
		{Scenario: "exporter-failure", Target: "langfuse"},
		{Scenario: "persistent-queue"},
		{Scenario: "score-worker-failure", Case: "langfuse-api"},
		{Scenario: "score-worker-failure", Case: "queue-full"},
		{Scenario: "score-worker-failure", Case: "shutdown"},
	}

	t.Run("all sub scenarios pass and every sub report is persisted", func(t *testing.T) {
		var created []resilienceScenarioConfig
		var written []*smoke.SmokeReport
		factory := func(config resilienceScenarioConfig) (resilienceScenarioRunner, error) {
			created = append(created, config)
			reportScenario := map[string]string{
				"exporter-failure":     "exporter_failure",
				"persistent-queue":     "persistent_queue",
				"score-worker-failure": "score_worker_failure",
			}[config.Scenario]
			marker := fmt.Sprintf("marker-full-%s-%s%s", reportScenario, config.Target, config.Case)
			return runnerFunc(func(context.Context) (*smoke.SmokeReport, error) {
				return newResilienceTestReport(t, reportScenario, "passed", marker), nil
			}), nil
		}
		subWriter := func(_ string, report *smoke.SmokeReport) (string, error) {
			written = append(written, report)
			return filepath.Join("build/observability/smoke-reports", report.Scenario()+".json"), nil
		}
		runner := newResilienceFullRunner(resilienceScenarioConfig{Scenario: "resilience", Profile: "grafana", Deadline: resilienceScenarioTestDeadline}, factory, subWriter, func() time.Time { return time.Now().UTC() })
		report, err := runner.Run(context.Background())
		if err != nil || report == nil {
			t.Fatalf("full runner error = %v, report = %v", err, report)
		}
		if len(created) != 7 || len(written) != 7 {
			t.Fatalf("sub executions = %d, sub reports = %d, want 7/7", len(created), len(written))
		}
		for index, want := range subScenarioTable {
			if created[index].Scenario != want.Scenario || created[index].Target != want.Target || created[index].Case != want.Case {
				t.Fatalf("sub scenario %d = %#v, want %#v", index, created[index], want)
			}
		}
		if report.Scenario() != "full" || report.Status() != "passed" {
			t.Fatalf("aggregate = %s/%s, want full/passed", report.Scenario(), report.Status())
		}
		checks := report.Checks()
		if len(checks) != 7 {
			t.Fatalf("aggregate checks = %d, want one row per sub scenario", len(checks))
		}
		wantBackends := []string{"collector", "collector", "collector", "collector", "langfuse_score", "langfuse_score", "langfuse_score"}
		for index, want := range wantBackends {
			if checks[index].Backend != want || checks[index].Status != "passed" || checks[index].FailureStage != "none" {
				t.Fatalf("aggregate check %d = %#v, want passed %s row", index, checks[index], want)
			}
		}
		cleanup := report.Cleanup()
		if cleanup.Status != "completed" || len(cleanup.ResidualResources) != 0 {
			t.Fatalf("aggregate cleanup = %#v, want completed without residuals", cleanup)
		}
	})

	t.Run("a failed sub scenario never blocks the remaining cleanup paths", func(t *testing.T) {
		var executed int
		factory := func(config resilienceScenarioConfig) (resilienceScenarioRunner, error) {
			current := executed
			executed++
			return runnerFunc(func(context.Context) (*smoke.SmokeReport, error) {
				if current == 1 {
					return newResilienceTestReport(t, "exporter_failure", "failed", fmt.Sprintf("marker-mixed-%d", current)), nil
				}
				return newResilienceTestReport(t, "exporter_failure", "passed", fmt.Sprintf("marker-mixed-%d", current)), nil
			}), nil
		}
		runner := newResilienceFullRunner(resilienceScenarioConfig{Scenario: "resilience", Profile: "grafana", Deadline: resilienceScenarioTestDeadline}, factory, func(_ string, report *smoke.SmokeReport) (string, error) {
			return filepath.Join("build/observability/smoke-reports", report.Scenario()+".json"), nil
		}, func() time.Time { return time.Now().UTC() })
		report, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("full runner error = %v", err)
		}
		if executed != 7 {
			t.Fatalf("sub executions = %d, want 7 even after a failure", executed)
		}
		if report.Status() != "failed" {
			t.Fatalf("aggregate status = %s, want failed", report.Status())
		}
		checks := report.Checks()
		if checks[1].Status != "failed" || checks[1].FailureStage != "query" || checks[1].ErrorClass != "unexpected_evidence" {
			t.Fatalf("failed row = %#v, want the sub report failure facts projected", checks[1])
		}
		for _, index := range []int{0, 2, 3, 4, 5, 6} {
			if checks[index].Status != "passed" {
				t.Fatalf("unrelated row %d = %#v, want untouched passed evidence", index, checks[index])
			}
		}
	})

	t.Run("sub runner operational failure records conservative residual and continues", func(t *testing.T) {
		var executed int
		factory := func(resilienceScenarioConfig) (resilienceScenarioRunner, error) {
			index := executed
			executed++
			if index == 3 {
				return nil, errors.New("composition exploded")
			}
			return runnerFunc(func(context.Context) (*smoke.SmokeReport, error) {
				if index == 5 {
					return nil, errors.New("runner crashed")
				}
				return newResilienceTestReport(t, "score_worker_failure", "passed", fmt.Sprintf("marker-opfail-%d", index)), nil
			}), nil
		}
		runner := newResilienceFullRunner(resilienceScenarioConfig{Scenario: "resilience", Profile: "grafana", Deadline: resilienceScenarioTestDeadline}, factory, func(_ string, report *smoke.SmokeReport) (string, error) {
			return filepath.Join("build/observability/smoke-reports", report.Scenario()+".json"), nil
		}, func() time.Time { return time.Now().UTC() })
		report, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("full runner error = %v", err)
		}
		if executed != 7 {
			t.Fatalf("sub executions = %d, want 7 despite operational failures", executed)
		}
		checks := report.Checks()
		for _, index := range []int{3, 5} {
			if checks[index].Status != "failed" || checks[index].FailureStage != "preflight" || checks[index].ErrorClass != "invalid_configuration" {
				t.Fatalf("operational failure row %d = %#v, want conservative preflight failure", index, checks[index])
			}
		}
		cleanup := report.Cleanup()
		if cleanup.Status != "failed" {
			t.Fatalf("aggregate cleanup = %#v, want failed after operational failures", cleanup)
		}
		found := false
		for _, residual := range cleanup.ResidualResources {
			if residual == "paused-service" {
				found = true
			}
		}
		if !found {
			t.Fatalf("aggregate residuals = %#v, want conservative paused-service", cleanup.ResidualResources)
		}
	})

	t.Run("duplicate sub markers are rejected as identity mismatch", func(t *testing.T) {
		var executed int
		factory := func(resilienceScenarioConfig) (resilienceScenarioRunner, error) {
			executed++
			return runnerFunc(func(context.Context) (*smoke.SmokeReport, error) {
				return newResilienceTestReport(t, "exporter_failure", "passed", "marker-duplicated"), nil
			}), nil
		}
		runner := newResilienceFullRunner(resilienceScenarioConfig{Scenario: "resilience", Profile: "grafana", Deadline: resilienceScenarioTestDeadline}, factory, func(_ string, report *smoke.SmokeReport) (string, error) {
			return filepath.Join("build/observability/smoke-reports", report.Scenario()+".json"), nil
		}, func() time.Time { return time.Now().UTC() })
		report, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("full runner error = %v", err)
		}
		checks := report.Checks()
		if checks[0].Status != "passed" {
			t.Fatalf("first marker occurrence = %#v, want accepted", checks[0])
		}
		duplicates := 0
		for _, check := range checks {
			if check.ErrorClass == "identity_mismatch" {
				duplicates++
			}
		}
		if duplicates != 6 {
			t.Fatalf("identity_mismatch rows = %d, want the six repeated markers rejected", duplicates)
		}
		if report.Status() != "failed" {
			t.Fatalf("aggregate status = %s, want failed after identity reuse", report.Status())
		}
	})

	t.Run("sub report persistence failure keeps later scenarios running", func(t *testing.T) {
		var executed, persisted int
		factory := func(resilienceScenarioConfig) (resilienceScenarioRunner, error) {
			executed++
			return runnerFunc(func(context.Context) (*smoke.SmokeReport, error) {
				return newResilienceTestReport(t, "persistent_queue", "passed", fmt.Sprintf("marker-writefail-%d", executed)), nil
			}), nil
		}
		subWriter := func(_ string, _ *smoke.SmokeReport) (string, error) {
			persisted++
			if persisted == 1 {
				return "", errors.New("disk full")
			}
			return filepath.Join("build/observability/smoke-reports", "persistent_queue.json"), nil
		}
		runner := newResilienceFullRunner(resilienceScenarioConfig{Scenario: "resilience", Profile: "grafana", Deadline: resilienceScenarioTestDeadline}, factory, subWriter, func() time.Time { return time.Now().UTC() })
		report, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("full runner error = %v", err)
		}
		if executed != 7 || persisted != 7 {
			t.Fatalf("executed = %d persisted = %d, want 7/7 despite write failure", executed, persisted)
		}
		if report.Status() != "failed" {
			t.Fatalf("aggregate status = %s, want failed after evidence loss", report.Status())
		}
		cleanup := report.Cleanup()
		found := false
		for _, residual := range cleanup.ResidualResources {
			if residual == "temporary-debug-data" {
				found = true
			}
		}
		if !found {
			t.Fatalf("aggregate residuals = %#v, want temporary-debug-data after evidence loss", cleanup.ResidualResources)
		}
	})

	t.Run("aggregate identity is runner owned and unique per run", func(t *testing.T) {
		factory := func(resilienceScenarioConfig) (resilienceScenarioRunner, error) {
			return runnerFunc(func(context.Context) (*smoke.SmokeReport, error) {
				return newResilienceTestReport(t, "persistent_queue", "passed", "marker-identity-fixed"), nil
			}), nil
		}
		subWriter := func(_ string, _ *smoke.SmokeReport) (string, error) {
			return "build/observability/smoke-reports/x.json", nil
		}
		config := resilienceScenarioConfig{Scenario: "resilience", Profile: "grafana", Deadline: resilienceScenarioTestDeadline}
		first, err := newResilienceFullRunner(config, factory, subWriter, func() time.Time { return time.Now().UTC() }).Run(context.Background())
		if err != nil {
			t.Fatalf("first run error = %v", err)
		}
		second, err := newResilienceFullRunner(config, factory, subWriter, func() time.Time { return time.Now().UTC() }).Run(context.Background())
		if err != nil {
			t.Fatalf("second run error = %v", err)
		}
		var firstDocument, secondDocument struct {
			RunID  string `json:"run_id"`
			Marker string `json:"marker"`
		}
		if err := json.Unmarshal(mustMarshalSmokeReport(t, first), &firstDocument); err != nil {
			t.Fatalf("first report decode error = %v", err)
		}
		if err := json.Unmarshal(mustMarshalSmokeReport(t, second), &secondDocument); err != nil {
			t.Fatalf("second report decode error = %v", err)
		}
		if firstDocument.RunID == "" || firstDocument.Marker == "" || firstDocument.RunID == secondDocument.RunID || firstDocument.Marker == secondDocument.Marker {
			t.Fatalf("aggregate identity = %q/%q vs %q/%q, want non-empty unique identities", firstDocument.RunID, firstDocument.Marker, secondDocument.RunID, secondDocument.Marker)
		}
	})
}

func mustMarshalSmokeReport(t *testing.T, report *smoke.SmokeReport) []byte {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	return encoded
}

// resolveDefaultResilienceScenarioConfig 必须在任何 docker 控制、client 或
// transport 之前完成全部必填 reference 预检；env 名称是 T130 钉死的公开契约。
func TestDefaultResilienceScenarioConfigPreflight(t *testing.T) {
	commonRefs := map[string]string{
		"LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT": "ashjazz-observability",
		"LONGTERMISM_SMOKE_APP_BASE_URL":               "http://127.0.0.1:8000",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL":  "http://127.0.0.1:9090",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL":       "http://127.0.0.1:3200",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL":        "http://127.0.0.1:3100",
	}
	scoreRefs := map[string]string{
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL":   "http://127.0.0.1:3000",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL": "langfuse-read-credential",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION":        "chat-live-shared-credential",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT":        "/var/folders/resilience/manifest",
		"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH":       "/var/folders/resilience/evidence",
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH":     "/var/folders/resilience/projection",
	}
	requiredRefs := map[string][]string{
		"exporter-failure": {
			"LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT", "LONGTERMISM_SMOKE_APP_BASE_URL", "LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL",
		},
		"persistent-queue": {
			"LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT", "LONGTERMISM_SMOKE_APP_BASE_URL", "LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL", "LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL",
		},
		"score-worker-failure": {
			"LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT", "LONGTERMISM_SMOKE_APP_BASE_URL", "LONGTERMISM_SMOKE_CHAT_AUTHORIZATION", "LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT",
			"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH", "LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH", "LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL", "LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL",
		},
	}
	fullRefs := append(append([]string{}, requiredRefs["persistent-queue"]...), requiredRefs["score-worker-failure"]...)
	requiredRefs["resilience"] = fullRefs

	requests := map[string]resilienceScenarioRequest{
		"exporter-failure":     {Scenario: "exporter-failure", Target: "tempo"},
		"persistent-queue":     {Scenario: "persistent-queue"},
		"score-worker-failure": {Scenario: "score-worker-failure", Case: "langfuse-api"},
		"resilience":           {Scenario: "resilience"},
	}

	for scenario, refs := range requiredRefs {
		t.Run(scenario+"_complete_references_resolve", func(t *testing.T) {
			for key, value := range commonRefs {
				t.Setenv(key, value)
			}
			for key, value := range scoreRefs {
				t.Setenv(key, value)
			}
			config, err := resolveDefaultResilienceScenarioConfig(context.Background(), requests[scenario])
			if err != nil {
				t.Fatalf("resolveDefaultResilienceScenarioConfig(%q) error = %v", scenario, err)
			}
			if config.Scenario != scenario || config.Profile != "grafana" {
				t.Fatalf("config = %#v, want fixed grafana profile", config)
			}
			request := requests[scenario]
			if config.Target != request.Target || config.Case != request.Case {
				t.Fatalf("config selectors = %q/%q, want %q/%q", config.Target, config.Case, request.Target, request.Case)
			}
			if config.Deadline.IsZero() || !config.Deadline.After(time.Now()) {
				t.Fatalf("config deadline = %s, want bounded future deadline", config.Deadline)
			}
		})
		for _, missing := range refs {
			t.Run(scenario+"_missing_"+missing, func(t *testing.T) {
				for key, value := range commonRefs {
					t.Setenv(key, value)
				}
				for key, value := range scoreRefs {
					t.Setenv(key, value)
				}
				t.Setenv(missing, "")
				if _, err := resolveDefaultResilienceScenarioConfig(context.Background(), requests[scenario]); err == nil {
					t.Fatalf("resolveDefaultResilienceScenarioConfig(%q) error = nil, want missing %s to fail preflight", scenario, missing)
				}
			})
		}
	}

	t.Run("unknown selectors are rejected before any reference is read", func(t *testing.T) {
		for key, value := range commonRefs {
			t.Setenv(key, value)
		}
		if _, err := resolveDefaultResilienceScenarioConfig(context.Background(), resilienceScenarioRequest{Scenario: "exporter-failure", Target: "grafana"}); err == nil {
			t.Fatal("unknown exporter target passed the resilience preflight")
		}
		if _, err := resolveDefaultResilienceScenarioConfig(context.Background(), resilienceScenarioRequest{Scenario: "score-worker-failure", Case: "restart"}); err == nil {
			t.Fatal("unknown score worker case passed the resilience preflight")
		}
	})

	t.Run("unsafe compose project name is rejected", func(t *testing.T) {
		for key, value := range commonRefs {
			t.Setenv(key, value)
		}
		for key, value := range scoreRefs {
			t.Setenv(key, value)
		}
		t.Setenv("LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT", "project; rm -rf")
		if _, err := resolveDefaultResilienceScenarioConfig(context.Background(), requests["persistent-queue"]); err == nil {
			t.Fatal("shell-metacharacter compose project passed the resilience preflight")
		}
	})

	t.Run("remote endpoint is rejected before transport", func(t *testing.T) {
		for key, value := range commonRefs {
			t.Setenv(key, value)
		}
		t.Setenv("LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL", "https://example.invalid")
		if _, err := resolveDefaultResilienceScenarioConfig(context.Background(), requests["exporter-failure"]); err == nil {
			t.Fatal("remote prometheus endpoint passed the resilience preflight")
		}
	})
}

// 默认装配的 WriteReport 与 live 场景共用受控目录契约：0600、相对可信路径、
// 文件名携带 report 自身密封的 scenario。
func TestDefaultResilienceScenarioDependenciesPersistContainedReports(t *testing.T) {
	dependencies := defaultResilienceScenarioDependencies()
	if dependencies.ResolveConfig == nil || dependencies.NewRunner == nil || dependencies.WriteReport == nil {
		t.Fatal("default resilience dependencies must provide every composition port")
	}
	workspace := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	path, err := dependencies.WriteReport(infrastructureSmokeReportDirectory, newResilienceTestReport(t, "score_worker_failure", "failed", "marker-default-writer"))
	if err != nil {
		t.Fatalf("default resilience report writer error = %v", err)
	}
	if !isTrustedInfrastructureReportPath(infrastructureSmokeReportDirectory, path) {
		t.Fatalf("default resilience report path = %q, want contained relative artifact", path)
	}
	if !strings.Contains(filepath.Base(path), "score_worker_failure") {
		t.Fatalf("default resilience report file = %q, want the sealed scenario in the artifact", path)
	}
	info, err := os.Stat(filepath.Join(workspace, path))
	if err != nil {
		t.Fatalf("default resilience report artifact stat error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("default resilience report mode = %04o, want 0600", got)
	}
}

// T130 能力收敛后的默认装配契约：
// - exporter-failure（3 目标）与 persistent-queue 已具备真实 live composition；
// - score-worker-failure 的 langfuse-api case 已收敛（pause/unpause langfuse-web +
//   本地 store 状态 + Langfuse 平台 score 计数 + warm-up 动态身份解析）；queue-full
//   与 shutdown case 仍以稳定能力哨兵 fail-fast（进程内通道无受控实现），禁止伪造证据；
// - full aggregate 的默认子工厂按固定序列构造：5 个真实子场景 + 2 个
//   preflight 失败行。单测不得执行聚合或单场景 Run（会触发真实 docker/网络副作用）。
func TestDefaultResilienceScenarioRunnerConvergence(t *testing.T) {
	for key, value := range map[string]string{
		"LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT": "ashjazz-observability",
		"LONGTERMISM_SMOKE_APP_BASE_URL":               "http://127.0.0.1:8000",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL":  "http://127.0.0.1:9090",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL":       "http://127.0.0.1:3200",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL":        "http://127.0.0.1:3100",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL":    "http://127.0.0.1:3000",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL":  "langfuse-read-credential",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION":         "chat-live-shared-credential",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT":         filepath.Join(t.TempDir(), "manifests"),
		"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH":        filepath.Join(t.TempDir(), "evidence"),
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH":      filepath.Join(t.TempDir(), "projection"),
	} {
		t.Setenv(key, value)
	}
	t.Run("queue-full and shutdown still fail fast with the stable capability sentinel", func(t *testing.T) {
		for _, scenarioCase := range []string{"queue-full", "shutdown"} {
			config, err := resolveDefaultResilienceScenarioConfig(context.Background(), resilienceScenarioRequest{Scenario: "score-worker-failure", Case: scenarioCase})
			if err != nil {
				t.Fatalf("preflight error = %v, want complete references to resolve", err)
			}
			runner, err := newDefaultResilienceScenarioRunner(config)
			if err == nil || runner != nil {
				t.Fatalf("newDefaultResilienceScenarioRunner(%q) runner=%v error=%v, want fail-fast before side effects", scenarioCase, runner, err)
			}
			if !errors.Is(err, errResilienceCapabilityUnavailable) {
				t.Fatalf("fail-fast error = %v, want the stable capability sentinel", err)
			}
		}
	})
	t.Run("real scenarios construct runners without side effects", func(t *testing.T) {
		for scenario, request := range map[string]resilienceScenarioRequest{
			"exporter-failure":     {Scenario: "exporter-failure", Target: "tempo"},
			"persistent-queue":     {Scenario: "persistent-queue"},
			"score-worker-failure": {Scenario: "score-worker-failure", Case: "langfuse-api"},
		} {
			t.Run(scenario, func(t *testing.T) {
				config, err := resolveDefaultResilienceScenarioConfig(context.Background(), request)
				if err != nil {
					t.Fatalf("preflight error = %v, want complete references to resolve", err)
				}
				runner, err := newDefaultResilienceScenarioRunner(config)
				if err != nil || runner == nil {
					t.Fatalf("newDefaultResilienceScenarioRunner(%q) error=%v, want the real live composition", scenario, err)
				}
			})
		}
	})
	t.Run("aggregate sub factory yields five real runners and two preflight gaps", func(t *testing.T) {
		parent := resilienceScenarioConfig{Scenario: "resilience", Profile: "grafana", Deadline: time.Now().Add(resilienceFullScenarioTimeout)}
		subs := resilienceFullSubScenarios(parent)
		if len(subs) != 7 {
			t.Fatalf("sub scenarios = %d, want 7", len(subs))
		}
		real, gated := 0, 0
		for _, sub := range subs {
			runner, err := newDefaultResilienceScenarioRunner(sub)
			switch {
			case err == nil && runner != nil:
				real++
			case errors.Is(err, errResilienceCapabilityUnavailable):
				gated++
			default:
				t.Fatalf("sub %q/%q unexpected error = %v", sub.Scenario, sub.Case, err)
			}
		}
		if real != 5 || gated != 2 {
			t.Fatalf("composition = %d real / %d gated, want 5/2（queue-full 与 shutdown 仍受限）", real, gated)
		}
	})
}
