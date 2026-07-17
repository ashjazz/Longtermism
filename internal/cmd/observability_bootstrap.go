package cmd

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	metricAPI "go.opentelemetry.io/otel/metric"
	traceAPI "go.opentelemetry.io/otel/trace"
)

// ObservabilitySignalPolicy 是 provider 创建的唯一信号开关。当前 SDK lifecycle
// 必须成对安装 trace/meter provider，故半配置直接失败，不能生成难以推理的全局状态。
type ObservabilitySignalPolicy struct {
	TracesEnabled  bool
	MetricsEnabled bool
}

// ObservabilityBootstrapInput 是 composition root 消费的原始运行时输入。HeaderValue
// 只会在默认 Collector bundle 创建时短暂使用，绝不会进入 Bootstrap 的返回值。
type ObservabilityBootstrapInput struct {
	Enabled       bool
	Runtime       ObservabilityRuntimeConfigInput
	Resource      ObservabilityResourceInput
	SamplingRatio float64
	Signals       ObservabilitySignalPolicy
	SmokeEnabled  bool
	IngressTrust  ObservabilityIngressTrustPolicy
}

// ObservabilityBootstrap 是启动入口稍后消费的纯装配结果。它不注册路由、不启动 server，
// 也不保存任何凭据；T052 才负责把 Middleware 和 smoke gate 接入 HTTP 应用。
type ObservabilityBootstrap struct {
	Runtime           ObservabilityRuntimeConfig
	InfraSmokeEnabled bool
	Lifecycle         *ObservabilityProviderLifecycle
	Middleware        func(http.Handler) http.Handler
	Propagator        ObservabilityIngressPropagator
}

func (b *ObservabilityBootstrap) Flush(ctx context.Context) error {
	if b == nil || b.Lifecycle == nil {
		return nil
	}
	return b.Lifecycle.Flush(ctx)
}

func (b *ObservabilityBootstrap) Shutdown(ctx context.Context) error {
	if b == nil || b.Lifecycle == nil {
		return nil
	}
	return b.Lifecycle.Shutdown(ctx)
}

// ObservabilityBootstrapDependencies 将网络 bundle 与 HTTP middleware 留在可注入边界。
// 默认构造器仅指向 Collector；测试可用内存 fake 证明装配顺序，不需要 server 或 secret。
type ObservabilityBootstrapDependencies struct {
	BuildCollector  func(context.Context, ObservabilityOTLPExporterConfig) (ObservabilityLifecycleExporter, error)
	BuildProviders  func(ObservabilityLifecycleExporter) (traceAPI.TracerProvider, metricAPI.MeterProvider, error)
	BuildMiddleware func() func(http.Handler) http.Handler
	state           *observabilityBootstrapState
}

type observabilityBootstrapState struct {
	mu        sync.Mutex
	bootstrap *ObservabilityBootstrap
}

// processObservabilityBootstrapState 是默认 composition root 的唯一装配状态。应用进程只
// 有一个全局 provider，所以零值依赖的重复调用必须复用它，而非仅成为 lifecycle follower。
var processObservabilityBootstrapState observabilityBootstrapState

// BuildObservabilityBootstrap 以固定顺序完成唯一的应用装配边界：解析 runtime、建立
// resource/exporter 配置、短暂消费 header、创建 bundle，最后让 lifecycle 安装全局 provider。
func BuildObservabilityBootstrap(ctx context.Context, input ObservabilityBootstrapInput, dependencies ObservabilityBootstrapDependencies) (*ObservabilityBootstrap, error) {
	state := dependencies.state
	if state == nil {
		state = &processObservabilityBootstrapState
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.bootstrap != nil {
		return state.bootstrap, nil
	}

	runtimeInput, err := normalizeObservabilityBootstrapInput(input)
	if err != nil {
		return nil, err
	}
	runtime, err := ResolveObservabilityRuntimeConfig(runtimeInput)
	if err != nil {
		return nil, err
	}
	bootstrap := &ObservabilityBootstrap{
		Runtime:           runtime,
		InfraSmokeEnabled: input.SmokeEnabled,
		Middleware:        identityObservabilityMiddleware,
		Propagator:        NewObservabilityIngressPropagator(input.IngressTrust),
	}
	if !input.Signals.TracesEnabled && !input.Signals.MetricsEnabled {
		state.bootstrap = bootstrap
		return bootstrap, nil
	}
	if runtime.Mode != ObservabilityRuntimeModeCollector {
		// local/noop 模式不创建网络 exporter 或全局 provider；这是一条明确的离线安全路径。
		state.bootstrap = bootstrap
		return bootstrap, nil
	}

	resourceInput := input.Resource
	if resourceInput.Environment == "" {
		resourceInput.Environment = runtime.Environment
	}
	config, err := buildObservabilityOTLPExporterConfig(runtime, resourceInput, input.SamplingRatio)
	if err != nil {
		return nil, err
	}
	dependencies = defaultObservabilityBootstrapDependencies(dependencies, input.Runtime.Collector.HeaderValue)
	middleware := dependencies.BuildMiddleware()
	if middleware == nil {
		return nil, fmt.Errorf("build observability middleware: middleware is required")
	}
	exporter, err := dependencies.BuildCollector(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("build collector bundle: %w", err)
	}
	cleanupExporter := true
	defer func() {
		if cleanupExporter {
			_ = exporter.Shutdown(ctx)
		}
	}()
	tracerProvider, meterProvider, err := dependencies.BuildProviders(exporter)
	if err != nil {
		return nil, fmt.Errorf("build paired providers: %w", err)
	}
	if tracerProvider == nil || meterProvider == nil {
		return nil, fmt.Errorf("trace and meter providers must be configured together")
	}
	lifecycle := NewObservabilityProviderLifecycle(ObservabilityProviderLifecycleConfig{
		Exporter:                   exporter,
		TracerProvider:             tracerProvider,
		MeterProvider:              meterProvider,
		ExporterOwnsTracerProvider: true,
		ExporterOwnsMeterProvider:  true,
	})
	if err := lifecycle.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("initialize observability lifecycle: %w", err)
	}
	if status := lifecycle.Status(); status.InitializationFailed {
		return nil, fmt.Errorf("initialize observability lifecycle: %s", status.FailureMessage)
	}
	bootstrap.Lifecycle = lifecycle
	bootstrap.Middleware = middleware
	state.bootstrap = bootstrap
	cleanupExporter = false
	return bootstrap, nil
}

func normalizeObservabilityBootstrapInput(input ObservabilityBootstrapInput) (ObservabilityRuntimeConfigInput, error) {
	if input.Signals.TracesEnabled != input.Signals.MetricsEnabled {
		return ObservabilityRuntimeConfigInput{}, fmt.Errorf("trace and metric signals must be enabled together")
	}
	runtime := input.Runtime
	if !input.Enabled {
		if runtime.Mode != "" && runtime.Mode != ObservabilityRuntimeModeNoop {
			return ObservabilityRuntimeConfigInput{}, fmt.Errorf("disabled observability requires noop mode")
		}
		runtime.Mode = ObservabilityRuntimeModeNoop
		return runtime, nil
	}
	if runtime.Mode == "" || runtime.Mode == ObservabilityRuntimeModeNoop {
		return ObservabilityRuntimeConfigInput{}, fmt.Errorf("enabled observability requires local or collector mode")
	}
	return runtime, nil
}

func defaultObservabilityBootstrapDependencies(dependencies ObservabilityBootstrapDependencies, headerValue string) ObservabilityBootstrapDependencies {
	if dependencies.BuildCollector == nil {
		dependencies.BuildCollector = func(ctx context.Context, config ObservabilityOTLPExporterConfig) (ObservabilityLifecycleExporter, error) {
			return newObservabilityOTLPExporterFromConfig(ctx, config, headerValue)
		}
	}
	if dependencies.BuildProviders == nil {
		dependencies.BuildProviders = func(exporter ObservabilityLifecycleExporter) (traceAPI.TracerProvider, metricAPI.MeterProvider, error) {
			bundle, ok := exporter.(*ObservabilityOTLPExporter)
			if !ok || bundle.TracerProvider() == nil || bundle.MeterProvider() == nil {
				return nil, nil, fmt.Errorf("collector bundle must own paired providers")
			}
			return bundle.TracerProvider(), bundle.MeterProvider(), nil
		}
	}
	if dependencies.BuildMiddleware == nil {
		dependencies.BuildMiddleware = func() func(http.Handler) http.Handler {
			return identityObservabilityMiddleware
		}
	}
	return dependencies
}

func identityObservabilityMiddleware(next http.Handler) http.Handler { return next }
