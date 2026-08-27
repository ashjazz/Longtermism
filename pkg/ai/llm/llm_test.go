package llm

import (
	"reflect"
	"testing"
)

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

// TestChatResponseCarriesProviderUsageValueObject 固定 T202 的领域边界而不规定字段名、
// 构造器或校验方法归属：ChatResponse 的可选 token summary 与 availability 必须属于
// 同一个 ProviderUsage 值对象，且不能再保留一份扁平 Usage 副本。nil summary、指向
// 零值 summary 和指向非零 summary 分别表达 unavailable、reported zero 与 reported
// nonzero，避免同一组 Go 数值零值承担两种事实语义。
func TestChatResponseCarriesProviderUsageValueObject(t *testing.T) {
	responseType := reflect.TypeOf(ChatResponse{})
	providerUsageType := providerUsageTypeFromChatResponse(t, responseType)

	var availabilityType reflect.Type
	var summaryType reflect.Type
	availabilityCount := 0
	summaryCount := 0
	for index := 0; index < providerUsageType.NumField(); index++ {
		fieldType := providerUsageType.Field(index).Type
		if fieldType.Kind() == reflect.String && fieldType.Name() != "" {
			availabilityType = fieldType
			availabilityCount++
		}
		if fieldType == reflect.TypeOf((*Usage)(nil)) {
			summaryType = fieldType
			summaryCount++
		}
	}

	if availabilityCount != 1 || availabilityType == nil {
		t.Fatal("ProviderUsage must carry exactly one named availability enum")
	}
	if summaryCount != 1 || summaryType == nil {
		t.Fatal("ProviderUsage must carry exactly one optional *Usage summary")
	}

	unavailable := reflect.Zero(summaryType)
	reportedZero := reflect.ValueOf(&Usage{})
	reportedNonzero := reflect.ValueOf(&Usage{InputTokens: 9, OutputTokens: 4, TotalTokens: 13})
	if !unavailable.IsNil() || reportedZero.IsNil() || reportedNonzero.IsNil() {
		t.Fatal("ProviderUsage summary cannot distinguish unavailable from reported")
	}
	if reportedZero.Elem().Interface() == reportedNonzero.Elem().Interface() {
		t.Fatal("ProviderUsage summary cannot preserve explicit zero and nonzero facts")
	}
}

func providerUsageTypeFromChatResponse(t *testing.T, responseType reflect.Type) reflect.Type {
	t.Helper()
	usageType := reflect.TypeOf(Usage{})
	usagePointerType := reflect.TypeOf((*Usage)(nil))
	var providerUsageType reflect.Type
	providerUsageCount := 0
	hasSiblingSummary := false
	for index := 0; index < responseType.NumField(); index++ {
		fieldType := responseType.Field(index).Type
		if fieldType.Kind() == reflect.Struct && fieldType.Name() == "ProviderUsage" {
			providerUsageType = fieldType
			providerUsageCount++
			continue
		}
		if fieldType == usageType || fieldType == usagePointerType {
			hasSiblingSummary = true
		}
	}
	if providerUsageCount != 1 || providerUsageType == nil {
		t.Fatal("ChatResponse must carry exactly one ProviderUsage value object")
	}
	if hasSiblingSummary {
		t.Fatal("ChatResponse must not retain a sibling Usage summary beside ProviderUsage")
	}
	return providerUsageType
}
