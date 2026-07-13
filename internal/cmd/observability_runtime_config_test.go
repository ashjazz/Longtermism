package cmd

import (
	"fmt"
	"strings"
	"testing"
)

func TestResolveObservabilityRuntimeConfig(t *testing.T) {
	const syntheticHeaderValue = "Bearer t011-synthetic-header-value"

	tests := []struct {
		name                  string
		input                 ObservabilityRuntimeConfigInput
		wantMode              ObservabilityRuntimeMode
		wantCollectorEnabled  bool
		wantCollectorProtocol string
		wantHeaderEnvName     string
		wantCredentialPresent bool
		wantErrField          string
	}{
		{
			name: "noop mode needs no Collector settings",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeNoop,
			},
			wantMode: ObservabilityRuntimeModeNoop,
		},
		{
			name: "local mode keeps network Collector disabled",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeLocal,
			},
			wantMode: ObservabilityRuntimeModeLocal,
		},
		{
			name: "collector mode accepts a complete gRPC configuration",
			input: ObservabilityRuntimeConfigInput{
				Mode:        ObservabilityRuntimeModeCollector,
				Environment: "local",
				Collector: ObservabilityCollectorConfigInput{
					Endpoint:      "otel-collector:4317",
					Protocol:      "grpc",
					Timeout:       "5s",
					HeaderEnvName: "OTEL_EXPORTER_OTLP_HEADERS",
					HeaderValue:   syntheticHeaderValue,
				},
			},
			wantMode:              ObservabilityRuntimeModeCollector,
			wantCollectorEnabled:  true,
			wantCollectorProtocol: "grpc",
			wantHeaderEnvName:     "OTEL_EXPORTER_OTLP_HEADERS",
			wantCredentialPresent: true,
		},
		{
			name: "collector mode accepts an HTTP protobuf configuration",
			input: ObservabilityRuntimeConfigInput{
				Mode:        ObservabilityRuntimeModeCollector,
				Environment: "staging",
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "https://collector.example.test:4318",
					Protocol: "http_protobuf",
					Timeout:  "9s",
				},
			},
			wantMode:              ObservabilityRuntimeModeCollector,
			wantCollectorEnabled:  true,
			wantCollectorProtocol: "http_protobuf",
		},
		{
			name: "collector mode rejects a missing endpoint",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Protocol: "grpc",
					Timeout:  "5s",
				},
			},
			wantErrField: "collector endpoint",
		},
		{
			name: "collector mode rejects an unsupported protocol",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "otel-collector:4317",
					Protocol: "udp",
					Timeout:  "5s",
				},
			},
			wantErrField: "collector protocol",
		},
		{
			name: "collector mode rejects the legacy HTTP protocol spelling",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "https://collector.example.test:4318",
					Protocol: "http/protobuf",
					Timeout:  "5s",
				},
			},
			wantErrField: "collector protocol",
		},
		{
			name: "collector mode rejects a non-positive timeout",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "https://collector.example.test:4318",
					Protocol: "http_protobuf",
					Timeout:  "0s",
				},
			},
			wantErrField: "collector timeout",
		},
		{
			name: "collector mode rejects a timeout above the bounded export window",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "otel-collector:4317",
					Timeout:  "61s",
				},
			},
			wantErrField: "collector timeout",
		},
		{
			name: "gRPC mode rejects a URL instead of Collector authority",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "https://collector.example.test:4317",
					Protocol: "grpc",
					Timeout:  "5s",
				},
			},
			wantErrField: "collector endpoint",
		},
		{
			name: "HTTP protobuf mode rejects a Collector authority without an HTTP URL",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "otel-collector:4318",
					Protocol: "http_protobuf",
					Timeout:  "5s",
				},
			},
			wantErrField: "collector endpoint",
		},
		{
			name: "collector mode rejects a header value mistakenly configured as an environment name",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint:      "otel-collector:4317",
					Protocol:      "grpc",
					Timeout:       "5s",
					HeaderEnvName: syntheticHeaderValue,
				},
			},
			wantErrField: "header environment name",
		},
		{
			name: "production rejects unauthorized insecure Collector transport",
			input: ObservabilityRuntimeConfigInput{
				Mode:        ObservabilityRuntimeModeCollector,
				Environment: "production",
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "otel-collector:4317",
					Protocol: "grpc",
					Timeout:  "5s",
					Insecure: true,
				},
			},
			wantErrField: "insecure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 运行时配置是应用到 Collector 的唯一出口。测试要先锁定非法组合，避免
			// 已显式启用 collector 却静默退化为 no-op，导致生产观测事实丢失。
			got, err := ResolveObservabilityRuntimeConfig(tt.input)
			if tt.wantErrField != "" {
				if err == nil {
					t.Fatalf("ResolveObservabilityRuntimeConfig() error = nil, want field %q", tt.wantErrField)
				}
				if !strings.Contains(err.Error(), tt.wantErrField) {
					t.Fatalf("error did not contain expected field %q", tt.wantErrField)
				}
				if strings.Contains(err.Error(), syntheticHeaderValue) {
					t.Fatal("runtime config error leaked synthetic header value")
				}
				return
			}

			if err != nil {
				t.Fatal("ResolveObservabilityRuntimeConfig() returned an unexpected error")
			}
			if got.Mode != tt.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if got.CollectorEnabled != tt.wantCollectorEnabled {
				t.Fatalf("CollectorEnabled = %v, want %v", got.CollectorEnabled, tt.wantCollectorEnabled)
			}
			if got.Collector.Protocol != tt.wantCollectorProtocol {
				t.Fatalf("Collector.Protocol = %q, want %q", got.Collector.Protocol, tt.wantCollectorProtocol)
			}
			if got.Collector.HeaderEnvName != tt.wantHeaderEnvName {
				t.Fatalf("Collector.HeaderEnvName = %q, want %q", got.Collector.HeaderEnvName, tt.wantHeaderEnvName)
			}
			if got.Collector.CredentialPresent != tt.wantCredentialPresent {
				t.Fatalf("Collector.CredentialPresent = %v, want %v", got.Collector.CredentialPresent, tt.wantCredentialPresent)
			}

			// Header 原值可能由环境变量/secret file 提供，但绝不能进入可打印配置快照。
			if rendered := fmt.Sprintf("%#v", got); strings.Contains(rendered, syntheticHeaderValue) {
				t.Fatal("runtime config snapshot leaked synthetic header value")
			}
		})
	}
}
