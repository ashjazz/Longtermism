package cmd

import (
	"context"
	"fmt"
	"sync"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ObservabilityLifecycleExporter 是应用层管理 TracerProvider/exporter 生命周期的窄接口。
//
// 真实实现可以包住 OTel TracerProvider 或 GoFrame trace 初始化对象；测试实现只需要
// 记录调用次数。接口放在使用方，是为了避免 internal/cmd 暴露具体平台 SDK 类型。
type ObservabilityLifecycleExporter interface {
	Initialize(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// ObservabilityProviderLifecycleConfig 描述生命周期封装依赖。
type ObservabilityProviderLifecycleConfig struct {
	Exporter                   ObservabilityLifecycleExporter
	TracerProvider             trace.TracerProvider
	MeterProvider              metric.MeterProvider
	ExporterOwnsTracerProvider bool
	// ExporterOwnsMeterProvider 与 trace 所有权成对声明。当一个 exporter bundle
	// 统一管理 SDK provider 与底层连接时，lifecycle 不得再重复关闭 meter。
	ExporterOwnsMeterProvider bool
}

// ObservabilityProviderLifecycleStatus 是生命周期状态的只读快照。
type ObservabilityProviderLifecycleStatus struct {
	Initialized bool
	// InitializationFailed 表示不可恢复的初始化失败。Lifecycle 配置构造后不可变；
	// 修正配置时必须新建实例，不能把失败实例误认为已成功初始化。
	InitializationFailed bool
	Shutdown             bool
	FailureStatus        string
	FailureMessage       string
}

// ObservabilityProviderLifecycle 只管理 trace/meter provider 与 OTLP exporter bundle
// 的启动、flush 和关闭。Langfuse score worker 有独立的失败域和重试语义，不能由此
// 对象接管，否则会把基础设施观测的全局 provider 生命周期与 AI 语义分数投递混在一起。
type ObservabilityProviderLifecycle struct {
	mu                  sync.Mutex
	exporter            ObservabilityLifecycleExporter
	tracer              trace.TracerProvider
	meter               metric.MeterProvider
	exporterOwnsTracer  bool
	exporterOwnsMeter   bool
	ownsGlobalProviders bool
	status              ObservabilityProviderLifecycleStatus
}

// processGlobalProviders 防止同一进程中的第二个装配入口替换 OTel 全局 provider。
// OTel 全局代理在首次设置后会长期委托给该 provider；替换或重置会让仍在飞行中的
// 请求产生跨 provider 的 span/meter 混用，因此生命周期只允许首次安装。
var processGlobalProviders struct {
	mu        sync.Mutex
	installed bool
}

var installOTelGlobalProviders = func(tracerProvider trace.TracerProvider, meterProvider metric.MeterProvider) {
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
}

// NewObservabilityProviderLifecycle 创建幂等的 provider 生命周期管理器。
func NewObservabilityProviderLifecycle(config ObservabilityProviderLifecycleConfig) *ObservabilityProviderLifecycle {
	return &ObservabilityProviderLifecycle{
		exporter:           config.Exporter,
		tracer:             config.TracerProvider,
		meter:              config.MeterProvider,
		exporterOwnsTracer: config.ExporterOwnsTracerProvider,
		exporterOwnsMeter:  config.ExporterOwnsMeterProvider,
	}
}

// Initialize 初始化 exporter。重复调用不会重复初始化。
func (l *ObservabilityProviderLifecycle) Initialize(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.status.Initialized || l.status.InitializationFailed || l.status.Shutdown {
		return nil
	}
	if (l.exporterOwnsTracer || l.exporterOwnsMeter) && l.exporter == nil {
		l.recordInitializationFailure(fmt.Errorf("exporter-owned provider requires an exporter"))
		return nil
	}
	if l.tracer != nil || l.meter != nil {
		ownsGlobalProviders, err := l.initializeGlobalProviderOwner(ctx)
		if err != nil {
			l.recordInitializationFailure(err)
			return nil
		}
		l.ownsGlobalProviders = ownsGlobalProviders
		// 带 provider 的第二个 lifecycle 是只读 follower：它不能再启动自己的 exporter，
		// 否则虽未替换 global provider，仍会形成第二套发送/关闭生命周期。
		if !l.ownsGlobalProviders {
			l.status.Initialized = true
			return nil
		}
		l.status.Initialized = true
		return nil
	}

	if err := l.initializeExporter(ctx); err != nil {
		l.recordInitializationFailure(err)
		return nil
	}
	l.status.Initialized = true
	return nil
}

// initializeGlobalProviderOwner 把 provider 安装与 exporter 初始化放进同一进程级
// 临界区：若 exporter 初始化失败，provider 尚未成为全局对象，下一套正确配置仍可接管。
// 这段锁只出现在一次性启动阶段，优先保证全局 provider 的原子所有权。
func (l *ObservabilityProviderLifecycle) initializeGlobalProviderOwner(ctx context.Context) (bool, error) {
	if l.tracer == nil || l.meter == nil {
		return false, fmt.Errorf("trace and meter providers must be configured together")
	}

	processGlobalProviders.mu.Lock()
	defer processGlobalProviders.mu.Unlock()
	if processGlobalProviders.installed {
		return false, nil
	}
	if err := l.initializeExporter(ctx); err != nil {
		return false, err
	}
	installOTelGlobalProviders(l.tracer, l.meter)
	processGlobalProviders.installed = true
	return true, nil
}

func (l *ObservabilityProviderLifecycle) initializeExporter(ctx context.Context) error {
	if l.exporter == nil {
		return nil
	}
	return runLifecycleExporter(func() error {
		return l.exporter.Initialize(ctx)
	})
}

// Flush 请求 exporter 把缓冲信号在 shutdown 前或受控 checkpoint 时送出。未实现
// ForceFlush 的 exporter 保持兼容；超时/失败只记录观测故障，绝不能改写业务结果。
func (l *ObservabilityProviderLifecycle) Flush(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if flusher, ok := l.exporter.(interface {
		ForceFlush(context.Context) error
	}); ok && flusher != nil {
		if err := runLifecycleExporter(func() error {
			return flusher.ForceFlush(ctx)
		}); err != nil {
			l.recordFailure(err)
		}
	}
	if l.ownsGlobalProviders {
		// 默认由 lifecycle 管理直接注入的 trace/meter provider。只有装配层明确声明
		// exporter 持有同一个 TracerProvider 时，才跳过 direct trace flush，防止重复。
		if !l.exporterOwnsTracer {
			l.flushProvider(ctx, l.tracer)
		}
		if !l.exporterOwnsMeter {
			l.flushProvider(ctx, l.meter)
		}
	}
	return nil
}

// Shutdown 关闭 exporter。重复调用不会重复 shutdown，exporter 失败不会影响主流程。
func (l *ObservabilityProviderLifecycle) Shutdown(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.status.Shutdown {
		return nil
	}
	l.status.Shutdown = true

	if l.exporter != nil {
		if err := runLifecycleExporter(func() error {
			return l.exporter.Shutdown(ctx)
		}); err != nil {
			l.recordFailure(err)
		}
	}
	if l.ownsGlobalProviders {
		if !l.exporterOwnsTracer {
			l.shutdownProvider(ctx, l.tracer)
		}
		if !l.exporterOwnsMeter {
			l.shutdownProvider(ctx, l.meter)
		}
	}
	return nil
}

// Status 返回生命周期状态快照。
func (l *ObservabilityProviderLifecycle) Status() ObservabilityProviderLifecycleStatus {
	if l == nil {
		return ObservabilityProviderLifecycleStatus{}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	return l.status
}

func (l *ObservabilityProviderLifecycle) recordFailure(err error) {
	l.status.FailureStatus = string(obs.FailureTelemetryExportFailed)
	l.status.FailureMessage = err.Error()
}

func (l *ObservabilityProviderLifecycle) recordInitializationFailure(err error) {
	l.status.InitializationFailed = true
	l.recordFailure(err)
}

func (l *ObservabilityProviderLifecycle) flushProvider(ctx context.Context, provider any) {
	flusher, ok := provider.(interface{ ForceFlush(context.Context) error })
	if !ok || flusher == nil {
		return
	}
	if err := runLifecycleExporter(func() error { return flusher.ForceFlush(ctx) }); err != nil {
		l.recordFailure(err)
	}
}

func (l *ObservabilityProviderLifecycle) shutdownProvider(ctx context.Context, provider any) {
	shutdowner, ok := provider.(interface{ Shutdown(context.Context) error })
	if !ok || shutdowner == nil {
		return
	}
	if err := runLifecycleExporter(func() error { return shutdowner.Shutdown(ctx) }); err != nil {
		l.recordFailure(err)
	}
}

func runLifecycleExporter(run func() error) (err error) {
	if run == nil {
		return nil
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()

	return run()
}
