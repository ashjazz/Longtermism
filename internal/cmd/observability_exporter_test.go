package cmd

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestNewObservabilityOTLPExporterBuildsSharedTraceAndMetricProvidersWithoutInstallingGlobals(t *testing.T) {
	// 真实 Collector 尚未启动时，装配也必须只构造 SDK provider；全局安装权仍由
	// T025 lifecycle 串行管理，避免 GoFrame 与应用初始化入口争抢全局对象。
	globalTracer := otel.GetTracerProvider()
	globalMeter := otel.GetMeterProvider()

	for _, input := range []struct {
		name     string
		protocol string
		endpoint string
		insecure bool
	}{
		{name: "gRPC", protocol: "grpc", endpoint: "collector.example.test:4317", insecure: true},
		{name: "HTTP protobuf", protocol: "http_protobuf", endpoint: "https://collector.example.test:4318"},
	} {
		t.Run(input.name, func(t *testing.T) {
			exporter, err := NewObservabilityOTLPExporter(context.Background(), ObservabilityOTLPExporterConfigInput{
				Runtime: ObservabilityRuntimeConfigInput{
					Mode:        ObservabilityRuntimeModeCollector,
					Environment: "test",
					Collector: ObservabilityCollectorConfigInput{
						Endpoint: input.endpoint,
						Protocol: input.protocol,
						Timeout:  "5s",
						Insecure: input.insecure,
					},
				},
				Resource:      ObservabilityResourceInput{ServiceName: "longtermism", Environment: "test"},
				SamplingRatio: 1,
			})
			if err != nil {
				t.Fatalf("NewObservabilityOTLPExporter() error = %v", err)
			}
			t.Cleanup(func() {
				// 该用例只验证离线构造；不存在的 endpoint 在关闭时允许报告导出失败，
				// 但必须使用已取消 context，避免测试为了网络重试而等待超时。
				shutdownContext, cancel := context.WithCancel(context.Background())
				cancel()
				_ = exporter.Shutdown(shutdownContext)
			})

			if exporter.TracerProvider() == nil || exporter.MeterProvider() == nil {
				t.Fatal("OTLP exporter did not create both signal providers")
			}
			if err := exporter.Initialize(context.Background()); err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}
			if otel.GetTracerProvider() != globalTracer || otel.GetMeterProvider() != globalMeter {
				t.Fatal("OTLP exporter initializer must not install competing global providers")
			}
		})
	}
}

func TestBuildObservabilityOTLPExporterConfigRejectsNonCollectorAndMismatchedResource(t *testing.T) {
	tests := []struct {
		name  string
		input ObservabilityOTLPExporterConfigInput
	}{
		{
			name: "local mode has no network exporter",
			input: ObservabilityOTLPExporterConfigInput{
				Runtime: ObservabilityRuntimeConfigInput{Mode: ObservabilityRuntimeModeLocal},
			},
		},
		{
			name: "resource environment cannot contradict runtime environment",
			input: ObservabilityOTLPExporterConfigInput{
				Runtime: ObservabilityRuntimeConfigInput{
					Mode:        ObservabilityRuntimeModeCollector,
					Environment: "staging",
					Collector:   ObservabilityCollectorConfigInput{Endpoint: "collector.example.test:4317", Timeout: "5s"},
				},
				Resource: ObservabilityResourceInput{Environment: "production"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BuildObservabilityOTLPExporterConfig(tt.input); err == nil {
				t.Fatal("BuildObservabilityOTLPExporterConfig() error = nil, want rejection")
			}
		})
	}
}

func TestNewObservabilityOTLPExporterRejectsUnsafeHeaderWithoutEcho(t *testing.T) {
	const rawHeader = "authorization=Bearer%0D%0At026-injection"
	_, err := NewObservabilityOTLPExporter(context.Background(), ObservabilityOTLPExporterConfigInput{
		Runtime: ObservabilityRuntimeConfigInput{
			Mode: ObservabilityRuntimeModeCollector,
			Collector: ObservabilityCollectorConfigInput{
				Endpoint:    "collector.example.test:4317",
				Timeout:     "5s",
				HeaderValue: rawHeader,
			},
		},
	})
	if err == nil {
		t.Fatal("NewObservabilityOTLPExporter() error = nil, want unsafe header rejection")
	}
	if strings.Contains(err.Error(), rawHeader) {
		t.Fatal("exporter initialization error reflected the raw header")
	}
}

func TestResolveObservabilityRuntimeConfigRejectsAmbiguousHTTPTransport(t *testing.T) {
	tests := []struct {
		name  string
		input ObservabilityRuntimeConfigInput
	}{
		{
			name: "production HTTP endpoint cannot bypass insecure authorization",
			input: ObservabilityRuntimeConfigInput{
				Mode:        ObservabilityRuntimeModeCollector,
				Environment: "production",
				Collector:   ObservabilityCollectorConfigInput{Endpoint: "http://collector.example.test:4318", Protocol: "http_protobuf", Timeout: "5s"},
			},
		},
		{
			name: "HTTPS endpoint cannot request plaintext transport",
			input: ObservabilityRuntimeConfigInput{
				Mode:      ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{Endpoint: "https://collector.example.test:4318", Protocol: "http_protobuf", Timeout: "5s", Insecure: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveObservabilityRuntimeConfig(tt.input)
			if err == nil || !strings.Contains(err.Error(), "insecure") {
				t.Fatal("ambiguous HTTP Collector transport was accepted")
			}
		})
	}
}

func TestObservabilityOTLPHTTPExporterDeliversBothSignalsToCollectorPaths(t *testing.T) {
	const headerValue = "authorization=Bearer%20t026-synthetic+header"
	var (
		mu       sync.Mutex
		requests = make(map[string]int)
	)
	collector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer t026-synthetic+header" {
			t.Error("OTLP request did not receive the one-time authorization header")
		}
		mu.Lock()
		requests[request.URL.Path]++
		mu.Unlock()
		response.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	exporter, err := NewObservabilityOTLPExporter(context.Background(), ObservabilityOTLPExporterConfigInput{
		Runtime: ObservabilityRuntimeConfigInput{
			Mode:        ObservabilityRuntimeModeCollector,
			Environment: "test",
			Collector: ObservabilityCollectorConfigInput{
				Endpoint:    collector.URL,
				Protocol:    "http_protobuf",
				Timeout:     "5s",
				HeaderValue: headerValue,
			},
		},
		Resource:      ObservabilityResourceInput{ServiceName: "longtermism", Environment: "test"},
		SamplingRatio: 1,
	})
	if err != nil {
		t.Fatalf("NewObservabilityOTLPExporter() error = %v", err)
	}
	defer func() {
		if err := exporter.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	}()

	_, span := exporter.TracerProvider().Tracer("t026-test").Start(context.Background(), "test-span")
	span.End()
	counter, err := exporter.MeterProvider().Meter("t026-test").Int64Counter("t026.counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1)
	if err := exporter.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests["/v1/traces"] != 1 || requests["/v1/metrics"] != 1 {
		t.Fatalf("Collector paths = %#v, want one trace and one metric request", requests)
	}
	if rendered := fmt.Sprintf("%#v", exporter); strings.Contains(rendered, headerValue) || strings.Contains(rendered, "Bearer t026-synthetic+header") {
		t.Fatal("exporter wrapper retained the raw authorization header")
	}
}

func TestParseOTLPHeadersRejectsUnsafeValuesWithoutEcho(t *testing.T) {
	for _, raw := range []string{"Bearer t026-raw", "authorization=Bearer%0D%0Ainjected", "=missing-key", "authorization=one,Authorization=two"} {
		t.Run(raw[:1], func(t *testing.T) {
			_, err := parseOTLPHeaders(raw)
			if err == nil {
				t.Fatal("parseOTLPHeaders() error = nil, want rejection")
			}
			if strings.Contains(err.Error(), raw) {
				t.Fatal("header validation error reflected sensitive input")
			}
		})
	}
}

func TestBuildObservabilityOTLPExporterConfig(t *testing.T) {
	const syntheticHeaderValue = "Bearer t014-synthetic-header-value"

	tests := []struct {
		name              string
		input             ObservabilityOTLPExporterConfigInput
		wantProtocol      ObservabilityOTLPProtocol
		wantEndpoint      string
		wantInsecure      bool
		wantTimeout       time.Duration
		wantSamplingRatio float64
		wantHeaderEnvName string
		wantCredentialSet bool
		wantResourceAttrs map[string]string
	}{
		{
			name: "defaults Collector transport to gRPC while preserving shared SDK settings",
			input: ObservabilityOTLPExporterConfigInput{
				Runtime: ObservabilityRuntimeConfigInput{
					Mode:        ObservabilityRuntimeModeCollector,
					Environment: "local",
					Collector: ObservabilityCollectorConfigInput{
						Endpoint:      "otel-collector:4317",
						Timeout:       "5s",
						Insecure:      true,
						HeaderEnvName: "OTEL_EXPORTER_OTLP_HEADERS",
						HeaderValue:   syntheticHeaderValue,
					},
				},
				Resource: ObservabilityResourceInput{
					ServiceName: "longtermism",
					Environment: "local",
					Version:     "v0.3.0",
					InstanceID:  "instance-t014",
				},
				SamplingRatio: 0.25,
			},
			wantProtocol:      ObservabilityOTLPProtocolGRPC,
			wantEndpoint:      "otel-collector:4317",
			wantInsecure:      true,
			wantTimeout:       5 * time.Second,
			wantSamplingRatio: 0.25,
			wantHeaderEnvName: "OTEL_EXPORTER_OTLP_HEADERS",
			wantCredentialSet: true,
			wantResourceAttrs: map[string]string{
				"service.name":           "longtermism",
				"deployment.environment": "local",
				"service.version":        "v0.3.0",
				"service.instance.id":    "instance-t014",
			},
		},
		{
			name: "HTTP protobuf override keeps the same Collector-only configuration boundary",
			input: ObservabilityOTLPExporterConfigInput{
				Runtime: ObservabilityRuntimeConfigInput{
					Mode:        ObservabilityRuntimeModeCollector,
					Environment: "staging",
					Collector: ObservabilityCollectorConfigInput{
						Endpoint: "https://collector.example.test:4318",
						Protocol: "http_protobuf",
						Timeout:  "9s",
					},
				},
				Resource: ObservabilityResourceInput{
					ServiceName: "longtermism",
					Environment: "staging",
				},
				SamplingRatio: 1,
			},
			wantProtocol:      ObservabilityOTLPProtocolHTTPProtobuf,
			wantEndpoint:      "https://collector.example.test:4318",
			wantInsecure:      false,
			wantTimeout:       9 * time.Second,
			wantSamplingRatio: 1,
			wantResourceAttrs: map[string]string{
				"service.name":           "longtermism",
				"deployment.environment": "staging",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// App 只把一份低敏、不可变的配置交给 traces 与 metrics exporter。这里不创建
			// 网络连接，避免离线契约测试把“配置正确”误当成后端投递已经成功。
			got, err := BuildObservabilityOTLPExporterConfig(tt.input)
			if err != nil {
				t.Fatal("BuildObservabilityOTLPExporterConfig() returned an unexpected error")
			}

			if got.Protocol != tt.wantProtocol {
				t.Fatalf("Protocol = %q, want %q", got.Protocol, tt.wantProtocol)
			}
			if got.Endpoint != tt.wantEndpoint {
				t.Fatalf("Endpoint = %q, want %q", got.Endpoint, tt.wantEndpoint)
			}
			if got.Insecure != tt.wantInsecure {
				t.Fatalf("Insecure = %v, want %v", got.Insecure, tt.wantInsecure)
			}
			if got.Timeout != tt.wantTimeout {
				t.Fatalf("Timeout = %s, want %s", got.Timeout, tt.wantTimeout)
			}
			if got.SamplingRatio != tt.wantSamplingRatio {
				t.Fatalf("SamplingRatio = %v, want %v", got.SamplingRatio, tt.wantSamplingRatio)
			}
			if got.HeaderEnvName != tt.wantHeaderEnvName {
				t.Fatalf("HeaderEnvName = %q, want %q", got.HeaderEnvName, tt.wantHeaderEnvName)
			}
			if got.CredentialPresent != tt.wantCredentialSet {
				t.Fatalf("CredentialPresent = %v, want %v", got.CredentialPresent, tt.wantCredentialSet)
			}
			if !reflect.DeepEqual(got.Resource.Attributes, tt.wantResourceAttrs) {
				t.Fatal("Resource attributes did not match the shared service identity")
			}

			// 认证 header 原值可来自环境变量或 secret file，但绝不能进入 SDK 配置快照。
			if rendered := fmt.Sprintf("%#v", got); strings.Contains(rendered, syntheticHeaderValue) {
				t.Fatal("exporter config snapshot leaked synthetic header value")
			}
		})
	}
}

func TestObservabilityOTLPExporterConfigurationOwnsOnlyCollectorEndpoint(t *testing.T) {
	// Tempo、Loki、Prometheus、Grafana、SigNoz 和 Langfuse 均由 Collector/profile
	// 持有；若它们重新进入应用输入，应用会绕过单一失败域并绑定具体后端。
	forbiddenFieldParts := []string{"tempo", "loki", "prometheus", "grafana", "signoz", "langfuse", "backend"}
	assertNoBackendSpecificFields(t, reflect.TypeFor[ObservabilityOTLPExporterConfigInput](), forbiddenFieldParts, map[reflect.Type]bool{})
	assertNoBackendSpecificFields(t, reflect.TypeFor[ObservabilityOTLPExporterConfig](), forbiddenFieldParts, map[reflect.Type]bool{})
	assertNoEndpointContainerInputs(t, reflect.TypeFor[ObservabilityOTLPExporterConfigInput](), map[reflect.Type]bool{})
}

func TestBuildObservabilityOTLPExporterConfigCopiesResourceAttributes(t *testing.T) {
	input := ObservabilityOTLPExporterConfigInput{
		Runtime: ObservabilityRuntimeConfigInput{
			Mode: ObservabilityRuntimeModeCollector,
			Collector: ObservabilityCollectorConfigInput{
				Endpoint: "otel-collector:4317",
				Timeout:  "5s",
			},
		},
		Resource: ObservabilityResourceInput{ServiceName: "longtermism", Environment: "local"},
	}

	first, err := BuildObservabilityOTLPExporterConfig(input)
	if err != nil {
		t.Fatal("BuildObservabilityOTLPExporterConfig() returned an unexpected error")
	}
	first.Resource.Attributes["service.name"] = "mutated-by-caller"

	second, err := BuildObservabilityOTLPExporterConfig(input)
	if err != nil {
		t.Fatal("BuildObservabilityOTLPExporterConfig() returned an unexpected error")
	}
	if second.Resource.Attributes["service.name"] != "longtermism" {
		t.Fatal("exporter configuration reused caller-mutable resource attributes")
	}
}

func TestBuildObservabilityOTLPExporterConfigRejectsInvalidSamplingRatio(t *testing.T) {
	const syntheticHeaderValue = "Bearer t014-invalid-sampling-header"

	for _, samplingRatio := range []float64{-0.01, 1.01, math.NaN()} {
		t.Run(fmt.Sprintf("sampling ratio %v is rejected", samplingRatio), func(t *testing.T) {
			_, err := BuildObservabilityOTLPExporterConfig(ObservabilityOTLPExporterConfigInput{
				Runtime: ObservabilityRuntimeConfigInput{
					Mode: ObservabilityRuntimeModeCollector,
					Collector: ObservabilityCollectorConfigInput{
						Endpoint:    "otel-collector:4317",
						Timeout:     "5s",
						HeaderValue: syntheticHeaderValue,
					},
				},
				SamplingRatio: samplingRatio,
			})
			if err == nil {
				t.Fatal("BuildObservabilityOTLPExporterConfig() error = nil, want invalid sampling ratio rejection")
			}
			if strings.Contains(err.Error(), syntheticHeaderValue) {
				t.Fatal("invalid sampling ratio error leaked synthetic header value")
			}
		})
	}
}

func assertNoBackendSpecificFields(t *testing.T, typeToCheck reflect.Type, forbiddenFieldParts []string, visited map[reflect.Type]bool) {
	t.Helper()
	if visited[typeToCheck] {
		return
	}
	visited[typeToCheck] = true

	for fieldIndex := range typeToCheck.NumField() {
		field := typeToCheck.Field(fieldIndex)
		fieldName := strings.ToLower(field.Name + " " + string(field.Tag))
		for _, forbidden := range forbiddenFieldParts {
			if strings.Contains(fieldName, forbidden) {
				t.Fatalf("exporter configuration exposes forbidden backend-specific field %q", field.Name)
			}
		}

		fieldType := field.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Struct {
			assertNoBackendSpecificFields(t, fieldType, forbiddenFieldParts, visited)
		}
	}
}

func assertNoEndpointContainerInputs(t *testing.T, typeToCheck reflect.Type, visited map[reflect.Type]bool) {
	t.Helper()
	if visited[typeToCheck] {
		return
	}
	visited[typeToCheck] = true

	for fieldIndex := range typeToCheck.NumField() {
		field := typeToCheck.Field(fieldIndex)
		fieldType := field.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}

		switch fieldType.Kind() {
		case reflect.Map, reflect.Slice, reflect.Array, reflect.Interface:
			// 导出器输入没有“额外 destinations”这类通用容器；否则任何后端 URL 都可绕过
			// Collector 单一出口。resource attributes 只出现在构造后的只读输出，不属于此输入。
			t.Fatalf("exporter input field %q must not accept generic endpoint containers", field.Name)
		case reflect.Struct:
			assertNoEndpointContainerInputs(t, fieldType, visited)
		}
	}
}
