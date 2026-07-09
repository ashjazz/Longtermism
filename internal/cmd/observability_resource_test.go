package cmd

import "testing"

func TestBuildObservabilityResource(t *testing.T) {
	tests := []struct {
		name       string
		input      ObservabilityResourceInput
		wantAttrs  map[string]string
		absentKeys []string
	}{
		{
			name: "service identity includes required OTel resource attributes",
			input: ObservabilityResourceInput{
				ServiceName: "longtermism",
				Environment: "local",
			},
			wantAttrs: map[string]string{
				"service.name":           "longtermism",
				"deployment.environment": "local",
			},
			absentKeys: []string{"service.version", "service.instance.id"},
		},
		{
			name: "version and instance id are optional resource attributes",
			input: ObservabilityResourceInput{
				ServiceName: "longtermism",
				Environment: "staging",
				Version:     "v0.2.0",
				InstanceID:  "instance-01",
			},
			wantAttrs: map[string]string{
				"service.name":           "longtermism",
				"deployment.environment": "staging",
				"service.version":        "v0.2.0",
				"service.instance.id":    "instance-01",
			},
		},
		{
			name:  "missing service identity falls back to safe local defaults",
			input: ObservabilityResourceInput{},
			wantAttrs: map[string]string{
				"service.name":           "longtermism",
				"deployment.environment": "local",
			},
			absentKeys: []string{"service.version", "service.instance.id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Resource 是基础设施平面的“名牌”：没有稳定 service/environment，
			// 后续 HTTP span、AI 语义记录和 eval 回链会散落在平台里，排障时很难聚合。
			got, err := BuildObservabilityResource(tt.input)
			if err != nil {
				t.Fatalf("BuildObservabilityResource() error = %v", err)
			}

			for key, want := range tt.wantAttrs {
				if got.Attributes[key] != want {
					t.Fatalf("Attributes[%q] = %q, want %q", key, got.Attributes[key], want)
				}
			}
			for _, key := range tt.absentKeys {
				if value, ok := got.Attributes[key]; ok {
					t.Fatalf("Attributes[%q] = %q, want absent", key, value)
				}
			}
		})
	}
}
