package llm

import "testing"

func TestProviderCapabilitiesModelToolSupport(t *testing.T) {
	tests := []struct {
		name string
		cap  ProviderCapabilities
		want bool
	}{
		{
			name: "tool capable provider",
			cap: ProviderCapabilities{
				ToolCalling:         true,
				StrictStructuredOut: true,
				Streaming:           true,
				ReasoningEffort:     true,
			},
			want: true,
		},
		{
			name: "plain chat provider",
			cap:  ProviderCapabilities{Streaming: true},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cap.ToolCalling != tt.want {
				t.Fatalf("ToolCalling = %v, want %v", tt.cap.ToolCalling, tt.want)
			}
		})
	}
}

func TestUsageTracksReasoningAndCacheTokens(t *testing.T) {
	usage := Usage{
		InputTokens:      100,
		OutputTokens:     50,
		ReasoningTokens:  20,
		CacheReadTokens:  80,
		CacheWriteTokens: 10,
		TotalTokens:      150,
	}

	if usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		t.Fatalf("TotalTokens = %d, want %d", usage.TotalTokens, usage.InputTokens+usage.OutputTokens)
	}
	if usage.ReasoningTokens == 0 || usage.CacheReadTokens == 0 || usage.CacheWriteTokens == 0 {
		t.Fatalf("usage did not preserve reasoning/cache fields: %#v", usage)
	}
}
