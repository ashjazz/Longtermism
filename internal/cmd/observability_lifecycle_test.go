package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel"
	metricSDK "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestObservabilityTracerProviderLifecycle(t *testing.T) {
	t.Run("initialization is idempotent", func(t *testing.T) {
		exporter := &lifecycleExporterStub{}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: exporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("first Initialize() error = %v", err)
		}
		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("second Initialize() error = %v", err)
		}

		if exporter.initCalls != 1 {
			t.Fatalf("exporter init calls = %d, want 1", exporter.initCalls)
		}
		if !lifecycle.Status().Initialized {
			t.Fatalf("Initialized = false, want true")
		}
	})

	t.Run("shutdown is idempotent", func(t *testing.T) {
		exporter := &lifecycleExporterStub{}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: exporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}
		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatalf("first Shutdown() error = %v", err)
		}
		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatalf("second Shutdown() error = %v", err)
		}

		if exporter.shutdownCalls != 1 {
			t.Fatalf("exporter shutdown calls = %d, want 1", exporter.shutdownCalls)
		}
		if !lifecycle.Status().Shutdown {
			t.Fatalf("Shutdown = false, want true")
		}
	})

	t.Run("exporter failure is captured without failing lifecycle", func(t *testing.T) {
		exporter := &lifecycleExporterStub{
			shutdownErr: errors.New("collector unavailable"),
		}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: exporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}
		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() error = %v, want exporter failure protected", err)
		}

		if lifecycle.Status().FailureStatus != string(obs.FailureTelemetryExportFailed) {
			t.Fatalf("FailureStatus = %q, want %q", lifecycle.Status().FailureStatus, obs.FailureTelemetryExportFailed)
		}
		if lifecycle.Status().FailureMessage != "collector unavailable" {
			t.Fatalf("FailureMessage = %q, want collector unavailable", lifecycle.Status().FailureMessage)
		}
	})
}

func TestObservabilityTracerProviderLifecycleFlushContract(t *testing.T) {
	t.Run("one lifecycle reuses one OTel provider and flushes its test exporter", func(t *testing.T) {
		spanExporter := tracetest.NewInMemoryExporter()
		provider := trace.NewTracerProvider(trace.WithSyncer(spanExporter))
		lifecycleExporter := &otelLifecycleExporter{provider: provider}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: lifecycleExporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatal("first lifecycle initialization failed")
		}
		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatal("second lifecycle initialization failed")
		}
		if lifecycleExporter.initCalls != 1 {
			t.Fatalf("provider initialization calls = %d, want 1", lifecycleExporter.initCalls)
		}

		_, span := provider.Tracer("t013-lifecycle").Start(context.Background(), "flush-contract")
		span.End()
		if err := lifecycle.Flush(context.Background()); err != nil {
			t.Fatal("Flush() returned an unexpected error")
		}
		if got := len(spanExporter.GetSpans()); got != 1 {
			t.Fatalf("OTel test exporter span count = %d, want 1", got)
		}

		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatal("Shutdown() returned an unexpected error")
		}
	})

	t.Run("flush timeout records telemetry failure without panicking", func(t *testing.T) {
		exporter := &lifecycleExporterStub{
			flushErr: context.DeadlineExceeded,
		}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: exporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatal("Initialize() returned an unexpected error")
		}
		if err := lifecycle.Flush(context.Background()); err != nil {
			t.Fatal("Flush() must protect the application lifecycle from exporter timeout")
		}

		status := lifecycle.Status()
		if status.FailureStatus != string(obs.FailureTelemetryExportFailed) {
			t.Fatalf("FailureStatus = %q, want telemetry export failure", status.FailureStatus)
		}
		if exporter.flushCalls != 1 {
			t.Fatalf("exporter flush calls = %d, want 1", exporter.flushCalls)
		}
	})

	t.Run("no exporter mode remains safe through initialize flush and shutdown", func(t *testing.T) {
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatal("Initialize() in no exporter mode returned an error")
		}
		if err := lifecycle.Flush(context.Background()); err != nil {
			t.Fatal("Flush() in no exporter mode returned an error")
		}
		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatal("Shutdown() in no exporter mode returned an error")
		}
		if status := lifecycle.Status(); status.FailureStatus != "" {
			t.Fatalf("FailureStatus = %q, want empty in no exporter mode", status.FailureStatus)
		}
	})
}

func TestObservabilityTracerProviderLifecycleInstallsOneGlobalTraceAndMeterProvider(t *testing.T) {
	if os.Getenv("T013_GLOBAL_PROVIDER_HELPER") != "1" {
		// OTel 默认全局代理在首次 Set 后会永久委托给该 provider，不能在同一进程中
		// 可靠恢复。用子进程隔离这条契约，避免后续测试指向已经 shutdown 的 provider。
		command := exec.Command(os.Args[0], "-test.run=^TestObservabilityTracerProviderLifecycleInstallsOneGlobalTraceAndMeterProvider$")
		command.Env = append(os.Environ(), "T013_GLOBAL_PROVIDER_HELPER=1")
		if err := command.Run(); err != nil {
			t.Fatal("global provider lifecycle helper process failed")
		}
		return
	}

	primarySpanExporter := tracetest.NewInMemoryExporter()
	primaryTracerProvider := trace.NewTracerProvider(trace.WithSyncer(primarySpanExporter))
	primaryMeterProvider := metricSDK.NewMeterProvider()
	primaryLifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
		Exporter:       &otelLifecycleExporter{provider: primaryTracerProvider},
		TracerProvider: primaryTracerProvider,
		MeterProvider:  primaryMeterProvider,
	})

	if err := primaryLifecycle.Initialize(context.Background()); err != nil {
		t.Fatal("primary lifecycle initialization failed")
	}
	if got := otel.GetTracerProvider(); got != primaryTracerProvider {
		t.Fatal("primary lifecycle did not install its trace provider globally")
	}
	if got := otel.GetMeterProvider(); got != primaryMeterProvider {
		t.Fatal("primary lifecycle did not install its meter provider globally")
	}

	secondaryTracerProvider := trace.NewTracerProvider()
	secondaryMeterProvider := metricSDK.NewMeterProvider()
	secondaryLifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
		TracerProvider: secondaryTracerProvider,
		MeterProvider:  secondaryMeterProvider,
	})
	if err := secondaryLifecycle.Initialize(context.Background()); err != nil {
		t.Fatal("second lifecycle initialization must reuse or safely reject the existing global providers")
	}
	if got := otel.GetTracerProvider(); got != primaryTracerProvider {
		t.Fatal("second lifecycle replaced the process global trace provider")
	}
	if got := otel.GetMeterProvider(); got != primaryMeterProvider {
		t.Fatal("second lifecycle replaced the process global meter provider")
	}

	if err := primaryLifecycle.Shutdown(context.Background()); err != nil {
		t.Fatal("primary lifecycle shutdown failed")
	}
	if err := primaryLifecycle.Shutdown(context.Background()); err != nil {
		t.Fatal("repeated primary lifecycle shutdown failed")
	}
}

type lifecycleExporterStub struct {
	initCalls     int
	shutdownCalls int
	shutdownErr   error
	flushCalls    int
	flushErr      error
}

func (s *lifecycleExporterStub) Initialize(_ context.Context) error {
	s.initCalls++
	return nil
}

func (s *lifecycleExporterStub) Shutdown(_ context.Context) error {
	s.shutdownCalls++
	return s.shutdownErr
}

func (s *lifecycleExporterStub) ForceFlush(_ context.Context) error {
	s.flushCalls++
	return s.flushErr
}

// otelLifecycleExporter 让 lifecycle 契约直接使用 SDK 的内存 exporter，避免测试通过
// 网络或真实 Collector 证明 flush。它只适合作为测试替身，不承担全局 provider 装配。
type otelLifecycleExporter struct {
	provider  *trace.TracerProvider
	initCalls int
}

func (s *otelLifecycleExporter) Initialize(_ context.Context) error {
	s.initCalls++
	return nil
}

func (s *otelLifecycleExporter) ForceFlush(ctx context.Context) error {
	return s.provider.ForceFlush(ctx)
}

func (s *otelLifecycleExporter) Shutdown(ctx context.Context) error {
	return s.provider.Shutdown(ctx)
}
