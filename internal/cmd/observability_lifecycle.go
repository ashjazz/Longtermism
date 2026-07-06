package cmd

import (
	"context"
	"fmt"
	"sync"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

// ObservabilityLifecycleExporter 是应用层管理 TracerProvider/exporter 生命周期的窄接口。
//
// 真实实现可以包住 OTel TracerProvider 或 GoFrame trace 初始化对象；测试实现只需要
// 记录调用次数。接口放在使用方，是为了避免 internal/cmd 暴露具体平台 SDK 类型。
type ObservabilityLifecycleExporter interface {
	Initialize(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// ObservabilityTracerProviderLifecycleConfig 描述生命周期封装依赖。
type ObservabilityTracerProviderLifecycleConfig struct {
	Exporter ObservabilityLifecycleExporter
}

// ObservabilityTracerProviderLifecycleStatus 是生命周期状态的只读快照。
type ObservabilityTracerProviderLifecycleStatus struct {
	Initialized    bool
	Shutdown       bool
	FailureStatus  string
	FailureMessage string
}

// ObservabilityTracerProviderLifecycle 管理基础设施观测 provider 的启动和关闭。
type ObservabilityTracerProviderLifecycle struct {
	mu       sync.Mutex
	exporter ObservabilityLifecycleExporter
	status   ObservabilityTracerProviderLifecycleStatus
}

// NewObservabilityTracerProviderLifecycle 创建幂等的 provider 生命周期管理器。
func NewObservabilityTracerProviderLifecycle(config ObservabilityTracerProviderLifecycleConfig) *ObservabilityTracerProviderLifecycle {
	return &ObservabilityTracerProviderLifecycle{
		exporter: config.Exporter,
	}
}

// Initialize 初始化 exporter。重复调用不会重复初始化。
func (l *ObservabilityTracerProviderLifecycle) Initialize(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.status.Initialized {
		return nil
	}
	l.status.Initialized = true

	if l.exporter == nil {
		return nil
	}
	if err := runLifecycleExporter(func() error {
		return l.exporter.Initialize(ctx)
	}); err != nil {
		l.recordFailure(err)
	}
	return nil
}

// Shutdown 关闭 exporter。重复调用不会重复 shutdown，exporter 失败不会影响主流程。
func (l *ObservabilityTracerProviderLifecycle) Shutdown(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.status.Shutdown {
		return nil
	}
	l.status.Shutdown = true

	if l.exporter == nil {
		return nil
	}
	if err := runLifecycleExporter(func() error {
		return l.exporter.Shutdown(ctx)
	}); err != nil {
		l.recordFailure(err)
	}
	return nil
}

// Status 返回生命周期状态快照。
func (l *ObservabilityTracerProviderLifecycle) Status() ObservabilityTracerProviderLifecycleStatus {
	if l == nil {
		return ObservabilityTracerProviderLifecycleStatus{}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	return l.status
}

func (l *ObservabilityTracerProviderLifecycle) recordFailure(err error) {
	l.status.FailureStatus = string(obs.FailureTelemetryExportFailed)
	l.status.FailureMessage = err.Error()
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
