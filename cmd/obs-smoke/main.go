package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	v1chat "github.com/ashjazz/Longtermism/api/v1/chat"
	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/backend"
	"github.com/ashjazz/Longtermism/internal/observability/failure"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
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

	// signoz 备选 profile 的查询面：三信号共用一个端点，ingestion key 仅用于
	// 查询认证头，绝不进入报告或错误文本（T138 契约）。
	SignozURL          string
	SignozIngestionKey string
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
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	scenario := os.Args[1]
	if scenario == "infra" {
		exitCode := runInfra(context.Background(), os.Args[2:], os.Stdout, os.Stderr, defaultInfraCommandDependencies())
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}
	if scenario == "chat" || scenario == "score" || scenario == "privacy" {
		exitCode := runLiveScenario(context.Background(), scenario, os.Args[2:], os.Stdout, os.Stderr, defaultLiveScenarioCommandDependencies())
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}
	if isKnownResilienceScenario(scenario) {
		// resilience 场景会暂停/重启真实服务：SIGINT/SIGTERM 先取消 context，
		// 让 runner 走自己的报告与 cleanup 路径（残留如实写入报告），而不是
		// 进程被信号直接杀死、丢失全部证据。
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		exitCode := runResilienceScenario(ctx, scenario, os.Args[2:], os.Stdout, os.Stderr, defaultResilienceScenarioDependencies())
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}
	os.Exit(2)
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
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*profile != "grafana" && *profile != "signoz") {
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
	// profile 由环境决定并必须与 --profile 一致（runInfra 已校验）：备选 profile 是
	// 部署选择，不是单次运行的随意切换。
	profile := envOrDefault(lookup, "LONGTERMISM_SMOKE_PROFILE", "grafana")
	config := infraCommandConfig{
		Profile:         profile,
		Deadline:        time.Now().UTC().Add(infrastructureSmokeTimeout),
		ReportDirectory: infrastructureSmokeReportDirectory,
		ApplicationURL:  envOrDefault(lookup, "LONGTERMISM_SMOKE_APP_BASE_URL", "http://127.0.0.1:8000"),
		LangfuseURL:     lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"),
		LangfuseAuth:    lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"),
		AIPlaneURL:      lookup("LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL"),
		AIPlaneAuth:     lookup("LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL"),
	}
	if profile == "signoz" {
		// 备选 profile：三信号共用一个 SigNoz 查询端点；Prometheus/Loki/Tempo 的
		// 引用不属于该部署，缺失是正确状态而不是配置错误。
		config.SignozURL = lookup("LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL")
		config.SignozIngestionKey = lookup("LONGTERMISM_SMOKE_SIGNOZ_INGESTION_KEY")
		if config.SignozURL == "" || config.LangfuseURL == "" || config.LangfuseAuth == "" || config.AIPlaneURL == "" || config.AIPlaneAuth == "" {
			return infraCommandConfig{}, errMissingInfrastructureCommandConfig
		}
		if err := validateLocalSmokeBaseURL(config.ApplicationURL); err != nil {
			return infraCommandConfig{}, errMissingInfrastructureCommandConfig
		}
		if err := validateLocalSmokeBaseURL(config.SignozURL); err != nil {
			return infraCommandConfig{}, errMissingInfrastructureCommandConfig
		}
		return config, nil
	}
	if profile != "grafana" {
		return infraCommandConfig{}, errMissingInfrastructureCommandConfig
	}
	config.PrometheusURL = lookup("LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL")
	config.LokiURL = lookup("LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL")
	config.TempoURL = lookup("LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL")
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
	run func(context.Context, smoke.InfrastructureSmokeRequest) (*smoke.SmokeReport, error)
	// backend 只暴露装配诊断所需的负向查询面：主线与备选 profile 的 smoke backend
	// 都实现它，命令层不感知具体平台类型。
	backend infrastructureAIPlaneProbe
}

type infrastructureAIPlaneProbe interface {
	QueryAIPlane(context.Context, smoke.PollMarkerTarget) (int, error)
}

func newDefaultInfrastructureCommandRunner(config infraCommandConfig) (infraCommandRunner, error) {
	// 应用入口与 trigger 与 profile 无关：备选 profile 只替换后端查询 adapter，
	// 请求 payload、应用 endpoint、marker 语义保持不变（T147 门控）。
	trigger, err := newProtectedInfrastructureSmokeTrigger(config.ApplicationURL, http.DefaultClient)
	if err != nil {
		return nil, err
	}
	langfuse, err := backend.NewLangfuseSmokeQueryClient(backend.LangfuseSmokeQueryConfig{BaseURL: config.LangfuseURL, Credential: config.LangfuseAuth})
	if err != nil {
		return nil, err
	}
	aiPlane, err := backend.NewAIPlaneSmokeQueryClient(backend.AIPlaneSmokeQueryConfig{BaseURL: config.AIPlaneURL, Credential: config.AIPlaneAuth})
	if err != nil {
		return nil, err
	}
	if config.Profile == "signoz" {
		signoz, err := backend.NewSignozSmokeQueryClient(backend.SignozQueryConfig{
			SignozURL:    config.SignozURL,
			IngestionKey: config.SignozIngestionKey,
			HTTPClient:   &http.Client{Transport: newLocalSmokeTransport()},
		})
		if err != nil {
			return nil, err
		}
		smokeBackend, err := backend.NewSignozInfrastructureSmokeBackend(backend.SignozInfrastructureSmokeBackendConfig{Signoz: signoz, Langfuse: langfuse, AIPlane: aiPlane})
		if err != nil {
			return nil, err
		}
		return &defaultInfrastructureCommandRunner{backend: smokeBackend, run: func(ctx context.Context, request smoke.InfrastructureSmokeRequest) (*smoke.SmokeReport, error) {
			return smoke.RunSignozInfrastructureSmoke(ctx, smoke.SignozInfrastructureSmokeRequest{Deadline: request.Deadline, Profile: config.Profile}, smoke.SignozInfrastructureSmokeRunnerDependencies{Backend: smokeBackend, Clock: systemPollerClock{}, PollInterval: time.Second, Trigger: func(ctx context.Context, identity smoke.SignozSmokeIdentity) error {
				// trigger 契约与 profile 无关：同一受保护应用入口，identity 只是
				// 两个 runner 之间的同构值对象。
				return trigger(ctx, smoke.InfrastructureSmokeIdentity{RunID: identity.RunID, Marker: identity.Marker})
			}})
		}}, nil
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
	smokeBackend, err := backend.NewGrafanaInfrastructureSmokeBackend(backend.GrafanaInfrastructureSmokeBackendConfig{Grafana: grafana, Langfuse: langfuse, AIPlane: aiPlane})
	if err != nil {
		return nil, err
	}
	return &defaultInfrastructureCommandRunner{backend: smokeBackend, run: func(ctx context.Context, request smoke.InfrastructureSmokeRequest) (*smoke.SmokeReport, error) {
		return smoke.RunInfrastructureSmoke(ctx, request, smoke.InfrastructureSmokeRunnerDependencies{Backend: smokeBackend, Trigger: trigger, Clock: systemPollerClock{}, PollInterval: time.Second})
	}}, nil
}

func (r *defaultInfrastructureCommandRunner) Run(ctx context.Context, request smoke.InfrastructureSmokeRequest) (*smoke.SmokeReport, error) {
	if r == nil || r.run == nil {
		return nil, errMissingInfrastructureCommandConfig
	}
	return r.run(ctx, request)
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

type containedInfrastructureReportWriter struct {
	workspace string
	prefix    string
}

func newContainedInfrastructureReportWriter(workspace string) (*containedInfrastructureReportWriter, error) {
	return newContainedScenarioReportWriter(workspace, "infra")
}

func newContainedScenarioReportWriter(workspace, prefix string) (*containedInfrastructureReportWriter, error) {
	if workspace == "" || filepath.IsAbs(infrastructureSmokeReportDirectory) || prefix == "" || prefix != strings.TrimSpace(prefix) || strings.ContainsAny(prefix, `/\`) {
		return nil, errMissingInfrastructureCommandConfig
	}
	return &containedInfrastructureReportWriter{workspace: workspace, prefix: prefix}, nil
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
	name := fmt.Sprintf("%s-%d.json", w.prefix, time.Now().UTC().UnixNano())
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

// ---------------------------------------------------------------------------
// Privacy composition command wiring（T198）：预检 → concrete stores/adapters → 组合。
// 这是生产组合的唯一装配根；T181/T108 的场景 CLI 仍保持未完成，本文件不实现它们。
// ---------------------------------------------------------------------------

const defaultPrivacyFixtureTriggerTimeout = 10 * time.Second

var errPrivacyCommandConfiguration = errors.New("privacy command configuration failed")

type privacyCommandConfig struct {
	Profile        string
	Deadline       time.Time
	SurfaceTimeout time.Duration

	MasterSmokeEnabled, ChatSmokeEnabled bool
	ApplicationURL                       string
	ChatSmokeAuthorization               string
	ChatManifestRoot                     string
	PrivacyArtifactRoot                  string

	TempoURL, LokiURL, LangfuseURL string
	LangfuseCredential             string
	ScoreProjectionPath            string

	CollectorRuntimeConfigDigest string
	CollectorComponentIdentity   string
	ExportAdmissionCorrelation   string
}

// privacyCommandRunner 持有组合所需的全部 concrete capabilities 与 stores。Close 释放
// 全部有界资源；Run 执行完整 fixture→八 surface→报告闭环。
type privacyCommandRunner struct {
	trigger   *smoke.ProtectedPrivacyFixtureTrigger
	manifests *smoke.ChatRunManifestStore
	backend   *backend.PrivacySmokeBackend
	score     *localeval.ScoreProjectionStore
	clock     smoke.PollerClock
}

// newDefaultPrivacyCommandRunner 先做无副作用的整图预检，再打开 contained stores，
// 最后构造 concrete adapters。任何缺项在创建目录/网络之前失败。
func newDefaultPrivacyCommandRunner(config privacyCommandConfig) (*privacyCommandRunner, error) {
	if err := validatePrivacyCommandConfig(config); err != nil {
		return nil, err
	}
	manifests, err := smoke.OpenChatRunManifestStore(config.ChatManifestRoot)
	if err != nil {
		return nil, newPrivacyCommandError()
	}
	artifactStore, err := smoke.OpenPrivacyArtifactStore(config.PrivacyArtifactRoot)
	if err != nil {
		_ = manifests.Close()
		return nil, newPrivacyCommandError()
	}
	scoreStore, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: config.ScoreProjectionPath})
	if err != nil {
		_ = manifests.Close()
		_ = artifactStore.Close()
		return nil, newPrivacyCommandError()
	}
	cleanup := func() {
		_ = manifests.Close()
		_ = artifactStore.Close()
		_ = scoreStore.Close()
	}

	trigger, err := smoke.NewProtectedPrivacyFixtureTrigger(smoke.ProtectedPrivacyFixtureTriggerConfig{
		Endpoint: config.ApplicationURL, MasterSmokeEnabled: config.MasterSmokeEnabled,
		ChatSmokeEnabled: config.ChatSmokeEnabled, Authorization: config.ChatSmokeAuthorization,
		Timeout: defaultPrivacyFixtureTriggerTimeout,
	})
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	grafanaClient, err := backend.NewGrafanaSmokeQueryClient(backend.GrafanaQueryConfig{TempoURL: config.TempoURL, LokiURL: config.LokiURL})
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	langfuseTrace, err := backend.NewLangfuseChatSmokeQueryClient(backend.LangfuseChatSmokeQueryConfig{
		BaseURL: config.LangfuseURL, Credential: config.LangfuseCredential,
	})
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	langfuseScore, err := backend.NewLangfuseScoreSmokeBackend(backend.LangfuseScoreSmokeBackendConfig{
		BaseURL: config.LangfuseURL, Credential: config.LangfuseCredential, ProjectionStore: scoreStore,
	})
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	grafanaSurfaces, err := backend.NewPrivacyGrafanaSurfaces(grafanaClient)
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	langfuseSurfaces, err := backend.NewPrivacyLangfuseSurfaces(langfuseTrace, langfuseScore, scoreStore)
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	localSurfaces, err := backend.NewPrivacyLocalSurfaces(backend.PrivacyLocalSurfacesConfig{
		RuntimeConfigDigest: config.CollectorRuntimeConfigDigest,
		// 预队列工件即 Collector 的 runtime 配置本身：其 digest 同时充当 pre-queue hash
		// 绑定，写入时的组合证明与扫描时的 config 校验使用同一来源。
		ExpectedPrequeueArtifactSHA256: config.CollectorRuntimeConfigDigest,
		CollectorComponent:             config.CollectorComponentIdentity,
		ExportAdmissionCorrelation:     config.ExportAdmissionCorrelation,
	}, artifactStore)
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	privacyBackend, err := backend.NewPrivacySmokeBackend(artifactStore, localSurfaces, grafanaSurfaces, langfuseSurfaces, config.SurfaceTimeout)
	if err != nil {
		cleanup()
		return nil, newPrivacyCommandError()
	}
	runner := newPrivacyCommandRunner(trigger, manifests, privacyBackend, systemPollerClock{})
	runner.score = scoreStore
	return runner, nil
}

// newPrivacyCommandRunner 是生产组合装配根（反射契约固定签名）：只接收 T193-T197 的
// concrete capabilities，不接受任何泛型或可伪造证明依赖。
func newPrivacyCommandRunner(trigger *smoke.ProtectedPrivacyFixtureTrigger, manifests *smoke.ChatRunManifestStore, privacyBackend *backend.PrivacySmokeBackend, clock smoke.PollerClock) *privacyCommandRunner {
	return &privacyCommandRunner{trigger: trigger, manifests: manifests, backend: privacyBackend, clock: clock}
}

func (runner *privacyCommandRunner) Close() error {
	if runner == nil {
		return nil
	}
	var result error
	for _, closer := range []io.Closer{runner.manifests, runner.backend, runner.score} {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil && result == nil {
			result = err
		}
	}
	return result
}

// Run 执行完整隐私组合：fixture（受保护 chat + manifest 消费 + artifact 写入）由本
// runner 装配，八 surface 扫描与报告由 smoke.RunPrivacyComposition 承担。
func (runner *privacyCommandRunner) Run(ctx context.Context, request privacyCommandRequest) (*smoke.SmokeReport, error) {
	if runner == nil || runner.trigger == nil || runner.manifests == nil || runner.backend == nil {
		return nil, newPrivacyCommandError()
	}
	return smoke.RunPrivacyComposition(ctx, smoke.PrivacyCompositionRequest{
		RunID: request.RunID, Marker: request.Marker, Profile: request.Profile,
		ForbiddenCanary: request.ForbiddenCanary, StartedAt: request.StartedAt,
		Deadline: request.Deadline, SurfaceTimeout: request.SurfaceTimeout,
	}, smoke.PrivacyCompositionDependencies{
		Fixture:  privacyCommandFixtureRunner{trigger: runner.trigger, manifests: runner.manifests, writer: runner.backend},
		Surfaces: runner.backend,
		Clock:    runner.clock,
	})
}

type privacyCommandRequest struct {
	RunID, Marker, Profile, ForbiddenCanary string
	StartedAt, Deadline                     time.Time
	SurfaceTimeout                          time.Duration
}

type privacyCommandFixtureRunner struct {
	trigger   *smoke.ProtectedPrivacyFixtureTrigger
	manifests *smoke.ChatRunManifestStore
	writer    *backend.PrivacySmokeBackend
}

func (fixture privacyCommandFixtureRunner) Run(ctx context.Context, request smoke.PrivacyFixtureRequest) (smoke.PrivacyFixtureResult, error) {
	return smoke.RunPrivacyFixture(ctx, request, smoke.PrivacyFixtureDependencies{
		Trigger: fixture.trigger, Manifest: fixture.manifests, Writer: fixture.writer,
	})
}

// validatePrivacyCommandConfig 是纯内存预检：任何缺项/畸形值在创建 store 或构造
// client 之前失败，保证预检失败零副作用。
func validatePrivacyCommandConfig(config privacyCommandConfig) error {
	if config.Profile != "grafana" || !config.MasterSmokeEnabled || !config.ChatSmokeEnabled {
		return newPrivacyCommandError()
	}
	now := time.Now()
	if config.Deadline.IsZero() || !config.Deadline.After(now) || config.Deadline.Sub(now) > time.Minute {
		return newPrivacyCommandError()
	}
	if config.SurfaceTimeout <= 0 || config.SurfaceTimeout > maximumPrivacyCommandSurfaceTimeout {
		return newPrivacyCommandError()
	}
	for _, value := range []string{
		config.ApplicationURL, config.ChatSmokeAuthorization, config.ChatManifestRoot, config.PrivacyArtifactRoot,
		config.TempoURL, config.LokiURL, config.LangfuseURL, config.LangfuseCredential, config.ScoreProjectionPath,
		config.CollectorRuntimeConfigDigest, config.CollectorComponentIdentity, config.ExportAdmissionCorrelation,
	} {
		if strings.TrimSpace(value) == "" {
			return newPrivacyCommandError()
		}
	}
	if config.CollectorComponentIdentity != "otlphttp/loki" || !privacyCommandDigestPattern.MatchString(config.CollectorRuntimeConfigDigest) {
		return newPrivacyCommandError()
	}
	for _, path := range []string{config.ChatManifestRoot, config.PrivacyArtifactRoot, config.ScoreProjectionPath} {
		if !filepath.IsAbs(path) || path != filepath.Clean(path) {
			return newPrivacyCommandError()
		}
	}
	for _, endpoint := range []string{config.ApplicationURL, config.TempoURL, config.LokiURL, config.LangfuseURL} {
		if !validPrivacyCommandLoopbackEndpoint(endpoint) {
			return newPrivacyCommandError()
		}
	}
	return nil
}

const maximumPrivacyCommandSurfaceTimeout = 30 * time.Second

var privacyCommandDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func validPrivacyCommandLoopbackEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	host := parsed.Hostname()
	if host == "127.0.0.1" {
		return true
	}
	if host != "localhost" && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
		return true
	}
	return host == "localhost"
}

type privacyCommandError struct{}

func (privacyCommandError) Error() string { return errPrivacyCommandConfiguration.Error() }
func (privacyCommandError) Class() string { return "privacy_command_configuration_failed" }
func (privacyCommandError) Unwrap() error { return errPrivacyCommandConfiguration }

func newPrivacyCommandError() error { return privacyCommandError{} }

// ---------------------------------------------------------------------------
// Live scenarios（T108）：chat / score / privacy。
//
// 真实 live 调用必须显式 `--live` opt-in、固定 grafana profile，identity 全部由
// runner 生成；缺 opt-in 或任一 endpoint/credential/evidence/manifest reference 都
// 在 runner/client/transport 之前退出。退出码：passed=0、failed/skipped/runtime=1、
// usage/config=2；报告先安全持久化，stdout 严格只含 scenario/status/可信 report path。
// ---------------------------------------------------------------------------

const (
	liveChatScenario    = "chat"
	liveScoreScenario   = "score"
	livePrivacyScenario = "privacy"
	liveSmokeTimeout    = time.Minute
)

var errLiveScenarioConfiguration = errors.New("missing live scenario configuration")

// liveScenarioConfig 只承载低敏编排快照。marker/run-id 属于 runner-owned identity，
// credential/endpoint 由 composition root 即时读取并直接交给 concrete constructor，
// 绝不进入该快照或任何日志/stdout。
type liveScenarioConfig struct {
	Scenario string
	Profile  string
	Deadline time.Time
}

type liveScenarioCommandRunner interface {
	Run(context.Context) (*smoke.SmokeReport, error)
}

type liveScenarioCommandDependencies struct {
	ResolveConfig func(context.Context, string) (liveScenarioConfig, error)
	NewRunner     func(liveScenarioConfig) (liveScenarioCommandRunner, error)
	WriteReport   func(string, *smoke.SmokeReport) (string, error)
}

type liveScenarioCommandOutput struct {
	Scenario   string `json:"scenario"`
	Status     string `json:"status"`
	ReportPath string `json:"report_path"`
}

func isKnownLiveScenario(scenario string) bool {
	return scenario == liveChatScenario || scenario == liveScoreScenario || scenario == livePrivacyScenario
}

// runLiveScenario 是三个 live 场景的共享编排：opt-in 与 profile 校验、配置预检、
// concrete runner 构造、单次运行、报告先落盘、最后输出三字段低敏摘要。
func runLiveScenario(ctx context.Context, scenario string, args []string, stdout, stderr io.Writer, dependencies liveScenarioCommandDependencies) int {
	if dependencies.ResolveConfig == nil || dependencies.NewRunner == nil || dependencies.WriteReport == nil ||
		!isKnownLiveScenario(scenario) || containsForbiddenIdentityFlag(args) {
		return 2
	}
	flags := flag.NewFlagSet(scenario, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	live := flags.Bool("live", false, "explicit live smoke opt-in")
	profile := flags.String("profile", "grafana", "smoke profile")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !*live {
		return 2
	}
	// signoz 备选 profile 只开放 chat live 场景：score/privacy 的证据面（Langfuse
	// score 投影状态、Tempo/Loki 平台文档扫描）绑定主线后端，profile 不能伪装支持。
	profileAllowed := *profile == "grafana" || (scenario == liveChatScenario && *profile == "signoz")
	if !profileAllowed {
		return 2
	}
	config, err := dependencies.ResolveConfig(ctx, scenario)
	if err != nil || config.Scenario != scenario || config.Profile != *profile || config.Deadline.IsZero() {
		return 2
	}
	runner, err := dependencies.NewRunner(config)
	if err != nil || runner == nil {
		return 1
	}
	// live runner 持有 dirfd/store 等有界资源；命令只运行一次，无论结果如何都释放。
	if closer, ok := runner.(interface{ Close() error }); ok {
		defer func() { _ = closer.Close() }()
	}
	report, err := runner.Run(ctx)
	if err != nil || report == nil {
		return 1
	}
	path, err := dependencies.WriteReport(infrastructureSmokeReportDirectory, report)
	if err != nil || !isTrustedInfrastructureReportPath(infrastructureSmokeReportDirectory, path) {
		return 1
	}
	status := report.Status()
	if err := json.NewEncoder(stdout).Encode(liveScenarioCommandOutput{Scenario: scenario, Status: status, ReportPath: path}); err != nil {
		return 1
	}
	if status == "passed" {
		return 0
	}
	return 1
}

// defaultLiveScenarioCommandDependencies 是三个 live 场景的生产装配根。Report 按
// report 自身密封的 scenario 命名并写入同一受控目录。
func defaultLiveScenarioCommandDependencies() liveScenarioCommandDependencies {
	return liveScenarioCommandDependencies{
		ResolveConfig: resolveDefaultLiveScenarioConfig,
		NewRunner:     newDefaultLiveScenarioRunner,
		WriteReport: func(directory string, report *smoke.SmokeReport) (string, error) {
			if directory != infrastructureSmokeReportDirectory || report == nil {
				return "", errMissingInfrastructureCommandConfig
			}
			writer, err := newContainedScenarioReportWriter(".", report.Scenario())
			if err != nil {
				return "", err
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

// resolveDefaultLiveScenarioConfig 在任何 runner/client/transport 之前完成全部必填
// reference 预检。env 引用名是 T181 钉死的公开契约，只能引用、不能存值。
func resolveDefaultLiveScenarioConfig(_ context.Context, scenario string) (liveScenarioConfig, error) {
	if !isKnownLiveScenario(scenario) {
		return liveScenarioConfig{}, errLiveScenarioConfiguration
	}
	lookup := os.Getenv
	endpoints := make(map[string]string)
	paths := make(map[string]string)
	secrets := make(map[string]string)
	switch scenario {
	case liveChatScenario:
		endpoints["LONGTERMISM_SMOKE_APP_BASE_URL"] = lookup("LONGTERMISM_SMOKE_APP_BASE_URL")
		secrets["LONGTERMISM_SMOKE_CHAT_AUTHORIZATION"] = lookup("LONGTERMISM_SMOKE_CHAT_AUTHORIZATION")
		secrets["LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"] = lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL")
		paths["LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT"] = lookup("LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT")
		if envOrDefault(lookup, "LONGTERMISM_SMOKE_PROFILE", "grafana") == "signoz" {
			// 备选 profile：三信号与 LLM 计数共用 SigNoz 查询端点。
			endpoints["LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL")
		} else {
			endpoints["LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL")
			endpoints["LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL")
			// chat smoke 的 metric_delta 证据来自 LLM 请求计数器：缺 Prometheus 引用时
			// 该 check 必然失败，必须在预检阶段拒绝，而不是让每次 live 运行都白跑 60 秒。
			endpoints["LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL")
		}
		endpoints["LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL")
	case liveScoreScenario:
		endpoints["LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL")
		secrets["LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"] = lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL")
		paths["LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH"] = lookup("LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH")
		paths["LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH"] = lookup("LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH")
	case livePrivacyScenario:
		endpoints["LONGTERMISM_SMOKE_APP_BASE_URL"] = lookup("LONGTERMISM_SMOKE_APP_BASE_URL")
		endpoints["LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL")
		endpoints["LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL")
		endpoints["LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"] = lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL")
		secrets["LONGTERMISM_SMOKE_CHAT_AUTHORIZATION"] = lookup("LONGTERMISM_SMOKE_CHAT_AUTHORIZATION")
		secrets["LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"] = lookup("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL")
		paths["LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT"] = lookup("LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT")
		paths["LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT"] = lookup("LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT")
		paths["LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH"] = lookup("LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH")
		secrets["LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST"] = lookup("LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST")
		secrets["LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY"] = lookup("LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY")
		secrets["LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION"] = lookup("LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION")
	}
	for _, value := range endpoints {
		if err := validateLocalSmokeBaseURL(value); err != nil {
			return liveScenarioConfig{}, errLiveScenarioConfiguration
		}
	}
	for _, value := range secrets {
		if strings.TrimSpace(value) == "" {
			return liveScenarioConfig{}, errLiveScenarioConfiguration
		}
	}
	for _, value := range paths {
		if !filepath.IsAbs(value) || value != filepath.Clean(value) {
			return liveScenarioConfig{}, errLiveScenarioConfiguration
		}
	}
	if scenario == livePrivacyScenario {
		if !privacyCommandDigestPattern.MatchString(lookup("LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST")) ||
			lookup("LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY") != "otlphttp/loki" {
			return liveScenarioConfig{}, errLiveScenarioConfiguration
		}
	}
	profile := "grafana"
	if scenario == liveChatScenario {
		profile = envOrDefault(lookup, "LONGTERMISM_SMOKE_PROFILE", "grafana")
	}
	return liveScenarioConfig{Scenario: scenario, Profile: profile, Deadline: time.Now().UTC().Add(liveSmokeTimeout)}, nil
}

// newDefaultLiveScenarioRunner 为每个 scenario 构造 concrete runner：只读取经过
// 预检的 env 引用，任何构造失败都发生在网络 I/O 之前。
func newDefaultLiveScenarioRunner(config liveScenarioConfig) (liveScenarioCommandRunner, error) {
	switch config.Scenario {
	case liveChatScenario:
		return newLiveChatCommandRunner(config)
	case liveScoreScenario:
		return newLiveScoreCommandRunner(config)
	case livePrivacyScenario:
		return newLivePrivacyCommandRunner(config)
	default:
		return nil, errLiveScenarioConfiguration
	}
}

// ---------------------------------------------------------------------------
// chat live scenario
// ---------------------------------------------------------------------------

const (
	liveChatSmokeMessage         = "Live smoke: verify the dual-plane evidence loop end to end."
	liveChatTriggerTimeout       = 15 * time.Second
	liveChatSmokeMaximumResponse = 1 << 20
)

var errProtectedLiveChatTrigger = errors.New("protected live chat trigger failed")

type liveChatCommandRunner struct {
	close func() error
	run   func(context.Context) (*smoke.SmokeReport, error)
}

func (r *liveChatCommandRunner) Run(ctx context.Context) (*smoke.SmokeReport, error) {
	if r == nil || r.run == nil {
		return nil, errProtectedLiveChatTrigger
	}
	return r.run(ctx)
}

func (r *liveChatCommandRunner) Close() error {
	if r == nil || r.close == nil {
		return nil
	}
	return r.close()
}

func newLiveChatCommandRunner(config liveScenarioConfig) (liveScenarioCommandRunner, error) {
	applicationURL := os.Getenv("LONGTERMISM_SMOKE_APP_BASE_URL")
	authorization := os.Getenv("LONGTERMISM_SMOKE_CHAT_AUTHORIZATION")
	manifestRoot := os.Getenv("LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT")
	tempoURL := os.Getenv("LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL")
	lokiURL := os.Getenv("LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL")
	prometheusURL := os.Getenv("LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL")
	langfuseURL := os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL")
	langfuseCredential := os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL")

	manifests, err := smoke.OpenChatRunManifestStore(manifestRoot)
	if err != nil {
		return nil, errLiveScenarioConfiguration
	}
	trigger, err := newProtectedLiveChatTrigger(applicationURL, authorization, http.DefaultClient, manifests)
	if err != nil {
		_ = manifests.Close()
		return nil, errLiveScenarioConfiguration
	}
	langfuse, err := backend.NewLangfuseChatSmokeQueryClient(backend.LangfuseChatSmokeQueryConfig{BaseURL: langfuseURL, Credential: langfuseCredential})
	if err != nil {
		_ = manifests.Close()
		return nil, errLiveScenarioConfiguration
	}
	if config.Profile == "signoz" {
		// 备选 profile：三信号/LLM 计数经 SigNoz，AI trace 沿用主线 Langfuse 客户端，
		// score 投影经受保护的 scores 计数查询——trigger 与 payload 不变（T147 门控）。
		signozURL := os.Getenv("LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL")
		signoz, err := backend.NewSignozSmokeQueryClient(backend.SignozQueryConfig{
			SignozURL: signozURL, HTTPClient: &http.Client{Transport: newLocalSmokeTransport()},
		})
		if err != nil {
			_ = manifests.Close()
			return nil, errLiveScenarioConfiguration
		}
		scoreCounter, err := backend.NewLangfuseScoreCountQueryClient(backend.LangfuseScoreCountConfig{BaseURL: langfuseURL, Credential: langfuseCredential})
		if err != nil {
			_ = manifests.Close()
			return nil, errLiveScenarioConfiguration
		}
		chatBackend, err := backend.NewSignozChatSmokeBackend(backend.SignozChatSmokeBackendConfig{Signoz: signoz, Langfuse: langfuse, Score: scoreCounter})
		if err != nil {
			_ = manifests.Close()
			return nil, errLiveScenarioConfiguration
		}
		return &liveChatCommandRunner{
			close: manifests.Close,
			run: func(ctx context.Context) (*smoke.SmokeReport, error) {
				return smoke.RunSignozChatSmoke(ctx, smoke.SignozChatSmokeRequest{Profile: config.Profile, Deadline: config.Deadline}, smoke.SignozChatSmokeRunnerDependencies{
					Backend: chatBackend, Clock: systemPollerClock{}, PollInterval: time.Second,
					Trigger: func(ctx context.Context, identity smoke.SignozSmokeIdentity) (smoke.ChatSmokeAPIResult, error) {
						return trigger(ctx, smoke.ChatSmokeIdentity{RunID: identity.RunID, Marker: identity.Marker})
					},
				})
			},
		}, nil
	}
	grafana := backend.NewGrafanaQueryClient(backend.GrafanaQueryConfig{
		TempoURL: tempoURL, LokiURL: lokiURL, PrometheusURL: prometheusURL,
		HTTPClient: &http.Client{Transport: newLocalSmokeTransport()},
	})
	chatBackend := backend.NewGrafanaChatSmokeBackend(backend.GrafanaChatSmokeBackendConfig{Grafana: grafana, Langfuse: langfuse})
	return &liveChatCommandRunner{
		close: manifests.Close,
		run: func(ctx context.Context) (*smoke.SmokeReport, error) {
			return smoke.RunChatSmoke(ctx, smoke.ChatSmokeRequest{Profile: "grafana", Deadline: config.Deadline}, smoke.ChatSmokeRunnerDependencies{
				Backend: chatBackend, Clock: systemPollerClock{}, PollInterval: time.Second, Trigger: trigger,
			})
		},
	}, nil
}

// newProtectedLiveChatTrigger 对 /api/v1/chat 做受保护 live POST：loopback-only
// transport、独立共享 smoke auth header、runner-owned marker、固定低敏消息体。
// 响应只投影 request_id/ai_trace_id；native service trace/span 只从受控 run
// manifest 一次性读取，绝不猜测。
func newProtectedLiveChatTrigger(baseURL, authorization string, client *http.Client, manifests *smoke.ChatRunManifestStore) (func(context.Context, smoke.ChatSmokeIdentity) (smoke.ChatSmokeAPIResult, error), error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errProtectedLiveChatTrigger
	}
	if strings.TrimSpace(authorization) == "" || manifests == nil {
		return nil, errProtectedLiveChatTrigger
	}
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	if client == http.DefaultClient {
		copy.Transport = newLocalSmokeTransport()
	}
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	endpoint := parsed.ResolveReference(&url.URL{Path: "/api/v1/chat"})
	return func(ctx context.Context, identity smoke.ChatSmokeIdentity) (smoke.ChatSmokeAPIResult, error) {
		if identity.Marker == "" {
			return smoke.ChatSmokeAPIResult{}, errProtectedLiveChatTrigger
		}
		body := `{"message":"` + liveChatSmokeMessage + `"}`
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(body))
		if err != nil {
			return smoke.ChatSmokeAPIResult{}, errProtectedLiveChatTrigger
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(v1chat.ChatSmokeRunIDHeader, identity.Marker)
		request.Header.Set(v1chat.ChatSmokeAuthorizationHeader, authorization)
		response, err := copy.Do(request)
		if err != nil {
			return smoke.ChatSmokeAPIResult{}, errProtectedLiveChatTrigger
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return smoke.ChatSmokeAPIResult{}, errProtectedLiveChatTrigger
		}
		payload, err := io.ReadAll(io.LimitReader(response.Body, liveChatSmokeMaximumResponse+1))
		if err != nil || len(payload) > liveChatSmokeMaximumResponse {
			return smoke.ChatSmokeAPIResult{}, errProtectedLiveChatTrigger
		}
		var envelope v1chat.ChatSuccessEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Code != 0 || envelope.Data.Content == "" || envelope.Meta.RequestID == "" || envelope.Meta.AITraceID == "" {
			return smoke.ChatSmokeAPIResult{}, errProtectedLiveChatTrigger
		}
		manifest, err := manifests.Consume(ctx, identity.Marker)
		if err != nil || manifest.SmokeRunID != identity.Marker || manifest.RequestID != envelope.Meta.RequestID || manifest.AITraceID != envelope.Meta.AITraceID {
			return smoke.ChatSmokeAPIResult{}, errProtectedLiveChatTrigger
		}
		return smoke.ChatSmokeAPIResult{
			RequestID: envelope.Meta.RequestID, AITraceID: envelope.Meta.AITraceID,
			ServiceTraceID: manifest.ServiceTraceID, SpanID: manifest.SpanID,
		}, nil
	}, nil
}

// ---------------------------------------------------------------------------
// score live scenario
// ---------------------------------------------------------------------------

// liveScoreEvidenceStore 把本地 projection 记录投影为 score smoke evidence，并强制
// 每条 evidence 都必须先有本地 eval 记录：平台结果绝不能事后制造事实。
type liveScoreEvidenceStore struct {
	projections *localeval.ScoreProjectionStore
	evidence    *localeval.LocalEvidenceStore
}

func (s *liveScoreEvidenceStore) Find(ctx context.Context, runID string) ([]smoke.ScoreSmokeEvidence, error) {
	if s == nil || s.projections == nil || s.evidence == nil {
		return nil, errLiveScenarioConfiguration
	}
	snapshots, err := s.projections.FindByRunID(ctx, runID)
	if err != nil {
		// 证据读取错误一律折叠为稳定哨兵：score runner 会把任何 Find 错误映射为
		// storage_unavailable check，错误值本身必须低敏且不泄露 store 内部细节。
		return nil, errLiveScenarioConfiguration
	}
	result := make([]smoke.ScoreSmokeEvidence, 0, len(snapshots))
	for _, snapshot := range snapshots {
		records, err := s.evidence.Find(ctx, snapshot.EvalRunID)
		if err != nil || len(records) == 0 {
			return nil, errLiveScenarioConfiguration
		}
		result = append(result, smoke.ScoreSmokeEvidence{
			EvalRunID: snapshot.EvalRunID, ProjectionID: snapshot.ProjectionID,
			RequestID: snapshot.RequestID, AITraceID: snapshot.AITraceID,
			PlatformTraceID: snapshot.PlatformTraceID, PlatformObservationID: snapshot.PlatformObservationID,
		})
	}
	return result, nil
}

type liveScoreCommandRunner struct {
	close func() error
	run   func(context.Context) (*smoke.SmokeReport, error)
}

func (r *liveScoreCommandRunner) Run(ctx context.Context) (*smoke.SmokeReport, error) {
	if r == nil || r.run == nil {
		return nil, errLiveScenarioConfiguration
	}
	return r.run(ctx)
}

func (r *liveScoreCommandRunner) Close() error {
	if r == nil || r.close == nil {
		return nil
	}
	return r.close()
}

func newLiveScoreCommandRunner(config liveScenarioConfig) (liveScenarioCommandRunner, error) {
	langfuseURL := os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL")
	langfuseCredential := os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL")
	projectionPath := os.Getenv("LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH")
	evidencePath := os.Getenv("LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH")

	projectionStore, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: projectionPath})
	if err != nil {
		return nil, errLiveScenarioConfiguration
	}
	evidenceStore, err := localeval.OpenLocalEvidenceStore(localeval.LocalEvidenceStoreConfig{Path: evidencePath})
	if err != nil {
		_ = projectionStore.Close()
		return nil, errLiveScenarioConfiguration
	}
	scoreBackend, err := backend.NewLangfuseScoreSmokeBackend(backend.LangfuseScoreSmokeBackendConfig{
		BaseURL: langfuseURL, Credential: langfuseCredential, ProjectionStore: projectionStore,
	})
	if err != nil {
		_ = projectionStore.Close()
		_ = evidenceStore.Close()
		return nil, errLiveScenarioConfiguration
	}
	store := &liveScoreEvidenceStore{projections: projectionStore, evidence: evidenceStore}
	return &liveScoreCommandRunner{
		close: func() error { return errors.Join(projectionStore.Close(), evidenceStore.Close()) },
		run: func(ctx context.Context) (*smoke.SmokeReport, error) {
			return smoke.RunScoreSmoke(ctx, smoke.ScoreSmokeRequest{Profile: "grafana", Deadline: config.Deadline}, smoke.ScoreSmokeRunnerDependencies{
				EvidenceStore: store, Backend: scoreBackend, Clock: systemPollerClock{}, PollInterval: time.Second,
				IdentityFactory: newLiveScoreIdentity(projectionStore),
			})
		},
	}, nil
}

// newLiveScoreIdentity 从本地 projection store 解析最近一次 pending 投影的 run：
// score 场景验证的是上一次真实 chat run 的异步投影，identity 只能来自本地事实。
// LoadPending 与后续 FindByRunID 之间存在天然 TOCTOU：应用的 score worker 可能在这
// 两步之间推进/清理投影状态。该窗口只可能让后续查找失败（fail-closed），绝不能靠
// 重试或放宽条件把竞态伪装成成功。
func newLiveScoreIdentity(projections *localeval.ScoreProjectionStore) func(context.Context) (smoke.ScoreSmokeIdentity, error) {
	return func(ctx context.Context) (smoke.ScoreSmokeIdentity, error) {
		if projections == nil {
			return smoke.ScoreSmokeIdentity{}, errLiveScenarioConfiguration
		}
		pending, err := projections.LoadPending(ctx)
		if err != nil || len(pending) == 0 {
			return smoke.ScoreSmokeIdentity{}, errLiveScenarioConfiguration
		}
		latest := pending[0]
		for _, record := range pending[1:] {
			if record.CreatedAt.After(latest.CreatedAt) {
				latest = record
			}
		}
		return smoke.ScoreSmokeIdentity{RunID: latest.RunID, Marker: latest.RunID}, nil
	}
}

// ---------------------------------------------------------------------------
// privacy live scenario
// ---------------------------------------------------------------------------

type livePrivacyCommandRunner struct {
	privacy        *privacyCommandRunner
	config         liveScenarioConfig
	surfaceTimeout time.Duration
	close          func() error
}

func (r *livePrivacyCommandRunner) Run(ctx context.Context) (*smoke.SmokeReport, error) {
	if r == nil || r.privacy == nil {
		return nil, errLiveScenarioConfiguration
	}
	runID, marker, canary, err := newLivePrivacyIdentity()
	if err != nil {
		return nil, errLiveScenarioConfiguration
	}
	return r.privacy.Run(ctx, privacyCommandRequest{
		RunID: runID, Marker: marker, Profile: "grafana", ForbiddenCanary: canary,
		StartedAt: time.Now().UTC(), Deadline: r.config.Deadline, SurfaceTimeout: r.surfaceTimeout,
	})
}

func (r *livePrivacyCommandRunner) Close() error {
	if r == nil || r.close == nil {
		return nil
	}
	return r.close()
}

// newLivePrivacyIdentity 生成 runner-owned 隐私身份：run marker 与 synthetic canary
// 都来自 crypto/rand，禁止任何 CLI 输入参与。
func newLivePrivacyIdentity() (runID, marker, canary string, err error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", "", "", err
	}
	nonce := hex.EncodeToString(value)
	return "privacy-run-" + nonce, "privacy-marker-" + nonce, "synthetic-canary-" + nonce, nil
}

func newLivePrivacyCommandRunner(config liveScenarioConfig) (liveScenarioCommandRunner, error) {
	privacyConfig := privacyCommandConfig{
		Profile:                      "grafana",
		MasterSmokeEnabled:           true,
		ChatSmokeEnabled:             true,
		Deadline:                     config.Deadline,
		SurfaceTimeout:               maximumPrivacyCommandSurfaceTimeout,
		ApplicationURL:               os.Getenv("LONGTERMISM_SMOKE_APP_BASE_URL"),
		ChatSmokeAuthorization:       os.Getenv("LONGTERMISM_SMOKE_CHAT_AUTHORIZATION"),
		ChatManifestRoot:             os.Getenv("LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT"),
		PrivacyArtifactRoot:          os.Getenv("LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT"),
		TempoURL:                     os.Getenv("LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL"),
		LokiURL:                      os.Getenv("LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL"),
		LangfuseURL:                  os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"),
		LangfuseCredential:           os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"),
		ScoreProjectionPath:          os.Getenv("LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH"),
		CollectorRuntimeConfigDigest: os.Getenv("LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST"),
		CollectorComponentIdentity:   os.Getenv("LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY"),
		ExportAdmissionCorrelation:   os.Getenv("LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION"),
	}
	privacy, err := newDefaultPrivacyCommandRunner(privacyConfig)
	if err != nil {
		return nil, errLiveScenarioConfiguration
	}
	return &livePrivacyCommandRunner{
		privacy: privacy, config: config, surfaceTimeout: privacyConfig.SurfaceTimeout,
		close: privacy.Close,
	}, nil
}

// ---------------------------------------------------------------------------
// Resilience scenarios（T130）：exporter-failure / persistent-queue /
// score-worker-failure / resilience（full aggregate）。
//
// 这些场景会暂停、重启真实 compose 服务，属于破坏性验证：
// - 必须显式 `--live` opt-in，固定 grafana profile，marker/run-id 永远由
//   runner 生成，caller 传入的 identity flag 一律拒绝（exit 2）；
// - 退出码与 live 场景同一约定：passed=0、failed/skipped/runtime=1、
//   usage/config=2；
// - 报告先安全持久化，stdout 严格只含 scenario/status/可信 report path；
// - full aggregate 逐个执行 7 个子场景：任一子场景失败不阻断后续子场景
//   （cleanup trap），子报告先于聚合报告落盘，marker 唯一性强制，运行期
//   未知状态按保守残留记录（paused-service），绝不静默丢弃失败证据。
// ---------------------------------------------------------------------------

const (
	resilienceExporterFailureScenario    = "exporter-failure"
	resiliencePersistentQueueScenario    = "persistent-queue"
	resilienceScoreWorkerFailureScenario = "score-worker-failure"
	resilienceFullScenario               = "resilience"

	// 单场景 deadline 覆盖 runner 内部的 120 秒恢复窗口加轮询余量。
	resilienceSingleScenarioTimeout = 5 * time.Minute
	// full aggregate 串行执行 7 个子场景，每个都继承聚合 deadline。
	resilienceFullScenarioTimeout = 30 * time.Minute
)

var (
	errResilienceScenarioConfiguration = errors.New("missing resilience scenario configuration")

	// errResilienceCapabilityUnavailable 是显式声明的收敛缺口：具体的
	// Collector 组件遥测快照后端（Prometheus otelcol 指标族查询/解码）与
	// score worker 故障注入通道尚未实现。默认装配必须在此 fail-fast——在
	// 任何 docker 控制、网络请求或故障注入之前——绝不能用伪造的零计数
	// 后端让场景假通过（语义优先约束：事实缺失不能被猜测）。
	errResilienceCapabilityUnavailable = errors.New("resilience live composition is not available yet")
)

// resilienceExporterTargets 把 CLI target 选择器映射到 failure 目录域。
// 目录是组件事实的唯一来源：目录缺失对应域时 CLI 拒绝该 target，
// 防止部署资产漂移后仍在注入不存在的出口。
var resilienceExporterTargets = map[string]failure.Domain{
	"tempo":    failure.DomainTempoExporter,
	"loki":     failure.DomainLokiExporter,
	"langfuse": failure.DomainLangfuseExporter,
}

// resilienceScoreWorkerCases 把 CLI case 选择器映射到 runner 的故障子场景。
var resilienceScoreWorkerCases = map[string]smoke.ScoreWorkerFailureScenario{
	"langfuse-api": smoke.ScoreWorkerFailureLangfuseAPI,
	"queue-full":   smoke.ScoreWorkerFailureQueueFull,
	"shutdown":     smoke.ScoreWorkerFailureShutdown,
}

// resilienceComposeProjectPattern 与 failure 包的 DockerControl 校验同一类
// 风险：compose project 名在触达任何命令之前先通过安全边界。
var resilienceComposeProjectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// resilienceScenarioRequest 是配置预检的输入：只携带场景名与注入目标
// 选择器，绝不携带 identity。
type resilienceScenarioRequest struct {
	Scenario, Target, Case string
}

// resilienceScenarioConfig 只承载低敏编排快照。marker/run-id 属于
// runner-owned identity，credential/endpoint 由 composition root 即时读取，
// 绝不进入该快照或任何日志/stdout。
type resilienceScenarioConfig struct {
	Scenario string
	Profile  string
	Deadline time.Time
	Target   string
	Case     string
}

type resilienceScenarioRunner interface {
	Run(context.Context) (*smoke.SmokeReport, error)
}

type resilienceScenarioDependencies struct {
	ResolveConfig func(context.Context, resilienceScenarioRequest) (resilienceScenarioConfig, error)
	NewRunner     func(resilienceScenarioConfig) (resilienceScenarioRunner, error)
	WriteReport   func(string, *smoke.SmokeReport) (string, error)
}

func isKnownResilienceScenario(scenario string) bool {
	return scenario == resilienceExporterFailureScenario ||
		scenario == resiliencePersistentQueueScenario ||
		scenario == resilienceScoreWorkerFailureScenario ||
		scenario == resilienceFullScenario
}

func validResilienceExporterTarget(target string) bool {
	domain, ok := resilienceExporterTargets[target]
	if !ok {
		return false
	}
	_, defined := failure.Lookup(domain)
	return defined
}

// runResilienceScenario 是四个 resilience 场景的共享编排：破坏性 opt-in 与
// profile 校验、target/case 选择器校验、配置预检、runner 构造、单次运行、
// 报告先落盘、最后输出三字段低敏摘要。ctx 原样透传给 runner：main 中的
// signal trap 通过取消该 context 触发 runner 的报告/cleanup 路径。
func runResilienceScenario(ctx context.Context, scenario string, args []string, stdout, stderr io.Writer, dependencies resilienceScenarioDependencies) int {
	if dependencies.ResolveConfig == nil || dependencies.NewRunner == nil || dependencies.WriteReport == nil ||
		!isKnownResilienceScenario(scenario) || containsForbiddenIdentityFlag(args) {
		return 2
	}
	flags := flag.NewFlagSet(scenario, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	live := flags.Bool("live", false, "explicit destructive resilience opt-in")
	profile := flags.String("profile", "grafana", "smoke profile")
	var target, scenarioCase *string
	if scenario == resilienceExporterFailureScenario {
		target = flags.String("target", "", "exporter failure injection target")
	}
	if scenario == resilienceScoreWorkerFailureScenario {
		scenarioCase = flags.String("case", "", "score worker failure case")
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !*live || *profile != "grafana" {
		return 2
	}
	request := resilienceScenarioRequest{Scenario: scenario}
	if target != nil && !validResilienceExporterTarget(*target) {
		return 2
	}
	if scenarioCase != nil {
		if _, ok := resilienceScoreWorkerCases[*scenarioCase]; !ok {
			return 2
		}
	}
	request.Target, request.Case = flagsDereference(target), flagsDereference(scenarioCase)
	config, err := dependencies.ResolveConfig(ctx, request)
	if err != nil || config.Scenario != scenario || config.Profile != *profile || config.Deadline.IsZero() ||
		config.Target != request.Target || config.Case != request.Case {
		return 2
	}
	runner, err := dependencies.NewRunner(config)
	if err != nil || runner == nil {
		return 1
	}
	if closer, ok := runner.(interface{ Close() error }); ok {
		defer func() { _ = closer.Close() }()
	}
	report, err := runner.Run(ctx)
	if err != nil || report == nil {
		return 1
	}
	path, err := dependencies.WriteReport(infrastructureSmokeReportDirectory, report)
	if err != nil || !isTrustedInfrastructureReportPath(infrastructureSmokeReportDirectory, path) {
		return 1
	}
	status := report.Status()
	if err := json.NewEncoder(stdout).Encode(liveScenarioCommandOutput{Scenario: scenario, Status: status, ReportPath: path}); err != nil {
		return 1
	}
	if status == "passed" {
		return 0
	}
	return 1
}

func flagsDereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ---------------------------------------------------------------------------
// full resilience aggregate runner
// ---------------------------------------------------------------------------

// resilienceFullRunner 串行编排 7 个子场景并聚合为 scenario=full 的单一
// schema report。子 runner 与子报告 writer 都是注入的窄端口：聚合器只做
// 编排、唯一性强制与低敏事实投影，从不解析子报告之外的任何平台数据。
type resilienceFullRunner struct {
	config   resilienceScenarioConfig
	newSub   func(resilienceScenarioConfig) (resilienceScenarioRunner, error)
	writeSub func(string, *smoke.SmokeReport) (string, error)
	now      func() time.Time
}

func newResilienceFullRunner(config resilienceScenarioConfig, newSub func(resilienceScenarioConfig) (resilienceScenarioRunner, error), writeSub func(string, *smoke.SmokeReport) (string, error), now func() time.Time) *resilienceFullRunner {
	return &resilienceFullRunner{config: config, newSub: newSub, writeSub: writeSub, now: now}
}

// resilienceFullSubScenarios 是 full 模式的固定子场景序列。顺序稳定，
// 保证报告行序、执行序与测试断言一一对应；三个 exporter 目标来自 failure
// 目录域，persistent-queue 固定以 Tempo 出口验证跨重启投递。
func resilienceFullSubScenarios(parent resilienceScenarioConfig) []resilienceScenarioConfig {
	subs := make([]resilienceScenarioConfig, 0, 7)
	for _, target := range []string{"tempo", "loki", "langfuse"} {
		subs = append(subs, resilienceScenarioConfig{Scenario: resilienceExporterFailureScenario, Profile: parent.Profile, Deadline: parent.Deadline, Target: target})
	}
	subs = append(subs, resilienceScenarioConfig{Scenario: resiliencePersistentQueueScenario, Profile: parent.Profile, Deadline: parent.Deadline})
	for _, scenarioCase := range []string{"langfuse-api", "queue-full", "shutdown"} {
		subs = append(subs, resilienceScenarioConfig{Scenario: resilienceScoreWorkerFailureScenario, Profile: parent.Profile, Deadline: parent.Deadline, Case: scenarioCase})
	}
	return subs
}

// resiliencePrimaryBackend 把子场景映射到报告允许的主证据后端，与
// failure 目录的证据源边界一致：exporter/queue -> collector 遥测，
// score worker -> langfuse_score。
func resiliencePrimaryBackend(scenario string) string {
	if scenario == resilienceScoreWorkerFailureScenario {
		return "langfuse_score"
	}
	return "collector"
}

type resilienceFullIdentity struct{ RunID, Marker string }

func newResilienceFullIdentity() (resilienceFullIdentity, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return resilienceFullIdentity{}, err
	}
	encoded := hex.EncodeToString(value)
	return resilienceFullIdentity{RunID: "resilience-run-" + encoded, Marker: "resilience-marker-" + encoded}, nil
}

// resilienceSubOutcome 是单个子场景的聚合输入：报告行 + 需要并入聚合
// cleanup 的保守残留事实。
type resilienceSubOutcome struct {
	row      smoke.BackendCheckInput
	residual []string
	// cleanupTrouble 表示子场景声明了 cleanup 失败但没有携带任何允许的
	// 残留值；聚合必须仍以 failed 结束并补一个保守残留。
	cleanupTrouble bool
}

func (r *resilienceFullRunner) Run(ctx context.Context) (*smoke.SmokeReport, error) {
	startedAt := r.now().UTC()
	identity, err := newResilienceFullIdentity()
	if err != nil {
		return nil, err
	}
	seenMarkers := make(map[string]struct{})
	residuals := make([]string, 0, 4)
	addResidual := func(value string) {
		if !slices.Contains(residuals, value) {
			residuals = append(residuals, value)
		}
	}
	cleanupTrouble := false
	subs := resilienceFullSubScenarios(r.config)
	checks := make([]smoke.BackendCheckInput, 0, len(subs))
	for _, sub := range subs {
		outcome := r.runSub(ctx, sub, seenMarkers)
		checks = append(checks, outcome.row)
		cleanupTrouble = cleanupTrouble || outcome.cleanupTrouble
		for _, residual := range outcome.residual {
			addResidual(residual)
		}
	}
	cleanupStatus := "completed"
	if cleanupTrouble || len(residuals) > 0 {
		cleanupStatus = "failed"
		if len(residuals) == 0 {
			// cleanup 状态机只允许 failed 携带残留/临时数据失败之一：
			// 保守补 temporary-debug-data 表示证据链不可全信。
			addResidual("temporary-debug-data")
		}
	}
	finishedAt := r.now().UTC()
	if finishedAt.After(r.config.Deadline) {
		finishedAt = r.config.Deadline
	}
	return smoke.BuildSmokeReport(smoke.SmokeReportInput{
		RunID:      identity.RunID,
		Marker:     identity.Marker,
		Profile:    r.config.Profile,
		Scenario:   "full",
		StartedAt:  startedAt,
		Deadline:   r.config.Deadline,
		FinishedAt: finishedAt,
		Checks:     checks,
		Cleanup: smoke.SmokeCleanupInput{
			Status:               cleanupStatus,
			ResidualResources:    residuals,
			TemporaryCredentials: "not_created",
			TemporaryData:        "not_created",
		},
	})
}

// runSub 执行一个子场景并投影为聚合行。失败编码规则：
//   - 构造失败：发生在任何装配副作用之前，known-clean——failed/preflight，
//     不补残留；
//   - 运行错误或 panic：违反 runner 契约（正常失败必须写入报告），注入
//     状态未知——保守记录 paused-service 残留；
//   - marker 重复：identity 契约被违反，cleanup 事实不可信——保守记录
//     paused-service 残留；
//   - 子报告写盘失败：运行事实保留在聚合行，证据工件丢失记录
//     temporary-debug-data 残留；
//   - 子 cleanup 失败：如实传播其残留值。
//
// 任何失败都不 return：后续子场景继续执行，它们的 cleanup 与证据照常
// 保留（cleanup trap）。
func (r *resilienceFullRunner) runSub(ctx context.Context, sub resilienceScenarioConfig, seenMarkers map[string]struct{}) (outcome resilienceSubOutcome) {
	backend := resiliencePrimaryBackend(sub.Scenario)
	runner, err := r.newSub(sub)
	if err != nil || runner == nil {
		return resilienceSubOutcome{row: resiliencePreflightFailureRow(backend)}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = resilienceSubOutcome{
				row:      resiliencePreflightFailureRow(backend),
				residual: []string{"paused-service"},
			}
		}
	}()
	report, runErr := runner.Run(ctx)
	if runErr != nil || report == nil {
		return resilienceSubOutcome{row: resiliencePreflightFailureRow(backend), residual: []string{"paused-service"}}
	}
	if _, duplicate := seenMarkers[report.Marker()]; duplicate {
		return resilienceSubOutcome{
			row:      smoke.BackendCheckInput{Backend: backend, Status: "failed", FailureStage: "query", ErrorClass: "identity_mismatch"},
			residual: []string{"paused-service"},
		}
	}
	seenMarkers[report.Marker()] = struct{}{}
	row := resilienceRowFromReport(backend, report)
	if cleanup := report.Cleanup(); cleanup.Status == "failed" {
		if len(cleanup.ResidualResources) == 0 {
			return resilienceSubOutcome{row: row, cleanupTrouble: true}
		}
		return resilienceSubOutcome{row: row, residual: cleanup.ResidualResources}
	}
	if _, writeErr := r.writeSub(infrastructureSmokeReportDirectory, report); writeErr != nil {
		return resilienceSubOutcome{row: row, residual: []string{"temporary-debug-data"}}
	}
	return resilienceSubOutcome{row: row}
}

// resiliencePreflightFailureRow 是子场景无法产出证据时的稳定投影：
// failed/preflight/invalid_configuration。闭合错误词表内最接近"该子场景
// 无法开始"的类别，且不伪造任何证据键。
func resiliencePreflightFailureRow(backend string) smoke.BackendCheckInput {
	return smoke.BackendCheckInput{Backend: backend, Status: "failed", FailureStage: "preflight", ErrorClass: "invalid_configuration"}
}

// resilienceRowFromReport 把子报告投影为一行低敏事实：状态如实复制；
// failed 行的 failure_stage/error_class 复制子报告第一个失败 check 的事实
// （cleanup 失败优先映射为 cleanup stage），绝不发明新类别。
func resilienceRowFromReport(backend string, report *smoke.SmokeReport) smoke.BackendCheckInput {
	status := report.Status()
	if status != "failed" {
		return smoke.BackendCheckInput{Backend: backend, Status: status, FailureStage: "none"}
	}
	for _, check := range report.Checks() {
		if check.Status == "failed" {
			return smoke.BackendCheckInput{Backend: backend, Status: status, FailureStage: check.FailureStage, ErrorClass: check.ErrorClass}
		}
	}
	if report.Cleanup().Status == "failed" {
		return smoke.BackendCheckInput{Backend: backend, Status: status, FailureStage: "cleanup"}
	}
	return smoke.BackendCheckInput{Backend: backend, Status: status, FailureStage: "query"}
}

// ---------------------------------------------------------------------------
// resilience 默认装配
// ---------------------------------------------------------------------------

// writeContainedScenarioReport 与 live 场景共用受控目录契约：报告按自身
// 密封的 scenario 命名、0600、相对可信路径。
func writeContainedScenarioReport(directory string, report *smoke.SmokeReport) (string, error) {
	if directory != infrastructureSmokeReportDirectory || report == nil {
		return "", errMissingInfrastructureCommandConfig
	}
	writer, err := newContainedScenarioReportWriter(".", report.Scenario())
	if err != nil {
		return "", err
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
}

func defaultResilienceScenarioDependencies() resilienceScenarioDependencies {
	return resilienceScenarioDependencies{
		ResolveConfig: resolveDefaultResilienceScenarioConfig,
		NewRunner:     newDefaultResilienceScenarioRunner,
		WriteReport:   writeContainedScenarioReport,
	}
}

// resolveDefaultResilienceScenarioConfig 在任何 docker 控制、client 或
// transport 之前完成选择器与全部必填 reference 预检。env 名称是 T130 钉死
// 的公开契约：endpoint 复用 live 场景引用名，新增的只有 compose project。
func resolveDefaultResilienceScenarioConfig(_ context.Context, request resilienceScenarioRequest) (resilienceScenarioConfig, error) {
	if !isKnownResilienceScenario(request.Scenario) {
		return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
	}
	if request.Scenario == resilienceExporterFailureScenario && !validResilienceExporterTarget(request.Target) {
		return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
	}
	if request.Scenario != resilienceExporterFailureScenario && request.Target != "" {
		return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
	}
	if request.Scenario == resilienceScoreWorkerFailureScenario {
		if _, ok := resilienceScoreWorkerCases[request.Case]; !ok {
			return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
		}
	} else if request.Case != "" {
		return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
	}

	lookup := os.Getenv
	endpoints := []string{
		"LONGTERMISM_SMOKE_APP_BASE_URL",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL",
	}
	secrets := map[string]string{}
	paths := map[string]string{}
	switch request.Scenario {
	case resilienceExporterFailureScenario:
		endpoints = endpoints[:2]
	case resilienceScoreWorkerFailureScenario:
		endpoints = []string{"LONGTERMISM_SMOKE_APP_BASE_URL", "LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"}
		secrets["LONGTERMISM_SMOKE_CHAT_AUTHORIZATION"] = ""
		secrets["LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"] = ""
		paths["LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT"] = ""
		paths["LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH"] = ""
		paths["LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH"] = ""
	case resilienceFullScenario:
		secrets["LONGTERMISM_SMOKE_CHAT_AUTHORIZATION"] = ""
		secrets["LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"] = ""
		paths["LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT"] = ""
		paths["LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH"] = ""
		paths["LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH"] = ""
		// full 是 exporter ∪ queue ∪ score 的并集，还需要 Langfuse 查询端点。
		endpoints = append(endpoints, "LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL")
	}
	for _, key := range endpoints {
		if err := validateLocalSmokeBaseURL(lookup(key)); err != nil {
			return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
		}
	}
	for key := range secrets {
		if strings.TrimSpace(lookup(key)) == "" {
			return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
		}
	}
	for key := range paths {
		value := lookup(key)
		if !filepath.IsAbs(value) || value != filepath.Clean(value) {
			return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
		}
	}
	project := lookup("LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT")
	if !resilienceComposeProjectPattern.MatchString(project) {
		return resilienceScenarioConfig{}, errResilienceScenarioConfiguration
	}
	timeout := resilienceSingleScenarioTimeout
	if request.Scenario == resilienceFullScenario {
		timeout = resilienceFullScenarioTimeout
	}
	return resilienceScenarioConfig{
		Scenario: request.Scenario,
		Profile:  "grafana",
		Deadline: time.Now().UTC().Add(timeout),
		Target:   request.Target,
		Case:     request.Case,
	}, nil
}

// newDefaultResilienceScenarioRunner 为每个 scenario 构造 runner。
// full aggregate 复用本函数作为子工厂：子场景能力未收敛时每个子场景得到
// 稳定的 preflight 失败行（known-clean，无伪造残留），聚合报告仍完整、
// schema-valid；单场景则在任何副作用之前以能力哨兵 fail-fast。
func newDefaultResilienceScenarioRunner(config resilienceScenarioConfig) (resilienceScenarioRunner, error) {
	if config.Scenario == resilienceFullScenario {
		return newResilienceFullRunner(config, newDefaultResilienceScenarioRunner, writeContainedScenarioReport, func() time.Time {
			return time.Now().UTC()
		}), nil
	}
	switch config.Scenario {
	case resilienceExporterFailureScenario:
		return newResilienceExporterFailureRunner(config)
	case resiliencePersistentQueueScenario:
		return newResiliencePersistentQueueRunner(config)
	case resilienceScoreWorkerFailureScenario:
		// 收敛分 case：langfuse-api 已具备真实注入通道（pause/unpause
		// langfuse-web）；queue-full（进程内队列填充）与 shutdown（宿主进程
		// 内的 worker 停机）没有可用的受控通道，保持能力哨兵 fail-fast。
		if config.Case == "langfuse-api" {
			return newResilienceScoreWorkerFailureRunner(config)
		}
		return nil, errResilienceCapabilityUnavailable
	}
	return nil, errResilienceCapabilityUnavailable
}

// ---------------------------------------------------------------------------
// resilience live composition（T130 能力收敛）
// ---------------------------------------------------------------------------

// systemResilienceCommandRunner 是 failure.CommandRunner 的真实实现：
// 参数化 argv 执行，绝不 shell 拼接；输出只回传给调用方（DockerControl
// 不会把它写进任何报告或错误链）。
type systemResilienceCommandRunner struct{}

func (systemResilienceCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	return command.CombinedOutput()
}

// resilienceExporterServices 把 CLI target 选择器映射到 compose 服务名。
// 服务名是部署资产事实：tempo/loki 是同名服务，langfuse 平台是 langfuse-web。
var resilienceExporterServices = map[string]string{
	"tempo":    "tempo",
	"loki":     "loki",
	"langfuse": "langfuse-web",
}

const resilienceCollectorService = "collector"

// dockerResilienceInjector 把受控容器操作投影为 smoke 注入端口。
type dockerResilienceInjector struct {
	control *failure.DockerControl
}

func (i dockerResilienceInjector) Pause(ctx context.Context, service string) error {
	return i.control.Pause(ctx, service)
}

func (i dockerResilienceInjector) Unpause(ctx context.Context, service string) error {
	return i.control.Unpause(ctx, service)
}

func (i dockerResilienceInjector) RestartCollector(ctx context.Context) error {
	return i.control.Restart(ctx, resilienceCollectorService)
}

// newResilienceDockerControl 构造受控容器操作。compose project 已在配置
// 预检阶段通过安全校验；这里只读取。
func newResilienceDockerControl() (*failure.DockerControl, error) {
	return failure.NewDockerControl(systemResilienceCommandRunner{}, os.Getenv("LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT"))
}

// newResilienceInfraTrigger 构造 exporter/persistent-queue 场景的业务
// trigger：一次受保护 infra-smoke 请求。status/hash 是稳定响应事实
// （envelope status OK），runner 只比较故障前/故障中的一致性。
// persistent-queue 场景必须传入 sharedMarker：该场景的 runner 用自己的
// identity.Marker 查询 Tempo marker，trigger 请求携带的 smoke run_id 必须
// 与之相同，否则 trace 永远无法与报告身份关联（drain 验证恒失败）。
// exporter-failure 场景不需要 marker 关联（证据是组件遥测 delta），传空
// 时 trigger 自建唯一请求身份。
func newResilienceInfraTrigger(sharedMarker string) (func(context.Context) (int, string, error), func(context.Context) error, error) {
	baseURL := os.Getenv("LONGTERMISM_SMOKE_APP_BASE_URL")
	inner, err := newProtectedInfrastructureSmokeTrigger(baseURL, &http.Client{Transport: newLocalSmokeTransport()})
	if err != nil {
		return nil, nil, errResilienceCapabilityUnavailable
	}
	marker := sharedMarker
	if marker == "" {
		bytes := make([]byte, 16)
		if _, err := rand.Read(bytes); err != nil {
			return nil, nil, errResilienceCapabilityUnavailable
		}
		marker = "run-" + hex.EncodeToString(bytes)
	}
	identity := smoke.InfrastructureSmokeIdentity{RunID: marker, Marker: marker}
	return func(ctx context.Context) (int, string, error) {
			if err := inner(ctx, identity); err != nil {
				return 0, "", err
			}
			return 200, "infra-smoke-ok", nil
		}, func(ctx context.Context) error {
			return inner(ctx, identity)
		}, nil
}

func newResilienceExporterFailureRunner(config resilienceScenarioConfig) (resilienceScenarioRunner, error) {
	domain, ok := resilienceExporterTargets[config.Target]
	if !ok {
		return nil, errResilienceScenarioConfiguration
	}
	definition, defined := failure.Lookup(domain)
	service, known := resilienceExporterServices[config.Target]
	if !defined || !known {
		return nil, errResilienceScenarioConfiguration
	}
	queryClient, err := backend.NewGrafanaSmokeQueryClient(backend.GrafanaQueryConfig{
		PrometheusURL: os.Getenv("LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL"),
		Timeout:       liveSmokeTimeout,
	})
	if err != nil {
		return nil, errResilienceCapabilityUnavailable
	}
	control, err := newResilienceDockerControl()
	if err != nil {
		return nil, errResilienceCapabilityUnavailable
	}
	// exporter-failure 的证据是组件遥测 delta，与请求 marker 无关：
	// trigger 自建唯一请求身份即可。
	trigger, _, err := newResilienceInfraTrigger("")
	if err != nil {
		return nil, err
	}
	target := smoke.ExporterFailureSmokeTarget{
		BackendService: service,
		ComponentID:    definition.CollectorComponentID,
		EvidencePrefix: definition.StorageQueueName,
	}
	return resilienceScenarioFuncRunner(func(ctx context.Context) (*smoke.SmokeReport, error) {
		return smoke.RunExporterFailureSmoke(ctx, smoke.ExporterFailureSmokeRequest{
			Deadline: config.Deadline,
			Profile:  config.Profile,
			Target:   target,
		}, smoke.ExporterFailureSmokeDependencies{
			Backend:  backend.NewGrafanaCollectorSnapshotBackend(queryClient),
			Injector: dockerResilienceInjector{control: control},
			Trigger:  trigger,
			Clock:    systemPollerClock{},
		})
	}), nil
}

// resilienceQueueBackend 组合快照后端与 Tempo marker 查询：同一份
// GrafanaQueryClient 服务两个证据源。
type resilienceQueueBackend struct {
	snapshots *backend.GrafanaCollectorSnapshotBackend
	markers   *backend.GrafanaSmokeEvidenceAdapter
}

func (b resilienceQueueBackend) SnapshotCollectorQueue(ctx context.Context) (smoke.PersistentQueueSnapshot, error) {
	return b.snapshots.SnapshotCollectorQueue(ctx)
}

func (b resilienceQueueBackend) QueryBackendMarker(ctx context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	return b.markers.QueryTempoMarker(ctx, target)
}

func newResiliencePersistentQueueRunner(config resilienceScenarioConfig) (resilienceScenarioRunner, error) {
	definition, ok := failure.Lookup(failure.DomainTempoExporter)
	if !ok {
		return nil, errResilienceScenarioConfiguration
	}
	queryClient, err := backend.NewGrafanaSmokeQueryClient(backend.GrafanaQueryConfig{
		PrometheusURL: os.Getenv("LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL"),
		TempoURL:      os.Getenv("LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL"),
		Timeout:       liveSmokeTimeout,
	})
	if err != nil {
		return nil, errResilienceCapabilityUnavailable
	}
	control, err := newResilienceDockerControl()
	if err != nil {
		return nil, errResilienceCapabilityUnavailable
	}
	// persistent-queue 的 trigger 必须携带 runner identity 的 marker：
	// 先构造 identity（与 runner 内部同一生成器语义），trigger 与
	// IdentityFactory 共享，保证 trace 的 smoke run_id 与报告 marker 一致。
	sharedIdentity, err := smoke.NewPersistentQueueSmokeIdentity(context.Background())
	if err != nil {
		return nil, errResilienceCapabilityUnavailable
	}
	_, trigger, err := newResilienceInfraTrigger(sharedIdentity.Marker)
	if err != nil {
		return nil, err
	}
	identity := sharedIdentity
	return resilienceScenarioFuncRunner(func(ctx context.Context) (*smoke.SmokeReport, error) {
		return smoke.RunPersistentQueueSmoke(ctx, smoke.PersistentQueueSmokeRequest{
			Deadline:       config.Deadline,
			Profile:        config.Profile,
			BackendService: "tempo",
			ComponentID:    definition.CollectorComponentID,
		}, smoke.PersistentQueueSmokeDependencies{
			Backend:  resilienceQueueBackend{snapshots: backend.NewGrafanaCollectorSnapshotBackend(queryClient), markers: backend.NewGrafanaSmokeEvidenceAdapter(queryClient)},
			Injector: dockerResilienceInjector{control: control},
			Trigger:  trigger,
			Clock:    systemPollerClock{},
			// 恢复窗口默认 120s + 轮询余量；PollInterval 与 live 场景一致。
			PollInterval:    time.Second,
			IdentityFactory: func(context.Context) (smoke.PersistentQueueSmokeIdentity, error) { return identity, nil },
		})
	}), nil
}

// ---------------------------------------------------------------------------
// resilience score-worker composition（T130d）
// ---------------------------------------------------------------------------

// resilienceScoreWorkerBackend 组合三份事实：本地投影状态（ScoreProjectionStore）、
// 本地 eval evidence（LocalEvidenceStore digest）与 Langfuse 平台 score 计数。
// State/Attempts 来自本地 store（状态机唯一事实源）；PlatformScoreCount 只在
// 本地状态为 sent 时查询平台——未 sent 就宣称平台计数是伪造证据。
type resilienceScoreWorkerBackend struct {
	projections *localeval.ScoreProjectionStore
	evidence    *localeval.LocalEvidenceStore
	scores      *backend.LangfuseScoreSmokeBackend
}

func (b resilienceScoreWorkerBackend) LocalEvidenceSnapshot(ctx context.Context, target smoke.ScoreFailureEvidenceTarget) (smoke.ScoreFailureEvidenceSnapshot, error) {
	if b.evidence == nil {
		return smoke.ScoreFailureEvidenceSnapshot{}, errResilienceCapabilityUnavailable
	}
	records, err := b.evidence.Find(ctx, target.EvidenceID)
	if err != nil || len(records) == 0 {
		// evidence 缺失或读取失败都投影为不完整快照：FR-015 事实源优先，
		// runner 会在注入之前拒绝。
		return smoke.ScoreFailureEvidenceSnapshot{EvidenceID: target.EvidenceID, Complete: false}, nil
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return smoke.ScoreFailureEvidenceSnapshot{EvidenceID: target.EvidenceID, Complete: false}, nil
	}
	digest := sha256.Sum256(encoded)
	return smoke.ScoreFailureEvidenceSnapshot{
		EvidenceID: target.EvidenceID,
		Digest:     hex.EncodeToString(digest[:]),
		Complete:   true,
	}, nil
}

func (b resilienceScoreWorkerBackend) ScoreProjectionStates(ctx context.Context, target smoke.ScoreFailureProjectionTarget) ([]smoke.ScoreFailureProjectionObservation, error) {
	if b.projections == nil {
		return nil, errResilienceCapabilityUnavailable
	}
	snapshot, found, err := b.projections.FindByProjectionID(ctx, target.ProjectionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	observation := smoke.ScoreFailureProjectionObservation{
		ProjectionID: snapshot.ProjectionID,
		State:        string(snapshot.Status),
		Attempts:     snapshot.Attempt,
		ObservedAt:   snapshot.ObservedAt,
	}
	if snapshot.Status == langfuse.ScoreProjectionStatusSent && b.scores != nil {
		count, countErr := b.scores.ScoreCountByID(ctx, target.ProjectionID, target.StartedAt, target.Deadline, resilienceScoreCountQueryLimit)
		if countErr != nil {
			return nil, countErr
		}
		observation.PlatformScoreCount = count
	}
	return []smoke.ScoreFailureProjectionObservation{observation}, nil
}

const resilienceScoreCountQueryLimit = 8

// resilienceScoreWorkerInjector 的 langfuse-api 通道：pause/unpause langfuse-web。
// queue/shutdown 通道没有受控实现：被调用即失败（fail-fast），绝不静默无操作。
type resilienceScoreWorkerInjector struct {
	control *failure.DockerControl
}

func (i resilienceScoreWorkerInjector) FailLangfuseAPI(ctx context.Context) error {
	return i.control.Pause(ctx, "langfuse-web")
}

func (i resilienceScoreWorkerInjector) RestoreLangfuseAPI(ctx context.Context) error {
	return i.control.Unpause(ctx, "langfuse-web")
}

func (resilienceScoreWorkerInjector) FillScoreWorkerQueue(context.Context) error {
	return errResilienceCapabilityUnavailable
}

func (resilienceScoreWorkerInjector) DrainScoreWorkerQueue(context.Context) error {
	return errResilienceCapabilityUnavailable
}

func (resilienceScoreWorkerInjector) ShutdownScoreWorker(context.Context) error {
	return errResilienceCapabilityUnavailable
}

func (resilienceScoreWorkerInjector) RestartScoreWorker(context.Context) error {
	return errResilienceCapabilityUnavailable
}

// resilienceScoreWorkerRunner 把动态身份解析推迟到 Run：warm-up chat 创建
// 真实投影后，从本地 store 解析 ProjectionID/EvalRunID 再进入 runner 契约。
// 构造阶段保持零副作用（与 exporter/persistent-queue 一致）。
type resilienceScoreWorkerRunner struct {
	config resilienceScenarioConfig
}

func (r resilienceScoreWorkerRunner) Run(ctx context.Context) (*smoke.SmokeReport, error) {
	composition, err := resolveResilienceScoreWorkerComposition(ctx, r.config)
	if err != nil {
		return nil, err
	}
	defer composition.close()
	return smoke.RunScoreWorkerFailureSmoke(ctx, smoke.ScoreWorkerFailureSmokeRequest{
		Deadline:     r.config.Deadline,
		Profile:      r.config.Profile,
		Scenario:     smoke.ScoreWorkerFailureLangfuseAPI,
		EvidenceID:   composition.evidenceID,
		ProjectionID: composition.projectionID,
	}, smoke.ScoreWorkerFailureSmokeDependencies{
		Backend:      composition.backend,
		Injector:     composition.injector,
		Trigger:      composition.trigger,
		Clock:        systemPollerClock{},
		PollInterval: time.Second,
	})
}

type resilienceScoreWorkerComposition struct {
	backend      resilienceScoreWorkerBackend
	injector     resilienceScoreWorkerInjector
	trigger      smoke.ScoreWorkerFailureTrigger
	evidenceID   string
	projectionID string
	close        func() error
}

// resolveResilienceScoreWorkerComposition 执行 warm-up：一次受保护 chat（带
// 唯一 smoke marker）创建真实投影，然后从本地 store 解析身份。chat 的投影
// 通过 marker 作为 runID 持久化（EnqueueForRun 契约），FindByRunID 因此能
// 精确定位 warm-up 投影；后续 runner 内的 baseline/during chat 复用同一
// marker，它们的投影处于同一 runID 下但由 FindByProjectionID 区分。
func resolveResilienceScoreWorkerComposition(ctx context.Context, config resilienceScenarioConfig) (*resilienceScoreWorkerComposition, error) {
	authorization := os.Getenv("LONGTERMISM_SMOKE_CHAT_AUTHORIZATION")
	manifests, err := smoke.OpenChatRunManifestStore(os.Getenv("LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT"))
	if err != nil {
		return nil, errResilienceCapabilityUnavailable
	}
	chatTrigger, err := newProtectedLiveChatTrigger(os.Getenv("LONGTERMISM_SMOKE_APP_BASE_URL"), authorization, http.DefaultClient, manifests)
	if err != nil {
		_ = manifests.Close()
		return nil, errResilienceCapabilityUnavailable
	}
	projectionStore, err := localeval.OpenScoreProjectionStore(localeval.ScoreProjectionStoreConfig{Path: os.Getenv("LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH")})
	if err != nil {
		_ = manifests.Close()
		return nil, errResilienceCapabilityUnavailable
	}
	evidenceStore, err := localeval.OpenLocalEvidenceStore(localeval.LocalEvidenceStoreConfig{Path: os.Getenv("LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH")})
	if err != nil {
		_ = projectionStore.Close()
		_ = manifests.Close()
		return nil, errResilienceCapabilityUnavailable
	}
	scores, err := backend.NewLangfuseScoreSmokeBackend(backend.LangfuseScoreSmokeBackendConfig{
		BaseURL:         os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL"),
		Credential:      os.Getenv("LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL"),
		Timeout:         liveSmokeTimeout,
		ProjectionStore: projectionStore,
	})
	if err != nil {
		_ = evidenceStore.Close()
		_ = projectionStore.Close()
		_ = manifests.Close()
		return nil, errResilienceCapabilityUnavailable
	}
	control, err := newResilienceDockerControl()
	if err != nil {
		_ = evidenceStore.Close()
		_ = projectionStore.Close()
		_ = manifests.Close()
		return nil, errResilienceCapabilityUnavailable
	}

	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return nil, errResilienceCapabilityUnavailable
	}
	marker := "run-" + hex.EncodeToString(bytes)
	identity := smoke.ChatSmokeIdentity{RunID: marker, Marker: marker}

	// warm-up：投影异步创建，有界等待它出现在本地 store。
	result, err := chatTrigger(ctx, identity)
	if err != nil {
		_ = evidenceStore.Close()
		_ = projectionStore.Close()
		_ = manifests.Close()
		return nil, errResilienceCapabilityUnavailable
	}
	_ = result
	var warmUp localeval.ScoreProjectionSnapshot
	clock := systemPollerClock{}
	deadline := time.Now().Add(30 * time.Second)
	for {
		snapshots, findErr := projectionStore.FindByRunID(ctx, marker)
		if findErr == nil && len(snapshots) > 0 {
			warmUp = snapshots[0]
			break
		}
		if time.Now().After(deadline) {
			_ = evidenceStore.Close()
			_ = projectionStore.Close()
			_ = manifests.Close()
			return nil, errResilienceCapabilityUnavailable
		}
		if waitErr := clock.Wait(ctx, time.Second); waitErr != nil {
			_ = evidenceStore.Close()
			_ = projectionStore.Close()
			_ = manifests.Close()
			return nil, waitErr
		}
	}

	// chat bodyHash 是响应包络结构指纹（状态 + 内容存在性），不是原文
	// digest：真实 chat 两次响应内容必然不同，契约比较的是业务语义不变。
	trigger := func(ctx context.Context) (int, string, error) {
		if _, err := chatTrigger(ctx, identity); err != nil {
			return 0, "", err
		}
		return 200, "chat-envelope-ok", nil
	}

	return &resilienceScoreWorkerComposition{
		backend: resilienceScoreWorkerBackend{
			projections: projectionStore,
			evidence:    evidenceStore,
			scores:      scores,
		},
		injector:     resilienceScoreWorkerInjector{control: control},
		trigger:      trigger,
		evidenceID:   warmUp.EvalRunID,
		projectionID: warmUp.ProjectionID,
		close: func() error {
			return errors.Join(evidenceStore.Close(), projectionStore.Close(), manifests.Close())
		},
	}, nil
}

func newResilienceScoreWorkerFailureRunner(config resilienceScenarioConfig) (resilienceScenarioRunner, error) {
	return resilienceScoreWorkerRunner{config: config}, nil
}

// resilienceScenarioFuncRunner 把 runner 函数适配为场景 runner 端口。
type resilienceScenarioFuncRunner func(context.Context) (*smoke.SmokeReport, error)

func (r resilienceScenarioFuncRunner) Run(ctx context.Context) (*smoke.SmokeReport, error) {
	return r(ctx)
}
