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
			wantHeaderEnvName:     "OTEL_EXPORTER_OTLP_HEADERS",
			wantCredentialPresent: true,
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
			name: "collector mode rejects a non-positive timeout",
			input: ObservabilityRuntimeConfigInput{
				Mode: ObservabilityRuntimeModeCollector,
				Collector: ObservabilityCollectorConfigInput{
					Endpoint: "otel-collector:4317",
					Protocol: "http/protobuf",
					Timeout:  "0s",
				},
			},
			wantErrField: "collector timeout",
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
					t.Fatalf("error = %q, want to contain field %q", err, tt.wantErrField)
				}
				if strings.Contains(err.Error(), syntheticHeaderValue) {
					t.Fatalf("error leaked synthetic header value: %q", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("ResolveObservabilityRuntimeConfig() error = %v", err)
			}
			if got.Mode != tt.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if got.CollectorEnabled != tt.wantCollectorEnabled {
				t.Fatalf("CollectorEnabled = %v, want %v", got.CollectorEnabled, tt.wantCollectorEnabled)
			}
			if got.Collector.HeaderEnvName != tt.wantHeaderEnvName {
				t.Fatalf("Collector.HeaderEnvName = %q, want %q", got.Collector.HeaderEnvName, tt.wantHeaderEnvName)
			}
			if got.Collector.CredentialPresent != tt.wantCredentialPresent {
				t.Fatalf("Collector.CredentialPresent = %v, want %v", got.Collector.CredentialPresent, tt.wantCredentialPresent)
			}

			// Header 原值可能由环境变量/secret file 提供，但绝不能进入可打印配置快照。
			if rendered := fmt.Sprintf("%#v", got); strings.Contains(rendered, syntheticHeaderValue) {
				t.Fatalf("runtime config snapshot leaked synthetic header value: %s", rendered)
			}
		})
	}
}
