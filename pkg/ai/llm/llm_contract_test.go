package llm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm/testutil"
)

// ProviderContractCase 描述一组可复用的 provider 契约样例。
//
// 这里刻意不直接依赖某个 fake 或真实 provider，而是要求调用方传入 NewProvider。
// 这样同一套契约测试可以同时约束：
//   - T010 将要实现的 fake provider；
//   - P0-A 将要实现的 OpenAI-compatible adapter；
//   - 未来 DeepSeek、Ollama、Anthropic 等 adapter。
//
// 契约测试关注的是“框架上层可以依赖的稳定语义”，而不是某个供应商协议细节。
type ProviderContractCase struct {
	Name             string
	NewProvider      func(t *testing.T) llm.Provider
	ChatRequest      *llm.ChatRequest
	WantChat         llm.ChatResponse
	ToolRequest      *llm.ChatRequest
	WantToolCall     llm.ToolCall
	StreamRequest    *llm.ChatRequest
	WantStreamDeltas []string
	WantCapabilities llm.ProviderCapabilities
	UpstreamRequest  *llm.ChatRequest
	CancelRequest    *llm.ChatRequest
}

func TestFakeProviderContract(t *testing.T) {
	t.Parallel()

	capabilities := llm.ProviderCapabilities{
		ToolCalling: true,
		Streaming:   true,
	}
	usage := llm.Usage{
		InputTokens:  7,
		OutputTokens: 5,
		TotalTokens:  12,
	}
	toolCall := llm.ToolCall{
		ID:   "call_weather",
		Name: "get_weather",
		Arguments: map[string]any{
			"city": "Shanghai",
		},
	}

	RunProviderContractTests(t, ProviderContractCase{
		Name: "fake-provider",
		NewProvider: func(t *testing.T) llm.Provider {
			t.Helper()

			return testutil.NewFakeProvider(testutil.FakeProviderConfig{
				Name: "fake-provider",
				Capabilities: map[string]llm.ProviderCapabilities{
					"chat-model":     capabilities,
					"tool-model":     capabilities,
					"stream-model":   capabilities,
					"upstream-model": capabilities,
					"cancel-model":   capabilities,
				},
				ChatResponses: map[string]llm.ChatResponse{
					"chat-model": {
						Content:      "hello from fake",
						Model:        "chat-model",
						Usage:        usage,
						FinishReason: llm.FinishStop,
					},
					"tool-model": {
						Model:        "tool-model",
						Usage:        usage,
						FinishReason: llm.FinishToolCall,
						ToolCalls:    []llm.ToolCall{toolCall},
					},
					"cancel-model": {
						Content:      "should not be returned after cancel",
						Model:        "cancel-model",
						Usage:        usage,
						FinishReason: llm.FinishStop,
					},
				},
				ChatErrors: map[string]error{
					"upstream-model": llm.ErrUpstream,
				},
				StreamChunks: map[string][]llm.ChatChunk{
					"stream-model": {
						{DeltaContent: "hel"},
						{DeltaContent: "lo"},
						{FinishReason: llm.FinishStop, Usage: &usage},
					},
				},
			})
		},
		ChatRequest: &llm.ChatRequest{
			Model:    "chat-model",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
		},
		WantChat: llm.ChatResponse{
			Content:      "hello from fake",
			Model:        "chat-model",
			Usage:        usage,
			FinishReason: llm.FinishStop,
		},
		ToolRequest: &llm.ChatRequest{
			Model: "tool-model",
			Messages: []llm.Message{
				{Role: llm.RoleUser, Content: "weather"},
			},
			Tools: []llm.Tool{
				{
					Name:        "get_weather",
					Description: "Get weather by city.",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
		WantToolCall: toolCall,
		StreamRequest: &llm.ChatRequest{
			Model:    "stream-model",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: "stream"}},
		},
		WantStreamDeltas: []string{"hel", "lo"},
		WantCapabilities: capabilities,
		UpstreamRequest: &llm.ChatRequest{
			Model:    "upstream-model",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: "fail"}},
		},
		CancelRequest: &llm.ChatRequest{
			Model:    "cancel-model",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: "cancel"}},
		},
	})
}

// RunProviderContractTests 执行所有 llm.Provider 都必须满足的最低行为契约。
//
// 注意：这个函数本身不应该访问网络，也不应该依赖 API key。真实 provider adapter
// 需要通过 httptest、本地 fake transport 或等价方式复用这套测试，避免默认测试变成
// 不稳定、昂贵、受限流影响的 live test。
func RunProviderContractTests(t *testing.T, tc ProviderContractCase) {
	t.Helper()

	t.Run(tc.Name+"/name", func(t *testing.T) {
		t.Parallel()

		// Provider 名称会进入 trace、限流 key、健康检查和故障诊断。
		// 空名称会让生产问题很难定位到具体供应商或 adapter。
		provider := tc.NewProvider(t)
		if provider.Name() == "" {
			t.Fatal("Name() returned empty provider name")
		}
	})

	t.Run(tc.Name+"/capabilities", func(t *testing.T) {
		t.Parallel()

		// Capabilities 是后续 failover、model routing 和 Agent tool calling 的控制面。
		// 这里要求 provider 给出稳定声明，避免上层误把不支持工具/流式的模型放进对应路径。
		provider := tc.NewProvider(t)
		got := provider.Capabilities(tc.ChatRequest.Model)
		if got != tc.WantCapabilities {
			t.Fatalf("Capabilities() = %#v, want %#v", got, tc.WantCapabilities)
		}
	})

	t.Run(tc.Name+"/chat", func(t *testing.T) {
		t.Parallel()

		// 非流式 Chat 是最基础能力。它必须保留 content、实际 model、finish reason
		// 和 usage，因为这些字段会被 obs trace、成本控制和 eval report 复用。
		provider := tc.NewProvider(t)
		got, err := provider.Chat(context.Background(), cloneChatRequest(tc.ChatRequest))
		if err != nil {
			t.Fatalf("Chat() error = %v", err)
		}
		assertChatResponse(t, got, tc.WantChat)
	})

	t.Run(tc.Name+"/tool-call", func(t *testing.T) {
		t.Parallel()

		// Agent executor 不能解析旧式 Thought/Action 文本；它依赖结构化 ToolCall。
		// 因此 provider adapter 必须把供应商返回的 tool call 稳定映射为 ID、名称和参数 map。
		provider := tc.NewProvider(t)
		got, err := provider.Chat(context.Background(), cloneChatRequest(tc.ToolRequest))
		if err != nil {
			t.Fatalf("Chat() tool call error = %v", err)
		}
		if len(got.ToolCalls) != 1 {
			t.Fatalf("ToolCalls length = %d, want 1", len(got.ToolCalls))
		}
		assertToolCall(t, got.ToolCalls[0], tc.WantToolCall)
	})

	t.Run(tc.Name+"/stream", func(t *testing.T) {
		t.Parallel()

		// 流式接口不仅要按顺序吐出 delta，还要在末尾暴露 finish reason 和 usage。
		// TTFT、用户体验和成本统计都会依赖这条路径，所以它必须成为 provider 公共契约。
		provider := tc.NewProvider(t)
		chunks, err := provider.ChatStream(context.Background(), cloneChatRequest(tc.StreamRequest))
		if err != nil {
			t.Fatalf("ChatStream() error = %v", err)
		}

		var deltas []string
		var finalUsage *llm.Usage
		var finalReason llm.FinishReason
		for chunk := range chunks {
			if chunk.Err != nil {
				t.Fatalf("ChatStream() chunk error = %v", chunk.Err)
			}
			if chunk.DeltaContent != "" {
				deltas = append(deltas, chunk.DeltaContent)
			}
			if chunk.Usage != nil {
				finalUsage = chunk.Usage
			}
			if chunk.FinishReason != "" {
				finalReason = chunk.FinishReason
			}
		}
		assertStringSlice(t, deltas, tc.WantStreamDeltas)
		if finalReason == "" {
			t.Fatal("ChatStream() did not emit final finish reason")
		}
		if finalUsage == nil {
			t.Fatal("ChatStream() did not emit final usage")
		}
	})

	t.Run(tc.Name+"/upstream-error", func(t *testing.T) {
		t.Parallel()

		// ErrUpstream 是 resilience 层判断“可重试/可熔断/可降级”的信号。
		// 429、5xx、timeout、连接失败等应归入这类；参数或认证错误不应伪装成上游不可用。
		provider := tc.NewProvider(t)
		_, err := provider.Chat(context.Background(), cloneChatRequest(tc.UpstreamRequest))
		if !errors.Is(err, llm.ErrUpstream) {
			t.Fatalf("Chat() upstream error = %v, want errors.Is ErrUpstream", err)
		}
	})

	t.Run(tc.Name+"/context-cancel", func(t *testing.T) {
		t.Parallel()

		// context 取消必须尽快返回，否则用户取消、请求超时或上游熔断后仍会继续消耗 token 和连接。
		// 这里不用 time.Sleep 等待，而是立即取消 context 后测量返回时间。
		provider := tc.NewProvider(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		startedAt := time.Now()
		_, err := provider.Chat(ctx, cloneChatRequest(tc.CancelRequest))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Chat() cancel error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
			t.Fatalf("Chat() cancel took %s, want under 100ms", elapsed)
		}
	})
}

// assertChatResponse 只检查框架契约字段，不检查供应商私有字段。
// 这能防止 adapter 把 OpenAI/Anthropic 等私有响应结构泄漏到核心接口。
func assertChatResponse(t *testing.T, got *llm.ChatResponse, want llm.ChatResponse) {
	t.Helper()

	if got == nil {
		t.Fatal("Chat() response is nil")
	}
	if got.Content != want.Content {
		t.Fatalf("Content = %q, want %q", got.Content, want.Content)
	}
	if got.Model != want.Model {
		t.Fatalf("Model = %q, want %q", got.Model, want.Model)
	}
	if got.FinishReason != want.FinishReason {
		t.Fatalf("FinishReason = %q, want %q", got.FinishReason, want.FinishReason)
	}
	if got.Usage != want.Usage {
		t.Fatalf("Usage = %#v, want %#v", got.Usage, want.Usage)
	}
}

// assertToolCall 明确检查 arguments 是结构化 map，而不是未解析的 JSON 字符串。
// 这是为了让 Agent 工具执行层可以直接做 schema 校验和权限判断。
func assertToolCall(t *testing.T, got llm.ToolCall, want llm.ToolCall) {
	t.Helper()

	if got.ID != want.ID || got.Name != want.Name {
		t.Fatalf("ToolCall = %#v, want %#v", got, want)
	}
	if len(got.Arguments) != len(want.Arguments) {
		t.Fatalf("ToolCall.Arguments length = %d, want %d", len(got.Arguments), len(want.Arguments))
	}
	for key, wantValue := range want.Arguments {
		if got.Arguments[key] != wantValue {
			t.Fatalf("ToolCall.Arguments[%q] = %#v, want %#v", key, got.Arguments[key], wantValue)
		}
	}
}

// assertStringSlice 用来验证流式 delta 的顺序。
// 对用户界面和 SSE 输出而言，顺序错误会造成肉眼可见的响应错乱。
func assertStringSlice(t *testing.T, got []string, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("slice length = %d, want %d: got %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slice[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// cloneChatRequest 让契约测试以不可变输入的方式调用 provider。
// 如果 provider 在内部修改请求对象，这类副作用会污染并行子测试，也会让真实业务中的重试/failover 变得危险。
func cloneChatRequest(req *llm.ChatRequest) *llm.ChatRequest {
	if req == nil {
		return nil
	}

	// 解指针，相当于生成一个新的变量，与原变量不共享内存
	cloned := *req
	cloned.Messages = append([]llm.Message(nil), req.Messages...)
	cloned.Tools = append([]llm.Tool(nil), req.Tools...)
	if req.StructuredOutput != nil {
		structured := *req.StructuredOutput
		structured.Schema = cloneMap(req.StructuredOutput.Schema)
		cloned.StructuredOutput = &structured
	}
	return &cloned
}

// cloneMap 目前只做浅拷贝，足够覆盖 P0 的 schema/arguments 场景。
// 如果后续在 schema 中放入深层嵌套对象，再扩展为递归 clone。
func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}

	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
