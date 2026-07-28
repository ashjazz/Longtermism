// Package cmd 是应用命令入口层。
//
// 这里完成 HTTP server 的装配：中间件、路由分组、版本前缀(/v1)以及
// 各 controller 的注册。AI 能力由 pkg/ai 提供，应用层只做"胶水"。
package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	"github.com/ashjazz/Longtermism/internal/controller/health"
	controllerobservability "github.com/ashjazz/Longtermism/internal/controller/observability"
	logicobservability "github.com/ashjazz/Longtermism/internal/logic/observability"
	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcfg"
	"github.com/gogf/gf/v2/os/gcmd"
	"go.opentelemetry.io/otel"
	"golang.org/x/sys/unix"
)

const (
	observabilityShutdownTimeout = 5 * time.Second
	collectorJSONLLogPath        = "/var/log/longtermism"
	hostJSONLLogPath             = "resource/log/observability"
	completionJSONLLogFile       = "application.jsonl"
)

var (
	// Main 是默认主命令。未来可扩展为多命令（gcmd.CommandWithOpts），
	// 例如 worker 子命令消费消息队列做异步 Agent 任务。
	Main = gcmd.Command{
		Name:  "longtermism",
		Usage: "longtermism",
		Brief: "生产级 AI Agent 框架（GoFrame v2）",
		Func: func(ctx context.Context, parser *gcmd.Parser) (err error) {
			s := g.Server()
			bootstrap, err := buildDefaultObservabilityBootstrap(ctx)
			if err != nil {
				return fmt.Errorf("initialize observability: %w", err)
			}
			defer shutdownObservabilityBootstrap(bootstrap)
			limiter := ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{})
			chatRuntime, err := buildDefaultChatRuntime(ctx, bootstrap)
			if err != nil {
				return fmt.Errorf("initialize chat runtime: %w", err)
			}
			defer func() {
				if closeErr := chatRuntime.Close(); closeErr != nil {
					g.Log().Error(ctx, "close chat runtime", closeErr)
				}
			}()
			completionMiddleware, err := newHTTPCompletionLoggingMiddleware(
				ctx,
				bootstrap,
				bootstrap.InfraSmokeEnabled || chatRuntime.Enabled,
			)
			if err != nil {
				return err
			}

			s.Group("/api", func(group *ghttp.RouterGroup) {
				// MiddlewareHandlerResponse 提供统一响应信封 {code, message, data}，
				// 对应全局 rules/common/patterns.md 的「API 响应格式」约定。
				group.Middleware(ghttp.MiddlewareHandlerResponse)

				group.Group("/v1", func(v1 *ghttp.RouterGroup) {
					// 新增 controller 在此注册。命名对应 api/<domain>/v1 目录。
					v1.Bind(health.NewV1())
				})
			})
			if err := registerDefaultObservabilityRoutes(ctx, s, bootstrap, limiter, completionMiddleware); err != nil {
				return err
			}
			if err := RegisterChatRoutes(s, ChatRoutesInput{
				Enabled:                     chatRuntime.Enabled,
				Bootstrap:                   bootstrap,
				CompletionLoggingMiddleware: completionMiddleware,
				Handler:                     chatRuntime.Handler,
				Limiter:                     limiter,
				Limit:                       chatRuntime.Limit,
				state:                       &processChatRoutesState,
			}); err != nil {
				return err
			}

			s.Run()
			return nil
		},
	}
)

// buildDefaultObservabilityBootstrap is the only production composition root that reads the
// raw configuration and transient Collector header value. Lower layers receive the sanitized
// bootstrap result and cannot accidentally build a second global provider.
func buildDefaultObservabilityBootstrap(ctx context.Context) (*ObservabilityBootstrap, error) {
	headerEnvironmentName := g.Cfg().MustGet(ctx, "observability.collector.headers_env", "").String()
	return BuildObservabilityBootstrap(ctx, ObservabilityBootstrapInput{
		Enabled: g.Cfg().MustGet(ctx, "observability.enabled", false).Bool(),
		Runtime: ObservabilityRuntimeConfigInput{
			Mode:        ObservabilityRuntimeMode(g.Cfg().MustGet(ctx, "observability.mode", "noop").String()),
			Environment: g.Cfg().MustGet(ctx, "observability.environment", "local").String(),
			Collector: ObservabilityCollectorConfigInput{
				Endpoint:      g.Cfg().MustGet(ctx, "observability.collector.endpoint", "").String(),
				Protocol:      g.Cfg().MustGet(ctx, "observability.collector.protocol", "grpc").String(),
				Timeout:       g.Cfg().MustGet(ctx, "observability.collector.timeout", "10s").String(),
				Insecure:      g.Cfg().MustGet(ctx, "observability.collector.insecure", false).Bool(),
				HeaderEnvName: headerEnvironmentName,
				HeaderValue:   os.Getenv(headerEnvironmentName),
			},
			Payload: obs.PayloadPolicyInput{
				Mode:              obs.PayloadMode(g.Cfg().MustGet(ctx, "observability.payload.mode", "metadata_only").String()),
				RawContentEnabled: g.Cfg().MustGet(ctx, "observability.payload.raw_content_enabled", false).Bool(),
			},
		},
		Resource: ObservabilityResourceInput{
			ServiceName: g.Cfg().MustGet(ctx, "observability.resource.service_name", "longtermism").String(),
			Version:     g.Cfg().MustGet(ctx, "observability.resource.service_version", "dev").String(),
		},
		SamplingRatio: g.Cfg().MustGet(ctx, "observability.tracing.sampling_ratio", 1.0).Float64(),
		Signals: ObservabilitySignalPolicy{
			TracesEnabled:  g.Cfg().MustGet(ctx, "observability.signals.traces_enabled", false).Bool(),
			MetricsEnabled: g.Cfg().MustGet(ctx, "observability.signals.metrics_enabled", false).Bool(),
		},
		SmokeEnabled: g.Cfg().MustGet(ctx, "observability.smoke.enabled", false).Bool(),
	}, ObservabilityBootstrapDependencies{})
}

func registerDefaultObservabilityRoutes(
	ctx context.Context,
	server *ghttp.Server,
	bootstrap *ObservabilityBootstrap,
	limiter ratelimit.Limiter,
	completionMiddleware func(http.Handler) http.Handler,
) error {
	rateLimitConfig, err := resolveInfraSmokeRateLimitConfig(ctx)
	if err != nil {
		return err
	}
	metrics, err := newInfraSmokeMetrics()
	if err != nil {
		return fmt.Errorf("create infra smoke metrics: %w", err)
	}
	usecase := logicobservability.NewInfraSmokeUsecase(logicobservability.InfraSmokeUsecaseDependencies{
		Tracer:        otel.Tracer("github.com/ashjazz/Longtermism/internal/logic/observability"),
		Metrics:       metrics,
		MetricFlusher: bootstrap,
	})
	controller := controllerobservability.NewV1(controllerobservability.InfraSmokeControllerDependencies{
		SmokeEnabled:         bootstrap.InfraSmokeEnabled,
		Runner:               usecase,
		RequestIDFromContext: RequestIDFromContext,
	})
	return RegisterObservabilityRoutes(server, ObservabilityRoutesInput{
		Bootstrap:                   bootstrap,
		CompletionLoggingMiddleware: completionMiddleware,
		InfraSmokeHandler:           newInfraSmokeHTTPHandler(controller),
		Limiter:                     limiter,
		InfraSmokeLimit:             rateLimitConfig,
		state:                       &processObservabilityRoutesState,
	})
}

func newHTTPCompletionLoggingMiddleware(
	ctx context.Context,
	bootstrap *ObservabilityBootstrap,
	isHTTPObservationEnabled bool,
) (func(http.Handler) http.Handler, error) {
	if bootstrap == nil || !isHTTPObservationEnabled {
		return nil, nil
	}
	config, err := gcfg.NewAdapterFile("manifest/config/glog-observability.yaml")
	if err != nil {
		return nil, fmt.Errorf("load observability glog config: %w", err)
	}
	loggerConfig := gcfg.NewWithAdapter(config)
	// Compose keeps the profile default under /var/log, while a host-run application must use a
	// project-local ignored directory that the Collector bind-mounts. The application config can
	// override only this boundary; it still never learns a backend endpoint.
	path, err := resolveHTTPCompletionLogPath(
		g.Cfg().MustGet(ctx, "observability.logs.path", loggerConfig.MustGet(ctx, "logger.path").String()).String(),
		loggerConfig.MustGet(ctx, "logger.path").String(),
	)
	if err != nil {
		return nil, err
	}
	file, err := resolveHTTPCompletionLogFile(loggerConfig.MustGet(ctx, "logger.file").String())
	if err != nil {
		return nil, err
	}
	output, err := openHTTPCompletionLog(path, file)
	if err != nil {
		return nil, fmt.Errorf("open observability JSONL file: %w", err)
	}
	writer, err := appobservability.NewJSONLHTTPCompletionLogWriter(output)
	if err != nil {
		_ = output.Close()
		return nil, err
	}
	return appobservability.NewHTTPCompletionLoggingMiddleware(appobservability.HTTPLoggingDependencies{
		Tracer:           otel.Tracer("github.com/ashjazz/Longtermism/internal/observability/http"),
		CompletionLogger: writer,
		Identify: func(request *http.Request) appobservability.HTTPRequestIdentity {
			return httpCompletionIdentity(request)
		},
	}), nil
}

// httpCompletionIdentity is intentionally derived at the HTTP boundary. The marker is accepted
// only for the one trusted smoke route, so ordinary requests cannot create a new log identity.
func httpCompletionIdentity(request *http.Request) appobservability.HTTPRequestIdentity {
	route := RouteTemplateFromContext(request.Context())
	identity := appobservability.HTTPRequestIdentity{RequestID: RequestIDFromContext(request.Context()), RouteTemplate: route}
	marker := request.Header.Get(v1observability.SmokeRunIDHeader)
	if route == "/api/v1/observability/infra-smoke" && marker != "" && v1observability.IsValidSmokeRunID(marker) {
		identity.IsSmokeRun = true
		identity.SmokeRunID = marker
	}
	return identity
}

func newInfraSmokeMetrics() (*appobservability.Metrics, error) {
	metrics, err := appobservability.NewMetrics(
		otel.Meter("github.com/ashjazz/Longtermism/internal/logic/observability"),
		appobservability.WithMetricLabelPolicy(appobservability.MetricLabelPolicy{AllowedRoutes: []string{"/api/v1/observability/infra-smoke"}}),
	)
	if err != nil {
		return nil, err
	}
	return metrics, nil
}

// resolveHTTPCompletionLogPath keeps the filesystem boundary closed: the local Grafana profile
// can write only its ignored project directory, and the container profile only its shared-volume
// path. Arbitrary config or environment overrides must not turn smoke into an arbitrary writer.
func resolveHTTPCompletionLogPath(path string, fallback string) (string, error) {
	if path == "" {
		path = fallback
	}
	switch path {
	case hostJSONLLogPath, collectorJSONLLogPath:
		return path, nil
	default:
		return "", fmt.Errorf("unsupported observability JSONL path: %q", path)
	}
}

// resolveHTTPCompletionLogFile prevents the independently loaded glog profile from escaping the
// approved directory through a relative or absolute filename. Completion logging owns one file.
func resolveHTTPCompletionLogFile(file string) (string, error) {
	if file != completionJSONLLogFile {
		return "", fmt.Errorf("unsupported observability JSONL file: %q", file)
	}
	return file, nil
}

// openHTTPCompletionLog owns the small filesystem boundary for the allowlisted JSONL stream.
// os.OpenFile creates a file but not its parent; creating the configured local directory here
// keeps host-run smoke deterministic without requiring developers to write under /var/log.
func openHTTPCompletionLog(path string, file string) (*os.File, error) {
	if path == "" || file == "" {
		return nil, fmt.Errorf("observability glog path and file are required")
	}
	if err := os.MkdirAll(path, 0750); err != nil {
		return nil, fmt.Errorf("create observability glog directory: %w", err)
	}
	// O_NOFOLLOW closes the check-to-open gap for an existing application.jsonl symlink.
	// The collector must never be induced to observe a file outside the two approved directories.
	descriptor, err := unix.Open(filepath.Join(path, file), unix.O_APPEND|unix.O_CREAT|unix.O_WRONLY|unix.O_NOFOLLOW, 0640)
	if err != nil {
		return nil, err
	}
	output := os.NewFile(uintptr(descriptor), filepath.Join(path, file))
	if output == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("wrap observability JSONL file descriptor")
	}
	if err := output.Chown(-1, os.Getgid()); err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("set observability JSONL group: %w", err)
	}
	// The host app and non-root Collector share the host user's primary group. Apply the intended
	// mode after opening so a restrictive umask or an old 0600 file cannot silently break filelog.
	if err := output.Chmod(0640); err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("set observability JSONL permissions: %w", err)
	}
	return output, nil
}

func resolveInfraSmokeRateLimitConfig(ctx context.Context) (InfraSmokeRateLimitConfig, error) {
	config := InfraSmokeRateLimitConfig{
		Rate:   g.Cfg().MustGet(ctx, "observability.smoke.rate_limit.rate", defaultInfraSmokeRate).Int(),
		Period: g.Cfg().MustGet(ctx, "observability.smoke.rate_limit.period", defaultInfraSmokePeriod.String()).Duration(),
	}
	if config.Rate <= 0 || config.Period <= 0 {
		return InfraSmokeRateLimitConfig{}, fmt.Errorf("observability smoke rate limit must be positive")
	}
	return config, nil
}

func newInfraSmokeHTTPHandler(controller *controllerobservability.ControllerV1) ghttp.HandlerFunc {
	return func(request *ghttp.Request) {
		result, err := controller.InfraSmoke(request.GetCtx(), &v1observability.InfraSmokeReq{
			SmokeRunID: request.Header.Get(v1observability.SmokeRunIDHeader),
		})
		if err != nil {
			var controllerError controllerobservability.InfraSmokeControllerError
			if !errors.As(err, &controllerError) {
				request.Response.Status = http.StatusInternalServerError
				request.Response.WriteJsonExit(v1observability.InfraSmokeErrorEnvelope{
					Code:    http.StatusInternalServerError,
					Message: "internal server error",
					Data:    nil,
					Meta:    v1observability.InfraSmokeMeta{RequestID: RequestIDFromContext(request.GetCtx())},
				})
				return
			}
			request.Response.Status = controllerError.StatusCode()
			request.Response.WriteJsonExit(controllerError.Envelope())
			return
		}
		request.Response.WriteJsonExit(result)
	}
}

func shutdownObservabilityBootstrap(bootstrap *ObservabilityBootstrap) {
	shutdownContext, cancel := context.WithTimeout(context.Background(), observabilityShutdownTimeout)
	defer cancel()
	if err := bootstrap.Flush(shutdownContext); err != nil {
		g.Log().Error(shutdownContext, "observability flush failed", "error_class", "telemetry_export_failed")
	}
	if err := bootstrap.Shutdown(shutdownContext); err != nil {
		g.Log().Error(shutdownContext, "observability shutdown failed", "error_class", "telemetry_export_failed")
	}
}
