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
	"time"

	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	"github.com/ashjazz/Longtermism/internal/controller/health"
	controllerobservability "github.com/ashjazz/Longtermism/internal/controller/observability"
	logicobservability "github.com/ashjazz/Longtermism/internal/logic/observability"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcmd"
	"go.opentelemetry.io/otel"
)

const observabilityShutdownTimeout = 5 * time.Second

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

			s.Group("/api", func(group *ghttp.RouterGroup) {
				// MiddlewareHandlerResponse 提供统一响应信封 {code, message, data}，
				// 对应全局 rules/common/patterns.md 的「API 响应格式」约定。
				group.Middleware(ghttp.MiddlewareHandlerResponse)

				group.Group("/v1", func(v1 *ghttp.RouterGroup) {
					// 新增 controller 在此注册。命名对应 api/<domain>/v1 目录。
					v1.Bind(health.NewV1())
				})
			})
			if err := registerDefaultObservabilityRoutes(ctx, s, bootstrap); err != nil {
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

func registerDefaultObservabilityRoutes(ctx context.Context, server *ghttp.Server, bootstrap *ObservabilityBootstrap) error {
	rateLimitConfig, err := resolveInfraSmokeRateLimitConfig(ctx)
	if err != nil {
		return err
	}
	usecase := logicobservability.NewInfraSmokeUsecase(logicobservability.InfraSmokeUsecaseDependencies{
		Tracer: otel.Tracer("github.com/ashjazz/Longtermism/internal/logic/observability"),
	})
	controller := controllerobservability.NewV1(controllerobservability.InfraSmokeControllerDependencies{
		SmokeEnabled:         bootstrap.InfraSmokeEnabled,
		Runner:               usecase,
		RequestIDFromContext: RequestIDFromContext,
	})
	return RegisterObservabilityRoutes(server, ObservabilityRoutesInput{
		Bootstrap:         bootstrap,
		InfraSmokeHandler: newInfraSmokeHTTPHandler(controller),
		Limiter:           ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}),
		InfraSmokeLimit:   rateLimitConfig,
		state:             &processObservabilityRoutesState,
	})
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
