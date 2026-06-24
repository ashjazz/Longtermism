package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
)

const streamDoneMarker = "[DONE]"

type openAIStreamEvent struct {
	Choices []openAIStreamChoice `json:"choices"`
	Usage   openAIUsage          `json:"usage"`
	Error   openAIErrorBody      `json:"error"`
}

type openAIStreamChoice struct {
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason string            `json:"finish_reason"`
}

type openAIStreamDelta struct {
	Content   string                      `json:"content"`
	ToolCalls []openAIStreamToolCallDelta `json:"tool_calls"`
}

func streamChatChunks(ctx context.Context, body io.ReadCloser, chunks chan<- llm.ChatChunk) {
	defer close(chunks)
	defer body.Close()

	// SSE 连接建立成功后，后续失败已经不适合通过 ChatStream 的初始 error 返回。
	// 因此这里把协议错误、上游流中 error 和读取失败都作为 ChatChunk.Err 暴露，
	// 让调用方可以在同一个消费循环里处理“已经输出了一部分内容后失败”的生产场景。
	scanner := bufio.NewScanner(body)
	processor := newStreamEventProcessor()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !processor.Process(ctx, chunks, data) {
			return
		}
	}

	reportStreamReadEnd(ctx, chunks, scanner.Err(), processor.sawTerminalEvent)
}

func reportStreamReadEnd(ctx context.Context, chunks chan<- llm.ChatChunk, scanErr error, sawTerminalEvent bool) {
	if ctx.Err() != nil {
		return
	}
	if scanErr != nil {
		_ = sendStreamChunk(ctx, chunks, llm.ChatChunk{
			Err: fmt.Errorf("read openai stream: %w", errors.Join(llm.ErrUpstream, scanErr)),
		})
		return
	}
	if !sawTerminalEvent {
		_ = sendStreamChunk(ctx, chunks, llm.ChatChunk{
			Err: fmt.Errorf("read openai stream: unexpected EOF before terminal event: %w", llm.ErrUpstream),
		})
	}
}

type streamEventProcessor struct {
	// toolCalls 只在当前流读取 goroutine 内使用，负责聚合尚未完成的工具调用。
	toolCalls        *toolCallAccumulator
	sawTerminalEvent bool
}

func newStreamEventProcessor() *streamEventProcessor {
	return &streamEventProcessor{toolCalls: newToolCallAccumulator()}
}

// Process 消费一个 SSE data 值；返回 false 表示流已终止或发生错误。
func (p *streamEventProcessor) Process(ctx context.Context, chunks chan<- llm.ChatChunk, data string) bool {
	if data == streamDoneMarker {
		// 正常协议会先用 finish_reason=tool_calls 完成聚合，再发送 [DONE]。
		// 如果此时仍有待完成调用，静默关闭会让 Agent 永久丢失模型请求的工具动作。
		if p.toolCalls.HasPending() {
			_ = sendStreamChunk(ctx, chunks, llm.ChatChunk{
				Err: fmt.Errorf("openai stream ended before finish_reason=tool_calls"),
			})
		} else {
			p.sawTerminalEvent = true
		}
		return false
	}

	chunk, toolCallDeltas, emitChunk := parseStreamChunk(data)
	p.toolCalls.Add(toolCallDeltas)
	// 纯 tool-call delta 只更新聚合状态，不向调用方发送没有可消费字段的空 chunk。
	// arguments 可能横跨多个 event，必须继续读取到 finish_reason 后再结构化输出。
	if !emitChunk {
		return true
	}
	if !sendStreamChunk(ctx, chunks, chunk) {
		return false
	}
	if chunk.FinishReason == llm.FinishToolCall && !p.emitCompletedToolCalls(ctx, chunks) {
		return false
	}
	if chunk.FinishReason != "" {
		p.sawTerminalEvent = true
	}
	return chunk.Err == nil
}

func (p *streamEventProcessor) emitCompletedToolCalls(ctx context.Context, chunks chan<- llm.ChatChunk) bool {
	completed, err := p.toolCalls.Complete()
	if err != nil {
		_ = sendStreamChunk(ctx, chunks, llm.ChatChunk{Err: err})
		return false
	}
	for index := range completed {
		// finish chunk 已先发送，调用方看到结构化 ToolCall 时可以安全进入
		// Agent executor，不会把尚未收齐的 arguments 提前交给真实工具。
		if !sendStreamChunk(ctx, chunks, llm.ChatChunk{DeltaToolCall: &completed[index]}) {
			return false
		}
	}
	p.toolCalls = newToolCallAccumulator()
	return true
}

func parseStreamChunk(data string) (llm.ChatChunk, []openAIStreamToolCallDelta, bool) {
	var event openAIStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return llm.ChatChunk{
			Err: fmt.Errorf("decode openai stream event: %w", err),
		}, nil, true
	}

	if event.Error.Message != "" || event.Error.Type != "" || event.Error.Code != "" {
		return llm.ChatChunk{
			Err: errors.New(buildStreamErrorMessage(event.Error)),
		}, nil, true
	}

	chunk := llm.ChatChunk{}
	var toolCallDeltas []openAIStreamToolCallDelta
	if len(event.Choices) > 0 {
		choice := event.Choices[0]
		chunk.DeltaContent = choice.Delta.Content
		chunk.FinishReason = mapFinishReason(choice.FinishReason)
		toolCallDeltas = choice.Delta.ToolCalls
	}
	if event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 || event.Usage.TotalTokens != 0 {
		chunk.Usage = &llm.Usage{
			InputTokens:  event.Usage.PromptTokens,
			OutputTokens: event.Usage.CompletionTokens,
			TotalTokens:  event.Usage.TotalTokens,
		}
	}

	// 只有包含文本、终止原因或 usage 的 event 才需要立即转发；
	// tool-call delta 已单独返回给聚合器处理。
	emitChunk := chunk.DeltaContent != "" || chunk.FinishReason != "" || chunk.Usage != nil
	return chunk, toolCallDeltas, emitChunk
}

func buildStreamErrorMessage(body openAIErrorBody) string {
	parts := []string{"openai stream error"}
	if body.Type != "" {
		parts = append(parts, "type "+body.Type)
	}
	if body.Code != "" {
		parts = append(parts, "code "+body.Code)
	}
	if body.Message != "" {
		parts = append(parts, "message "+body.Message)
	}
	return strings.Join(parts, ": ")
}

func sendStreamChunk(ctx context.Context, chunks chan<- llm.ChatChunk, chunk llm.ChatChunk) bool {
	select {
	case chunks <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}
