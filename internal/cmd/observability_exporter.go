package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// ObservabilityOTLPProtocol 是应用到唯一 Collector 的 OTLP 传输协议。
type ObservabilityOTLPProtocol string

const (
	ObservabilityOTLPProtocolGRPC         ObservabilityOTLPProtocol = collectorProtocolGRPC
	ObservabilityOTLPProtocolHTTPProtobuf ObservabilityOTLPProtocol = collectorProtocolHTTPProtobuf
)

// ObservabilityOTLPExporterConfigInput 将已校验的运行时选择、resource 和采样策略
// 收敛为 SDK 初始化的最小输入；它不接受任何后端特定 endpoint。
type ObservabilityOTLPExporterConfigInput struct {
	Runtime       ObservabilityRuntimeConfigInput
	Resource      ObservabilityResourceInput
	SamplingRatio float64
}

// ObservabilityOTLPExporterConfig 是不会保留 credential 的低敏 SDK 配置快照。
type ObservabilityOTLPExporterConfig struct {
	Protocol          ObservabilityOTLPProtocol
	Endpoint          string
	Insecure          bool
	Timeout           time.Duration
	SamplingRatio     float64
	HeaderEnvName     string
	CredentialPresent bool
	Resource          ObservabilityResource
}

// ObservabilityOTLPExporter 是同一份 Collector 配置生成的 trace 与 metric provider。
// 它故意不安装全局 provider：调用方必须把这两个 provider 交给 T025 lifecycle，
// 从而与 GoFrame 自动埋点共享唯一的全局安装入口。
type ObservabilityOTLPExporter struct {
	tracerProvider *trace.TracerProvider
	meterProvider  *metric.MeterProvider
	loggerProvider *sdklog.LoggerProvider
	grpcConnection *grpc.ClientConn
}

func (e *ObservabilityOTLPExporter) TracerProvider() *trace.TracerProvider {
	if e == nil {
		return nil
	}
	return e.tracerProvider
}

func (e *ObservabilityOTLPExporter) MeterProvider() *metric.MeterProvider {
	if e == nil {
		return nil
	}
	return e.meterProvider
}

func (e *ObservabilityOTLPExporter) LoggerProvider() *sdklog.LoggerProvider {
	if e == nil {
		return nil
	}
	return e.loggerProvider
}

func (e *ObservabilityOTLPExporter) CompletionLogger() (appobservability.HTTPCompletionLogWriter, error) {
	if e == nil || e.loggerProvider == nil {
		return nil, fmt.Errorf("completion logger is unavailable")
	}
	return appobservability.NewOTLPHTTPCompletionLogWriter(e.loggerProvider.Logger("github.com/ashjazz/Longtermism/internal/observability/http-completion"))
}

// Initialize 满足 lifecycle 的窄接口。SDK provider 与 exporter 已在构造时完成，
// 此处不拨号；实际网络连接延迟到首批信号发送，避免启动阶段被 Collector 短暂故障阻塞。
func (e *ObservabilityOTLPExporter) Initialize(context.Context) error {
	return nil
}

// ForceFlush 让 bundle 作为 lifecycle 的唯一 owner 时同时排空两类信号。
func (e *ObservabilityOTLPExporter) ForceFlush(ctx context.Context) error {
	if e == nil {
		return nil
	}
	var firstErr error
	if e.loggerProvider != nil {
		firstErr = e.loggerProvider.ForceFlush(ctx)
	}
	if e.tracerProvider != nil {
		if err := e.tracerProvider.ForceFlush(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.meterProvider != nil {
		if err := e.meterProvider.ForceFlush(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Shutdown 是 bundle 自己拥有两类 provider 和共享 gRPC 连接时的唯一关闭路径。将
// 它交给 lifecycle 时必须同时设置两个 ExporterOwns*Provider 标志，避免双重关闭。
func (e *ObservabilityOTLPExporter) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	var firstErr error
	// Logs are the final completion fact for a request. Drain them before slower trace/metric
	// exporters can consume the caller's shutdown budget; the shared connection closes last.
	if e.loggerProvider != nil {
		firstErr = e.loggerProvider.Shutdown(ctx)
	}
	if e.tracerProvider != nil {
		if err := e.tracerProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.meterProvider != nil {
		if err := e.meterProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.grpcConnection != nil {
		if err := e.grpcConnection.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BuildObservabilityOTLPExporterConfig 构造离线可验证的 SDK 配置快照。
func BuildObservabilityOTLPExporterConfig(input ObservabilityOTLPExporterConfigInput) (ObservabilityOTLPExporterConfig, error) {
	runtime, err := ResolveObservabilityRuntimeConfig(input.Runtime)
	if err != nil {
		return ObservabilityOTLPExporterConfig{}, err
	}
	return buildObservabilityOTLPExporterConfig(runtime, input.Resource, input.SamplingRatio)
}

// buildObservabilityOTLPExporterConfig 只接收已解析的安全 runtime 快照。Bootstrap 用它
// 保证原始配置（尤其 header source）不会在 resource/exporter 阶段被重复解析。
func buildObservabilityOTLPExporterConfig(runtime ObservabilityRuntimeConfig, resourceInput ObservabilityResourceInput, samplingRatio float64) (ObservabilityOTLPExporterConfig, error) {
	if !runtime.CollectorEnabled {
		return ObservabilityOTLPExporterConfig{}, fmt.Errorf("collector mode is required for OTLP exporter")
	}
	if math.IsNaN(samplingRatio) || samplingRatio < 0 || samplingRatio > 1 {
		return ObservabilityOTLPExporterConfig{}, fmt.Errorf("sampling ratio must be within [0,1]")
	}
	if resourceInput.Environment == "" {
		resourceInput.Environment = runtime.Environment
	} else if !strings.EqualFold(strings.TrimSpace(resourceInput.Environment), strings.TrimSpace(runtime.Environment)) {
		return ObservabilityOTLPExporterConfig{}, fmt.Errorf("resource environment must match observability environment")
	}
	observabilityResource, err := BuildObservabilityResource(resourceInput)
	if err != nil {
		return ObservabilityOTLPExporterConfig{}, err
	}

	return ObservabilityOTLPExporterConfig{
		Protocol:          ObservabilityOTLPProtocol(runtime.Collector.Protocol),
		Endpoint:          runtime.Collector.Endpoint,
		Insecure:          runtime.Collector.Insecure,
		Timeout:           runtime.Collector.Timeout,
		SamplingRatio:     samplingRatio,
		HeaderEnvName:     runtime.Collector.HeaderEnvName,
		CredentialPresent: runtime.Collector.CredentialPresent,
		Resource:          ObservabilityResource{Attributes: cloneResourceAttributes(observabilityResource.Attributes)},
	}, nil
}

// NewObservabilityOTLPExporter 创建仅指向 Collector 的两套 SDK 信号管道。credential
// 在这里从原始输入即时解析并交给 SDK，之后只保留 SDK 内部状态，绝不进入配置快照。
func NewObservabilityOTLPExporter(ctx context.Context, input ObservabilityOTLPExporterConfigInput) (*ObservabilityOTLPExporter, error) {
	config, err := BuildObservabilityOTLPExporterConfig(input)
	if err != nil {
		return nil, err
	}
	return newObservabilityOTLPExporterFromConfig(ctx, config, input.Runtime.Collector.HeaderValue)
}

func newObservabilityOTLPExporterFromConfig(ctx context.Context, config ObservabilityOTLPExporterConfig, headerValue string) (*ObservabilityOTLPExporter, error) {
	headers, err := parseOTLPHeaders(headerValue)
	if err != nil {
		return nil, err
	}
	sdkResource := newOTLPResource(config.Resource)
	traceExporter, metricExporter, logExporter, grpcConnection, err := newOTLPExporters(ctx, config, headers)
	if err != nil {
		return nil, err
	}

	provider := &ObservabilityOTLPExporter{
		grpcConnection: grpcConnection,
		tracerProvider: trace.NewTracerProvider(
			trace.WithResource(sdkResource),
			trace.WithSampler(newObservabilitySampler(config.SamplingRatio)),
			trace.WithBatcher(traceExporter),
		),
		meterProvider: metric.NewMeterProvider(
			metric.WithResource(sdkResource),
			metric.WithReader(metric.NewPeriodicReader(metricExporter, metric.WithTimeout(config.Timeout))),
		),
		loggerProvider: sdklog.NewLoggerProvider(
			sdklog.WithResource(sdkResource),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		),
	}
	return provider, nil
}

// newObservabilitySampler 对公网可控的 remote sampled bit 保持本地预算的最终裁决。
// 默认 ParentBased 会让 remote `-00` 永远不采样、remote `-01` 永远采样，二者都可
// 被攻击者用来规避或耗尽观测预算；因此两条 remote 分支都显式使用本地 ratio。
func newObservabilitySampler(ratio float64) trace.Sampler {
	localBudget := trace.TraceIDRatioBased(ratio)
	return trace.ParentBased(localBudget,
		trace.WithRemoteParentSampled(trace.TraceIDRatioBased(ratio)),
		trace.WithRemoteParentNotSampled(trace.TraceIDRatioBased(ratio)),
	)
}

func newOTLPResource(input ObservabilityResource) *resource.Resource {
	attributes := make([]attribute.KeyValue, 0, len(input.Attributes))
	for key, value := range input.Attributes {
		attributes = append(attributes, attribute.String(key, value))
	}
	return resource.NewWithAttributes("", attributes...)
}

func newOTLPExporters(ctx context.Context, config ObservabilityOTLPExporterConfig, headers map[string]string) (trace.SpanExporter, metric.Exporter, sdklog.Exporter, *grpc.ClientConn, error) {
	switch config.Protocol {
	case ObservabilityOTLPProtocolGRPC:
		connection, err := newDirectOTLPGRPCConnection(config)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		traceExporter, err := otlptracegrpc.New(ctx, grpcTraceOptions(config, headers, connection)...)
		if err != nil {
			_ = connection.Close()
			return nil, nil, nil, nil, err
		}
		metricExporter, err := otlpmetricgrpc.New(ctx, grpcMetricOptions(config, headers, connection)...)
		if err != nil {
			_ = traceExporter.Shutdown(ctx)
			_ = connection.Close()
			return nil, nil, nil, nil, err
		}
		logExporter, err := otlploggrpc.New(ctx, grpcLogOptions(config, headers, connection)...)
		if err != nil {
			_ = metricExporter.Shutdown(ctx)
			_ = traceExporter.Shutdown(ctx)
			_ = connection.Close()
			return nil, nil, nil, nil, err
		}
		return traceExporter, metricExporter, logExporter, connection, nil
	case ObservabilityOTLPProtocolHTTPProtobuf:
		endpoint, err := url.Parse(config.Endpoint)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("collector endpoint is invalid")
		}
		traceExporter, err := otlptracehttp.New(ctx, httpTraceOptions(config, endpoint, headers)...)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		metricExporter, err := otlpmetrichttp.New(ctx, httpMetricOptions(config, endpoint, headers)...)
		if err != nil {
			_ = traceExporter.Shutdown(ctx)
			return nil, nil, nil, nil, err
		}
		logExporter, err := otlploghttp.New(ctx, httpLogOptions(config, endpoint, headers)...)
		if err != nil {
			_ = metricExporter.Shutdown(ctx)
			_ = traceExporter.Shutdown(ctx)
			return nil, nil, nil, nil, err
		}
		return traceExporter, metricExporter, logExporter, nil, nil
	default:
		return nil, nil, nil, nil, fmt.Errorf("collector protocol is unsupported")
	}
}

func newDirectOTLPGRPCConnection(config ObservabilityOTLPExporterConfig) (*grpc.ClientConn, error) {
	transportCredentials := credentials.NewTLS(&tls.Config{})
	if config.Insecure {
		transportCredentials = insecure.NewCredentials()
	}
	// 自建 ClientConn 让 TLS 与 proxy 策略不再受 OTEL_EXPORTER_OTLP_* 或进程环境影响。
	// exporter 不会关闭 WithGRPCConn 注入的连接，因此由 bundle Shutdown 统一回收。
	return grpc.NewClient(config.Endpoint, grpc.WithTransportCredentials(transportCredentials), grpc.WithNoProxy())
}

func grpcTraceOptions(config ObservabilityOTLPExporterConfig, headers map[string]string, connection *grpc.ClientConn) []otlptracegrpc.Option {
	return []otlptracegrpc.Option{otlptracegrpc.WithGRPCConn(connection), otlptracegrpc.WithTimeout(config.Timeout), otlptracegrpc.WithHeaders(headers)}
}

func grpcMetricOptions(config ObservabilityOTLPExporterConfig, headers map[string]string, connection *grpc.ClientConn) []otlpmetricgrpc.Option {
	return []otlpmetricgrpc.Option{otlpmetricgrpc.WithGRPCConn(connection), otlpmetricgrpc.WithTimeout(config.Timeout), otlpmetricgrpc.WithHeaders(headers)}
}

func grpcLogOptions(config ObservabilityOTLPExporterConfig, headers map[string]string, connection *grpc.ClientConn) []otlploggrpc.Option {
	return []otlploggrpc.Option{otlploggrpc.WithGRPCConn(connection), otlploggrpc.WithTimeout(config.Timeout), otlploggrpc.WithHeaders(headers)}
}

func httpTraceOptions(config ObservabilityOTLPExporterConfig, endpoint *url.URL, headers map[string]string) []otlptracehttp.Option {
	options := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint.Host), otlptracehttp.WithURLPath("/v1/traces"), otlptracehttp.WithTimeout(config.Timeout), otlptracehttp.WithHeaders(headers), otlptracehttp.WithProxy(noOTLPHTTPProxy)}
	if config.Insecure {
		options = append(options, otlptracehttp.WithInsecure())
	} else {
		options = append(options, otlptracehttp.WithTLSClientConfig(&tls.Config{}))
	}
	return options
}

func httpMetricOptions(config ObservabilityOTLPExporterConfig, endpoint *url.URL, headers map[string]string) []otlpmetrichttp.Option {
	options := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(endpoint.Host), otlpmetrichttp.WithURLPath("/v1/metrics"), otlpmetrichttp.WithTimeout(config.Timeout), otlpmetrichttp.WithHeaders(headers), otlpmetrichttp.WithProxy(noOTLPHTTPProxy)}
	if config.Insecure {
		options = append(options, otlpmetrichttp.WithInsecure())
	} else {
		options = append(options, otlpmetrichttp.WithTLSClientConfig(&tls.Config{}))
	}
	return options
}

func httpLogOptions(config ObservabilityOTLPExporterConfig, endpoint *url.URL, headers map[string]string) []otlploghttp.Option {
	options := []otlploghttp.Option{otlploghttp.WithEndpoint(endpoint.Host), otlploghttp.WithURLPath("/v1/logs"), otlploghttp.WithTimeout(config.Timeout), otlploghttp.WithHeaders(headers), otlploghttp.WithProxy(noOTLPHTTPProxy)}
	if config.Insecure {
		options = append(options, otlploghttp.WithInsecure())
	} else {
		options = append(options, otlploghttp.WithTLSClientConfig(&tls.Config{}))
	}
	return options
}

// noOTLPHTTPProxy 覆盖 HTTP_PROXY/HTTPS_PROXY，确保应用的唯一出口仍是已验证的
// Collector。代理如需支持，必须成为显式、可审计的运行时配置。
func noOTLPHTTPProxy(*http.Request) (*url.URL, error) {
	return nil, nil
}

func parseOTLPHeaders(value string) (map[string]string, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]string{}, nil
	}
	headers := make(map[string]string)
	for _, pair := range strings.Split(value, ",") {
		key, rawValue, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found || !isValidOTLPHeaderName(key) || strings.ContainsAny(rawValue, "\r\n\x00") {
			return nil, fmt.Errorf("OTLP header is invalid")
		}
		// OTLP header 格式使用百分号编码；PathUnescape 不会把普通 `+` 误改为空格，
		// 否则常见的 bearer/API key 会在发送前被悄悄破坏。
		decoded, err := url.PathUnescape(rawValue)
		if err != nil || decoded == "" || strings.ContainsAny(decoded, "\r\n\x00") {
			return nil, fmt.Errorf("OTLP header is invalid")
		}
		normalizedKey := strings.ToLower(key)
		if _, exists := headers[normalizedKey]; exists {
			return nil, fmt.Errorf("OTLP header is invalid")
		}
		headers[normalizedKey] = decoded
	}
	return headers, nil
}

func isValidOTLPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			continue
		}
		return false
	}
	return true
}
