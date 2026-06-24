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
	Content string `json:"content"`
}

func streamChatChunks(ctx context.Context, body io.ReadCloser, chunks chan<- llm.ChatChunk) {
	defer close(chunks)
	defer body.Close()

	// SSE 连接建立成功后，后续失败已经不适合通过 ChatStream 的初始 error 返回。
	// 因此这里把协议错误、上游流中 error 和读取失败都作为 ChatChunk.Err 暴露，
	// 让调用方可以在同一个消费循环里处理“已经输出了一部分内容后失败”的生产场景。
	scanner := bufio.NewScanner(body)
	sawTerminalEvent := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == streamDoneMarker {
			sawTerminalEvent = true
			return
		}

		chunk, ok := parseStreamChunk(data)
		if !ok {
			continue
		}
		if !sendStreamChunk(ctx, chunks, chunk) {
			return
		}
		if chunk.FinishReason != "" {
			// 某些 OpenAI-compatible 网关在 finish_reason 后直接关闭连接，
			// 不发送标准 [DONE]。记录终止事件后，EOF 仍可视为完整结束。
			sawTerminalEvent = true
		}
		if chunk.Err != nil {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return
		}
		_ = sendStreamChunk(ctx, chunks, llm.ChatChunk{
			Err: fmt.Errorf("read openai stream: %w", errors.Join(llm.ErrUpstream, err)),
		})
		return
	}
	if !sawTerminalEvent && ctx.Err() == nil {
		_ = sendStreamChunk(ctx, chunks, llm.ChatChunk{
			Err: fmt.Errorf("read openai stream: unexpected EOF before terminal event: %w", llm.ErrUpstream),
		})
	}
}

func parseStreamChunk(data string) (llm.ChatChunk, bool) {
	var event openAIStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return llm.ChatChunk{
			Err: fmt.Errorf("decode openai stream event: %w", err),
		}, true
	}

	if event.Error.Message != "" || event.Error.Type != "" || event.Error.Code != "" {
		return llm.ChatChunk{
			Err: errors.New(buildStreamErrorMessage(event.Error)),
		}, true
	}

	chunk := llm.ChatChunk{}
	if len(event.Choices) > 0 {
		choice := event.Choices[0]
		chunk.DeltaContent = choice.Delta.Content
		chunk.FinishReason = mapFinishReason(choice.FinishReason)
	}
	if event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 || event.Usage.TotalTokens != 0 {
		chunk.Usage = &llm.Usage{
			InputTokens:  event.Usage.PromptTokens,
			OutputTokens: event.Usage.CompletionTokens,
			TotalTokens:  event.Usage.TotalTokens,
		}
	}

	return chunk, chunk.DeltaContent != "" || chunk.FinishReason != "" || chunk.Usage != nil
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
