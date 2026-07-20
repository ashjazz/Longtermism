package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
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
