package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	"github.com/ashjazz/Longtermism/internal/observability/backend"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const (
	infrastructureSmokeTimeout         = time.Minute
	infrastructureSmokeReportDirectory = "build/observability/smoke-reports"
)

var errMissingInfrastructureCommandConfig = errors.New("missing infrastructure smoke configuration")

type infraCommandConfig struct {
	Profile         string
	Deadline        time.Time
	ReportDirectory string

	ApplicationURL string
	PrometheusURL  string
	LokiURL        string
	TempoURL       string
	LangfuseURL    string
	LangfuseAuth   string
	AIPlaneURL     string
	AIPlaneAuth    string
}

type infraCommandRunner interface {
	Run(context.Context, smoke.InfrastructureSmokeRequest) (*smoke.SmokeReport, error)
}

type infraCommandDependencies struct {
	ResolveConfig func(context.Context) (infraCommandConfig, error)
	NewRunner     func(infraCommandConfig) (infraCommandRunner, error)
	WriteReport   func(string, *smoke.SmokeReport) (string, error)
}

type infraCommandOutput struct {
	Status     string `json:"status"`
	ReportPath string `json:"report_path"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "infra" {
		os.Exit(2)
	}
	exitCode := runInfra(context.Background(), os.Args[2:], os.Stdout, os.Stderr, defaultInfraCommandDependencies())
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runInfra is deliberately only orchestration: the smoke runner owns identity, ordering and
// report semantics; this command validates config, invokes it once, persists the resulting report
// under the ignored root, and prints a two-field low-sensitivity summary.
func runInfra(ctx context.Context, args []string, stdout, stderr io.Writer, dependencies infraCommandDependencies) int {
	if containsForbiddenIdentityFlag(args) || dependencies.ResolveConfig == nil || dependencies.NewRunner == nil || dependencies.WriteReport == nil {
		return 2
	}
	flags := flag.NewFlagSet("infra", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "grafana", "infra smoke profile")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *profile != "grafana" {
		return 2
	}
	config, err := dependencies.ResolveConfig(ctx)
	if err != nil || config.Profile != *profile || config.Deadline.IsZero() || config.ReportDirectory != infrastructureSmokeReportDirectory {
		return 2
	}
	runner, err := dependencies.NewRunner(config)
	if err != nil || runner == nil {
		return 1
	}
	report, err := runner.Run(ctx, smoke.InfrastructureSmokeRequest{Profile: config.Profile, Deadline: config.Deadline})
	if err != nil || report == nil {
		return 1
	}
	path, err := dependencies.WriteReport(config.ReportDirectory, report)
	if err != nil || !isTrustedInfrastructureReportPath(config.ReportDirectory, path) {
		return 1
	}
	status := report.Status()
	if err := json.NewEncoder(stdout).Encode(infraCommandOutput{Status: status, ReportPath: path}); err != nil {
		return 1
	}
	if status == "passed" {
		return 0
	}
	return 1
}

func containsForbiddenIdentityFlag(args []string) bool {
	for _, argument := range args {
		if argument == "-marker" || argument == "--marker" || strings.HasPrefix(argument, "-marker=") || strings.HasPrefix(argument, "--marker=") || argument == "-run-id" || argument == "--run-id" || strings.HasPrefix(argument, "-run-id=") || strings.HasPrefix(argument, "--run-id=") {
			return true
		}
	}
	return false
}

func isTrustedInfrastructureReportPath(directory, path string) bool {
	if filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	relative, err := filepath.Rel(directory, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func defaultInfraCommandDependencies() infraCommandDependencies {
	writer, err := newContainedInfrastructureReportWriter(".")
	return infraCommandDependencies{
		ResolveConfig: resolveDefaultInfrastructureCommandConfig,
		NewRunner:     newDefaultInfrastructureCommandRunner,
		WriteReport: func(directory string, report *smoke.SmokeReport) (string, error) {
			if err != nil || directory != infrastructureSmokeReportDirectory {
				return "", errMissingInfrastructureCommandConfig
			}
			path, writeErr := writer.Write(report)
			if writeErr != nil {
				return "", writeErr
			}
			relative, relativeErr := filepath.Rel(".", path)
			if relativeErr != nil {
				return "", relativeErr
			}
			return relative, nil
		},
	}
}

func resolveDefaultInfrastructureCommandConfig(context.Context) (infraCommandConfig, error) {
	lookup := os.Getenv
	config := infraCommandConfig{
		Profile:         "grafana",
		Deadline:        time.Now().UTC().Add(infrastructureSmokeTimeout),
		ReportDirectory: infrastructureSmokeReportDirectory,
		ApplicationURL:  envOrDefault(lookup, "LONGTERMISM_SMOKE_APP_BASE_URL", "http://127.0.0.1:8000"),
		PrometheusURL:   lookup("LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL"),
		LokiURL:         lookup("LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL"),
		TempoURL:        lookup("LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL"),
		LangfuseURL:     lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"),
		LangfuseAuth:    lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"),
		AIPlaneURL:      lookup("LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL"),
		AIPlaneAuth:     lookup("LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL"),
	}
	if config.PrometheusURL == "" || config.LokiURL == "" || config.TempoURL == "" || config.LangfuseURL == "" || config.LangfuseAuth == "" || config.AIPlaneURL == "" || config.AIPlaneAuth == "" {
		return infraCommandConfig{}, errMissingInfrastructureCommandConfig
	}
	for _, endpoint := range []string{config.ApplicationURL, config.PrometheusURL, config.LokiURL, config.TempoURL} {
		if err := validateLocalSmokeBaseURL(endpoint); err != nil {
			return infraCommandConfig{}, errMissingInfrastructureCommandConfig
		}
	}
	return config, nil
}

func envOrDefault(lookup func(string) string, key, fallback string) string {
	if value := lookup(key); value != "" {
		return value
	}
	return fallback
}

type defaultInfrastructureCommandRunner struct {
	backend smoke.InfrastructureSmokeBackend
	trigger smoke.InfrastructureSmokeTrigger
}

func newDefaultInfrastructureCommandRunner(config infraCommandConfig) (infraCommandRunner, error) {
	trigger, err := newProtectedInfrastructureSmokeTrigger(config.ApplicationURL, http.DefaultClient)
	if err != nil {
		return nil, err
	}
	grafana := backend.NewGrafanaQueryClient(backend.GrafanaQueryConfig{
		PrometheusURL: config.PrometheusURL,
		LokiURL:       config.LokiURL,
		TempoURL:      config.TempoURL,
		// GrafanaQueryClient is reusable outside this command, so it does not impose this
		// profile's host-local policy itself. The composition root supplies the same
		// no-proxy, re-resolving transport used by the protected trigger.
		HTTPClient: &http.Client{Transport: newLocalSmokeTransport()},
	})
	langfuse, err := backend.NewLangfuseSmokeQueryClient(backend.LangfuseSmokeQueryConfig{BaseURL: config.LangfuseURL, Credential: config.LangfuseAuth})
	if err != nil {
		return nil, err
	}
	aiPlane, err := backend.NewAIPlaneSmokeQueryClient(backend.AIPlaneSmokeQueryConfig{BaseURL: config.AIPlaneURL, Credential: config.AIPlaneAuth})
	if err != nil {
		return nil, err
	}
	smokeBackend, err := backend.NewGrafanaInfrastructureSmokeBackend(backend.GrafanaInfrastructureSmokeBackendConfig{Grafana: grafana, Langfuse: langfuse, AIPlane: aiPlane})
	if err != nil {
		return nil, err
	}
	return &defaultInfrastructureCommandRunner{backend: smokeBackend, trigger: trigger}, nil
}

func (r *defaultInfrastructureCommandRunner) Run(ctx context.Context, request smoke.InfrastructureSmokeRequest) (*smoke.SmokeReport, error) {
	return smoke.RunInfrastructureSmoke(ctx, request, smoke.InfrastructureSmokeRunnerDependencies{Backend: r.backend, Trigger: r.trigger, Clock: systemPollerClock{}, PollInterval: time.Second})
}

type systemPollerClock struct{}

func (systemPollerClock) Now() time.Time { return time.Now() }
func (systemPollerClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var errProtectedInfrastructureTrigger = errors.New("protected infrastructure trigger failed")

func newProtectedInfrastructureSmokeTrigger(baseURL string, client *http.Client) (smoke.InfrastructureSmokeTrigger, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errProtectedInfrastructureTrigger
	}
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	if client == http.DefaultClient {
		copy.Transport = newLocalSmokeTransport()
	}
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	endpoint := parsed.ResolveReference(&url.URL{Path: "/api/v1/observability/infra-smoke"})
	return func(ctx context.Context, identity smoke.InfrastructureSmokeIdentity) error {
		if identity.Marker == "" {
			return errProtectedInfrastructureTrigger
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return errProtectedInfrastructureTrigger
		}
		request.Header.Set(v1observability.SmokeRunIDHeader, identity.Marker)
		response, err := copy.Do(request)
		if err != nil {
			return errProtectedInfrastructureTrigger
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return errProtectedInfrastructureTrigger
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			return errProtectedInfrastructureTrigger
		}
		var envelope struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.Status != v1observability.InfraSmokeStatusOK {
			return errProtectedInfrastructureTrigger
		}
		return nil
	}, nil
}

func validateLocalSmokeBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("unsafe local smoke URL")
	}
	if parsed.Hostname() == "127.0.0.1" {
		return nil
	}
	if parsed.Hostname() != "localhost" {
		return errors.New("non-loopback local smoke URL")
	}
	addresses, err := net.DefaultResolver.LookupIP(context.Background(), "ip", "localhost")
	if err != nil || len(addresses) == 0 {
		return errors.New("unresolved local smoke URL")
	}
	for _, address := range addresses {
		if !address.IsLoopback() {
			return errors.New("non-loopback local smoke resolution")
		}
	}
	return nil
}

func newLocalSmokeTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "127.0.0.1" {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, port))
		}
		if host != "localhost" {
			return nil, errors.New("non-loopback smoke dial")
		}
		addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("unresolved smoke dial")
		}
		for _, candidate := range addresses {
			if !candidate.IsLoopback() {
				return nil, errors.New("non-loopback smoke dial")
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	return transport
}

type containedInfrastructureReportWriter struct{ workspace string }

func newContainedInfrastructureReportWriter(workspace string) (*containedInfrastructureReportWriter, error) {
	if workspace == "" || filepath.IsAbs(infrastructureSmokeReportDirectory) {
		return nil, errMissingInfrastructureCommandConfig
	}
	return &containedInfrastructureReportWriter{workspace: workspace}, nil
}

func (w *containedInfrastructureReportWriter) Write(report *smoke.SmokeReport) (string, error) {
	if report == nil {
		return "", errors.New("missing smoke report")
	}
	root := filepath.Join(w.workspace, infrastructureSmokeReportDirectory)
	document, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("infra-%d.json", time.Now().UTC().UnixNano())
	directory, err := openContainedReportDirectory(w.workspace)
	if err != nil {
		return "", err
	}
	defer unix.Close(directory)
	fd, err := unix.Openat(directory, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err := file.Write(document); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(directory, name, 0)
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

func openContainedReportDirectory(workspace string) (int, error) {
	root, err := unix.Open(workspace, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	current := root
	for _, component := range strings.Split(infrastructureSmokeReportDirectory, "/") {
		if err := unix.Mkdirat(current, component, 0750); err != nil && !errors.Is(err, unix.EEXIST) {
			_ = unix.Close(current)
			return -1, err
		}
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if err != nil {
			return -1, err
		}
		current = next
	}
	return current, nil
}
