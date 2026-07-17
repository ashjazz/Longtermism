package cmd

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	metricAPI "go.opentelemetry.io/otel/metric"
	metricSDK "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace"
	traceAPI "go.opentelemetry.io/otel/trace"
)

func TestBuildObservabilityBootstrapNormalizesRuntimeOwnership(t *testing.T) {
	// 这组契约防止 enabled、mode、signals 与 smoke 分别在路由和 exporter 层解释，
	// 否则同一份配置可能在不同启动路径中产生不同的网络副作用。
	// 测试不得永久替换进程的 OTel provider，避免影响同包的其它 lifecycle 契约。
	replaceOTelGlobalProviderInstallerForTest(t)
	tests := []struct {
		name              string
		input             ObservabilityBootstrapInput
		wantMode          ObservabilityRuntimeMode
		wantCollectorCall int
		wantProviderCall  int
		wantMiddleware    int
		wantSmokeEnabled  bool
		wantErr           bool
	}{
		{
			name: "disabled configuration normalizes to noop without providers or network",
			input: ObservabilityBootstrapInput{
				Enabled: false,
				Runtime: ObservabilityRuntimeConfigInput{Environment: "test"},
				Signals: ObservabilitySignalPolicy{TracesEnabled: true, MetricsEnabled: true},
			},
			wantMode: ObservabilityRuntimeModeNoop,
		},
		{
			name: "disabled configuration rejects an explicit non noop mode",
			input: ObservabilityBootstrapInput{
				Enabled: false,
				Runtime: ObservabilityRuntimeConfigInput{Mode: ObservabilityRuntimeModeCollector, Environment: "test"},
			},
			wantErr: true,
		},
		{
			name: "local mode stays offline even when both signals are enabled",
			input: ObservabilityBootstrapInput{
				Enabled: true,
				Runtime: ObservabilityRuntimeConfigInput{Mode: ObservabilityRuntimeModeLocal, Environment: "test"},
				Signals: ObservabilitySignalPolicy{TracesEnabled: true, MetricsEnabled: true},
			},
			wantMode: ObservabilityRuntimeModeLocal,
		},
		{
			name: "signals are the only provider creation gate and smoke does not override them",
			input: ObservabilityBootstrapInput{
				Enabled: true,
				Runtime: ObservabilityRuntimeConfigInput{
					Mode:        ObservabilityRuntimeModeCollector,
					Environment: "test",
					Collector: ObservabilityCollectorConfigInput{
						Endpoint: "collector.example.test:4317",
						Timeout:  "5s",
						Insecure: true,
					},
				},
				SmokeEnabled: true,
			},
			wantMode:         ObservabilityRuntimeModeCollector,
			wantSmokeEnabled: true,
		},
		{
			name: "trace only signal configuration fails instead of creating an invalid half provider pair",
			input: ObservabilityBootstrapInput{
				Enabled: true,
				Runtime: ObservabilityRuntimeConfigInput{Mode: ObservabilityRuntimeModeLocal, Environment: "test"},
				Signals: ObservabilitySignalPolicy{TracesEnabled: true},
			},
			wantErr: true,
		},
		{
			name: "metric only signal configuration fails instead of creating an invalid half provider pair",
			input: ObservabilityBootstrapInput{
				Enabled: true,
				Runtime: ObservabilityRuntimeConfigInput{Mode: ObservabilityRuntimeModeLocal, Environment: "test"},
				Signals: ObservabilitySignalPolicy{MetricsEnabled: true},
			},
			wantErr: true,
		},
		{
			name: "collector creates the paired providers once and smoke only controls its route gate",
			input: ObservabilityBootstrapInput{
				Enabled: true,
				Runtime: ObservabilityRuntimeConfigInput{
					Mode:        ObservabilityRuntimeModeCollector,
					Environment: "test",
					Collector: ObservabilityCollectorConfigInput{
						Endpoint: "collector.example.test:4317",
						Timeout:  "5s",
						Insecure: true,
					},
				},
				Signals:      ObservabilitySignalPolicy{TracesEnabled: true, MetricsEnabled: true},
				SmokeEnabled: true,
			},
			wantMode:          ObservabilityRuntimeModeCollector,
			wantCollectorCall: 1,
			wantProviderCall:  1,
			wantMiddleware:    1,
			wantSmokeEnabled:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dependencies := newObservabilityBootstrapDependenciesStub()
			bootstrap, err := BuildObservabilityBootstrap(context.Background(), tt.input, dependencies.dependencies())
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildObservabilityBootstrap() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if bootstrap.Runtime.Mode != tt.wantMode {
				t.Fatalf("Runtime.Mode = %q, want %q", bootstrap.Runtime.Mode, tt.wantMode)
			}
			if dependencies.collectorCalls != tt.wantCollectorCall {
				t.Fatalf("collector builder calls = %d, want %d", dependencies.collectorCalls, tt.wantCollectorCall)
			}
			if dependencies.providerCalls != tt.wantProviderCall {
				t.Fatalf("provider builder calls = %d, want %d", dependencies.providerCalls, tt.wantProviderCall)
			}
			if dependencies.middlewareCalls != tt.wantMiddleware {
				t.Fatalf("middleware builder calls = %d, want %d", dependencies.middlewareCalls, tt.wantMiddleware)
			}
			if bootstrap.InfraSmokeEnabled != tt.wantSmokeEnabled {
				t.Fatalf("InfraSmokeEnabled = %v, want %v", bootstrap.InfraSmokeEnabled, tt.wantSmokeEnabled)
			}
		})
	}
}

func TestBuildObservabilityBootstrapFailsFastBeforeGlobalInstallation(t *testing.T) {
	dependencies := newObservabilityBootstrapDependenciesStub()
	dependencies.collectorErr = errors.New("synthetic collector initialization failure")
	installer := replaceOTelGlobalProviderInstallerForTest(t)

	_, err := BuildObservabilityBootstrap(context.Background(), collectorBootstrapInput(), dependencies.dependencies())
	if err == nil {
		t.Fatal("BuildObservabilityBootstrap() error = nil, want collector initialization failure")
	}
	if dependencies.collectorCalls != 1 || dependencies.providerCalls != 0 || installer.calls != 0 {
		t.Fatal("failed collector initialization created providers or installed global state")
	}
}

func TestBuildObservabilityBootstrapReusesOneProviderAndMiddleware(t *testing.T) {
	dependencies := newObservabilityBootstrapDependenciesStub()
	installer := replaceOTelGlobalProviderInstallerForTest(t)
	resetObservabilityBootstrapForTest(t)
	input := collectorBootstrapInput()
	bootstrapDependencies := dependencies.dependencies()
	bootstrapDependencies.state = nil

	first, err := BuildObservabilityBootstrap(context.Background(), input, bootstrapDependencies)
	if err != nil {
		t.Fatalf("first BuildObservabilityBootstrap() error = %v", err)
	}
	second, err := BuildObservabilityBootstrap(context.Background(), input, bootstrapDependencies)
	if err != nil {
		t.Fatalf("second BuildObservabilityBootstrap() error = %v", err)
	}
	if first != second || dependencies.collectorCalls != 1 || dependencies.providerCalls != 1 || dependencies.middlewareCalls != 1 || installer.calls != 1 {
		t.Fatal("repeated bootstrap did not reuse the sole provider and middleware assembly")
	}
	if installer.tracer != dependencies.tracerProvider || installer.meter != dependencies.meterProvider {
		t.Fatal("bootstrap did not install the exact paired trace and meter providers globally")
	}
}

func TestObservabilityBootstrapFlushAndShutdownHonorDeadline(t *testing.T) {
	dependencies := newObservabilityBootstrapDependenciesStub()
	bootstrap, err := BuildObservabilityBootstrap(context.Background(), collectorBootstrapInput(), dependencies.dependencies())
	if err != nil {
		t.Fatalf("BuildObservabilityBootstrap() error = %v", err)
	}
	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if err := bootstrap.Flush(deadline); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if err := bootstrap.Shutdown(deadline); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if !dependencies.flushSawDeadline || !dependencies.shutdownSawDeadline || !dependencies.flushSawCancellation || !dependencies.shutdownSawCancellation {
		t.Fatal("flush or shutdown did not receive the caller's expired deadline")
	}
}

func TestObservabilityIngressTrustPolicyPreventsRemoteSamplingAndTracestateRelay(t *testing.T) {
	// 公网入口的 traceparent/tracestate 都是攻击者可控输入。远端 sampled bit 不能让
	// ParentBased sampler 跳过本地预算，未经审计的 tracestate 也不能自动带到第三方。
	propagator := NewObservabilityIngressPropagator(ObservabilityIngressTrustPolicy{})
	inbound := propagationCarrier{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"tracestate":  "vendor=unreviewed",
	}
	ctx := propagator.Extract(context.Background(), inbound)
	spanContext := traceAPI.SpanContextFromContext(ctx)
	if spanContext.IsSampled() {
		t.Fatal("untrusted remote sampled bit bypassed local sampling budget")
	}
	provider := trace.NewTracerProvider(trace.WithSampler(newObservabilitySampler(0)))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	_, localSpan := provider.Tracer("t036a-ingress").Start(ctx, "local-budget")
	if localSpan.SpanContext().IsSampled() {
		t.Fatal("untrusted remote sampled bit bypassed the zero local sampling budget")
	}
	localSpan.End()
	fullBudgetProvider := trace.NewTracerProvider(trace.WithSampler(newObservabilitySampler(1)))
	defer func() { _ = fullBudgetProvider.Shutdown(context.Background()) }()
	_, fullBudgetSpan := fullBudgetProvider.Tracer("t036a-ingress").Start(ctx, "local-full-budget")
	if !fullBudgetSpan.SpanContext().IsSampled() {
		t.Fatal("untrusted remote not sampled bit prevented the full local sampling budget")
	}
	fullBudgetSpan.End()
	outbound := propagationCarrier{}
	propagator.Inject(ctx, outbound)
	if outbound.Get("tracestate") != "" {
		t.Fatal("untrusted tracestate was relayed to a third party")
	}

	trusted := NewObservabilityIngressPropagator(ObservabilityIngressTrustPolicy{TrustedRemote: true})
	trustedContext := trusted.Extract(context.Background(), inbound)
	if !traceAPI.SpanContextFromContext(trustedContext).IsSampled() {
		t.Fatal("trusted service traceparent did not retain its sampling decision")
	}
	trustedOutbound := propagationCarrier{}
	trusted.Inject(trustedContext, trustedOutbound)
	if trustedOutbound.Get("tracestate") != "vendor=unreviewed" {
		t.Fatal("trusted service tracestate was not relayed according to the explicit trust policy")
	}
}

func collectorBootstrapInput() ObservabilityBootstrapInput {
	return ObservabilityBootstrapInput{
		Enabled: true,
		Runtime: ObservabilityRuntimeConfigInput{
			Mode:        ObservabilityRuntimeModeCollector,
			Environment: "test",
			Collector: ObservabilityCollectorConfigInput{
				Endpoint: "collector.example.test:4317",
				Timeout:  "5s",
				Insecure: true,
			},
		},
		Resource: ObservabilityResourceInput{ServiceName: "longtermism", Environment: "test"},
		Signals:  ObservabilitySignalPolicy{TracesEnabled: true, MetricsEnabled: true},
	}
}

type observabilityBootstrapDependenciesStub struct {
	collectorCalls          int
	providerCalls           int
	middlewareCalls         int
	collectorErr            error
	flushSawDeadline        bool
	shutdownSawDeadline     bool
	flushSawCancellation    bool
	shutdownSawCancellation bool
	lifecycleExporter       *bootstrapLifecycleExporter
	tracerProvider          traceAPI.TracerProvider
	meterProvider           metricAPI.MeterProvider
}

func newObservabilityBootstrapDependenciesStub() *observabilityBootstrapDependenciesStub {
	return &observabilityBootstrapDependenciesStub{}
}

func (s *observabilityBootstrapDependenciesStub) dependencies() ObservabilityBootstrapDependencies {
	return ObservabilityBootstrapDependencies{
		state: &observabilityBootstrapState{},
		BuildCollector: func(context.Context, ObservabilityOTLPExporterConfig) (ObservabilityLifecycleExporter, error) {
			s.collectorCalls++
			if s.collectorErr != nil {
				return nil, s.collectorErr
			}
			s.lifecycleExporter = &bootstrapLifecycleExporter{stub: s}
			return s.lifecycleExporter, nil
		},
		BuildProviders: func(ObservabilityLifecycleExporter) (traceAPI.TracerProvider, metricAPI.MeterProvider, error) {
			s.providerCalls++
			s.tracerProvider = trace.NewTracerProvider()
			s.meterProvider = metricSDK.NewMeterProvider()
			return s.tracerProvider, s.meterProvider, nil
		},
		BuildMiddleware: func() func(http.Handler) http.Handler {
			s.middlewareCalls++
			return func(next http.Handler) http.Handler { return next }
		},
	}
}

type bootstrapLifecycleExporter struct {
	stub *observabilityBootstrapDependenciesStub
}

func (s *bootstrapLifecycleExporter) Initialize(context.Context) error { return nil }

func (s *bootstrapLifecycleExporter) ForceFlush(ctx context.Context) error {
	_, s.stub.flushSawDeadline = ctx.Deadline()
	s.stub.flushSawCancellation = ctx.Err() != nil
	return nil
}

func (s *bootstrapLifecycleExporter) Shutdown(ctx context.Context) error {
	_, s.stub.shutdownSawDeadline = ctx.Deadline()
	s.stub.shutdownSawCancellation = ctx.Err() != nil
	return nil
}

type observabilityGlobalInstallSpy struct {
	calls  int
	tracer traceAPI.TracerProvider
	meter  metricAPI.MeterProvider
}

func replaceOTelGlobalProviderInstallerForTest(t *testing.T) *observabilityGlobalInstallSpy {
	t.Helper()
	originalInstaller := installOTelGlobalProviders
	processGlobalProviders.mu.Lock()
	originalInstalled := processGlobalProviders.installed
	processGlobalProviders.installed = false
	processGlobalProviders.mu.Unlock()
	installer := &observabilityGlobalInstallSpy{}
	installOTelGlobalProviders = func(tracer traceAPI.TracerProvider, meter metricAPI.MeterProvider) {
		installer.calls++
		installer.tracer = tracer
		installer.meter = meter
	}
	t.Cleanup(func() {
		installOTelGlobalProviders = originalInstaller
		processGlobalProviders.mu.Lock()
		processGlobalProviders.installed = originalInstalled
		processGlobalProviders.mu.Unlock()
	})
	return installer
}

func resetObservabilityBootstrapForTest(t *testing.T) {
	t.Helper()
	processObservabilityBootstrap.mu.Lock()
	original := processObservabilityBootstrap.bootstrap
	processObservabilityBootstrap.bootstrap = nil
	processObservabilityBootstrap.mu.Unlock()
	t.Cleanup(func() {
		processObservabilityBootstrap.mu.Lock()
		processObservabilityBootstrap.bootstrap = original
		processObservabilityBootstrap.mu.Unlock()
	})
}

type propagationCarrier map[string]string

func (c propagationCarrier) Get(key string) string { return c[key] }

func (c propagationCarrier) Set(key string, value string) { c[key] = value }

func (c propagationCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}
