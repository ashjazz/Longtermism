package openai

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
)

// openAIStreamToolCallDelta 对应 OpenAI-compatible 流中的 delta.tool_calls[]。
//
// index 是聚合键。id/type/name 通常只在首个分片出现，arguments 则可能被拆成
// 任意长度的字符串片段；不能假设单个 SSE event 内已经是合法 JSON。
type openAIStreamToolCallDelta struct {
	Index    int                           `json:"index"`
	ID       string                        `json:"id"`
	Type     string                        `json:"type"`
	Function openAIStreamToolFunctionDelta `json:"function"`
}

type openAIStreamToolFunctionDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolCallAccumulator 在一次 ChatStream 生命周期内聚合所有工具调用分片。
//
// map 负责按 index 隔离并行 tool call；完成时再排序输出，避免 map 遍历顺序让
// Agent executor 的执行轨迹不稳定。该对象只由流读取 goroutine 使用，不需要锁。
type toolCallAccumulator struct {
	calls map[int]*toolCallBuffer
}

type toolCallBuffer struct {
	id        string
	name      strings.Builder
	arguments strings.Builder
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{
		calls: make(map[int]*toolCallBuffer),
	}
}

// Add 只拼接原始分片，不解析 arguments。
//
// 延迟解析是流式工具调用最重要的边界：例如 `{"city":"` 本身不是合法 JSON，
// 但它是完全正常的中间状态，不能提前作为协议错误暴露给上层。
func (a *toolCallAccumulator) Add(deltas []openAIStreamToolCallDelta) {
	for _, delta := range deltas {
		buffer, ok := a.calls[delta.Index]
		if !ok {
			buffer = &toolCallBuffer{}
			a.calls[delta.Index] = buffer
		}

		if delta.ID != "" {
			buffer.id = delta.ID
		}
		buffer.name.WriteString(delta.Function.Name)
		buffer.arguments.WriteString(delta.Function.Arguments)
	}
}

func (a *toolCallAccumulator) HasPending() bool {
	return len(a.calls) > 0
}

// Complete 在 finish_reason=tool_calls 到达后生成框架内部的结构化 ToolCall。
//
// 任一调用解析失败时不会返回部分结果，防止上层执行一半工具后才发现另一条
// arguments 已损坏。错误包含 tool call id/index，便于 trace 定位上游协议问题。
func (a *toolCallAccumulator) Complete() ([]llm.ToolCall, error) {
	if len(a.calls) == 0 {
		return nil, nil
	}

	indexes := make([]int, 0, len(a.calls))
	for index := range a.calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	completed := make([]llm.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		buffer := a.calls[index]
		arguments, err := decodeToolArguments(buffer.arguments.String())
		if err != nil {
			return nil, fmt.Errorf(
				"decode openai streaming tool call %q at index %d arguments: %w",
				buffer.id,
				index,
				err,
			)
		}

		completed = append(completed, llm.ToolCall{
			ID:        buffer.id,
			Name:      buffer.name.String(),
			Arguments: arguments,
		})
	}
	return completed, nil
}
