package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
)

func TestProviderChatStreamAggregatesToolCallFragments(t *testing.T) {
	tests := []struct {
		name      string
		events    []map[string]any
		sendDone  bool
		wantCalls []llm.ToolCall
		wantError string
	}{
		{
			name:     "single tool call waits for complete arguments",
			sendDone: true,
			events: []map[string]any{
				streamToolCallEvent(0, "call_weather_001", "get_weather", ""),
				streamToolCallEvent(0, "", "", `{"city":"`),
				streamToolCallEvent(0, "", "", `Shanghai","unit":"`),
				streamToolCallEvent(0, "", "", `celsius"}`),
				streamToolCallFinishEvent(),
			},
			wantCalls: []llm.ToolCall{
				{
					ID:   "call_weather_001",
					Name: "get_weather",
					Arguments: map[string]any{
						"city": "Shanghai",
						"unit": "celsius",
					},
				},
			},
		},
		{
			name:     "interleaved tool calls remain isolated by index",
			sendDone: true,
			events: []map[string]any{
				streamToolCallsEvent(
					streamToolCallDelta(0, "call_search_001", "search_docs", `{"query":"`),
					streamToolCallDelta(1, "call_sum_001", "sum", `{"left":`),
				),
				streamToolCallsEvent(
					streamToolCallDelta(1, "", "", `2,"right":3}`),
					streamToolCallDelta(0, "", "", `streaming tool calls"}`),
				),
				streamToolCallFinishEvent(),
			},
			wantCalls: []llm.ToolCall{
				{
					ID:   "call_search_001",
					Name: "search_docs",
					Arguments: map[string]any{
						"query": "streaming tool calls",
					},
				},
				{
					ID:   "call_sum_001",
					Name: "sum",
					Arguments: map[string]any{
						"left":  float64(2),
						"right": float64(3),
					},
				},
			},
		},
		{
			name:     "incomplete arguments fail only when tool calls finish",
			sendDone: true,
			events: []map[string]any{
				streamToolCallEvent(0, "call_broken_001", "search_docs", `{"query":"unfinished`),
				streamToolCallFinishEvent(),
			},
			wantError: "call_broken_001",
		},
		{
			name:     "done marker before tool finish is a protocol error",
			sendDone: true,
			events: []map[string]any{
				streamToolCallEvent(0, "call_unfinished_001", "search_docs", `{"query":"complete json"}`),
			},
			wantError: "finish_reason=tool_calls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newToolCallStreamProvider(t, tt.events, tt.sendDone)

			chunks, err := provider.ChatStream(context.Background(), &llm.ChatRequest{
				Model:    "gpt-tool-stream",
				Messages: []llm.Message{{Role: llm.RoleUser, Content: "use tools"}},
			})
			if err != nil {
				t.Fatalf("ChatStream() initial error = %v", err)
			}

			var gotCalls []llm.ToolCall
			var streamErr error
			var sawToolCallFinish bool
			for chunk := range chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
				if chunk.FinishReason == llm.FinishToolCall {
					sawToolCallFinish = true
				}
				if chunk.DeltaToolCall != nil {
					// Agent executor 只能在模型明确结束 tool_calls 阶段后执行工具。
					// 如果 arguments 还没收齐就提前产出，会把半截 JSON 送入真实工具。
					if !sawToolCallFinish {
						t.Fatal("ChatStream() emitted a tool call before finish_reason=tool_calls")
					}
					gotCalls = append(gotCalls, *chunk.DeltaToolCall)
				}
			}

			if tt.wantError != "" {
				if len(gotCalls) != 0 {
					t.Fatalf("ChatStream() emitted %d tool calls from invalid arguments, want 0", len(gotCalls))
				}
				if streamErr == nil {
					t.Fatal("ChatStream() stream error = nil, want incomplete arguments protocol error")
				}
				if !strings.Contains(streamErr.Error(), tt.wantError) {
					t.Fatalf("ChatStream() stream error = %q, want %q for diagnosis", streamErr, tt.wantError)
				}
				return
			}

			if streamErr != nil {
				t.Fatalf("ChatStream() chunk error = %v, want fragments to remain buffered until complete", streamErr)
			}
			if !sawToolCallFinish {
				t.Fatal("ChatStream() did not emit finish_reason=tool_calls")
			}
			assertStreamToolCalls(t, gotCalls, tt.wantCalls)
		})
	}
}

func newToolCallStreamProvider(t *testing.T, events []map[string]any, sendDone bool) *Provider {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			writeStreamPayload(t, w, event)
		}
		if sendDone {
			writeStreamData(t, w, streamDoneMarker)
		}
	}))
	t.Cleanup(server.Close)

	provider, err := NewProvider(Config{
		BaseURL:      server.URL,
		APIKey:       "test-api-key",
		DefaultModel: "gpt-tool-stream",
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	return provider
}

func streamToolCallEvent(index int, id, name, arguments string) map[string]any {
	return streamToolCallsEvent(streamToolCallDelta(index, id, name, arguments))
}

func streamToolCallsEvent(toolCalls ...map[string]any) map[string]any {
	return map[string]any{
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": toolCalls,
				},
			},
		},
	}
}

func streamToolCallDelta(index int, id, name, arguments string) map[string]any {
	delta := map[string]any{
		"index": index,
	}
	if id != "" {
		delta["id"] = id
		delta["type"] = "function"
	}

	function := map[string]any{}
	if name != "" {
		function["name"] = name
	}
	if arguments != "" {
		function["arguments"] = arguments
	}
	if len(function) > 0 {
		delta["function"] = function
	}
	return delta
}

func streamToolCallFinishEvent() map[string]any {
	return map[string]any{
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "tool_calls",
			},
		},
	}
}

func assertStreamToolCalls(t *testing.T, got, want []llm.ToolCall) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("tool calls length = %d, want %d; got = %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].ID != want[index].ID || got[index].Name != want[index].Name {
			t.Fatalf("tool call[%d] identity = %#v, want %#v", index, got[index], want[index])
		}
		assertToolArguments(t, index, got[index].Arguments, want[index].Arguments)
	}
}

func assertToolArguments(t *testing.T, index int, got, want map[string]any) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("tool call[%d] arguments length = %d, want %d; got = %#v", index, len(got), len(want), got)
	}
	for key, wantValue := range want {
		if gotValue, ok := got[key]; !ok || !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("tool call[%d] arguments[%q] = %#v, want %#v", index, key, gotValue, wantValue)
		}
	}
}
