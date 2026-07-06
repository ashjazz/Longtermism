package cmd

import "testing"

func TestResolveObservabilityConfig(t *testing.T) {
	tests := []struct {
		name                     string
		input                    ObservabilityConfigInput
		wantEnabled              bool
		wantSink                 ObservabilitySink
		wantLocalSinkEnabled     bool
		wantExternalExport       bool
		wantExternalSkipReason   bool
		wantExternalEndpointName string
	}{
		{
			name:                   "default configuration keeps observability disabled",
			input:                  ObservabilityConfigInput{},
			wantEnabled:            false,
			wantSink:               ObservabilitySinkNoop,
			wantLocalSinkEnabled:   false,
			wantExternalExport:     false,
			wantExternalSkipReason: false,
		},
		{
			name: "explicit noop sink keeps runtime observability disabled",
			input: ObservabilityConfigInput{
				Enabled: true,
				Sink:    ObservabilitySinkNoop,
			},
			wantEnabled:            false,
			wantSink:               ObservabilitySinkNoop,
			wantLocalSinkEnabled:   false,
			wantExternalExport:     false,
			wantExternalSkipReason: false,
		},
		{
			name: "enabled local sink records offline spans without external export",
			input: ObservabilityConfigInput{
				Enabled: true,
				Sink:    ObservabilitySinkLocal,
			},
			wantEnabled:            true,
			wantSink:               ObservabilitySinkLocal,
			wantLocalSinkEnabled:   true,
			wantExternalExport:     false,
			wantExternalSkipReason: false,
		},
		{
			name: "real platform without endpoint or credentials does not contact external services",
			input: ObservabilityConfigInput{
				Enabled: true,
				Sink:    ObservabilitySinkPlatform,
				Platform: ObservabilityPlatformConfig{
					Provider: "otlp",
				},
			},
			wantEnabled:              false,
			wantSink:                 ObservabilitySinkNoop,
			wantLocalSinkEnabled:     false,
			wantExternalExport:       false,
			wantExternalSkipReason:   true,
			wantExternalEndpointName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 配置解析是基础设施平面的第一道安全阀：默认不外连，真实平台缺配置时
			// 必须显式降级，而不是在启动期悄悄拨打外部 endpoint。
			got, err := ResolveObservabilityConfig(tt.input)
			if err != nil {
				t.Fatalf("ResolveObservabilityConfig() error = %v", err)
			}

			if got.Enabled != tt.wantEnabled {
				t.Fatalf("Enabled = %v, want %v", got.Enabled, tt.wantEnabled)
			}
			if got.Sink != tt.wantSink {
				t.Fatalf("Sink = %q, want %q", got.Sink, tt.wantSink)
			}
			if got.LocalSinkEnabled != tt.wantLocalSinkEnabled {
				t.Fatalf("LocalSinkEnabled = %v, want %v", got.LocalSinkEnabled, tt.wantLocalSinkEnabled)
			}
			if got.ExternalExportEnabled != tt.wantExternalExport {
				t.Fatalf("ExternalExportEnabled = %v, want %v", got.ExternalExportEnabled, tt.wantExternalExport)
			}
			if (got.ExternalSkipReason != "") != tt.wantExternalSkipReason {
				t.Fatalf("ExternalSkipReason empty = %v, want non-empty %v", got.ExternalSkipReason == "", tt.wantExternalSkipReason)
			}
			if got.ExternalEndpoint != tt.wantExternalEndpointName {
				t.Fatalf("ExternalEndpoint = %q, want %q", got.ExternalEndpoint, tt.wantExternalEndpointName)
			}
		})
	}
}
