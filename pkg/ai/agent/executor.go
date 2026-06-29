package agent

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
)

const (
	defaultMaxSteps     = 10
	defaultStepTimeoutS = 30

	terminatedFinished       = "finished"
	terminatedMaxSteps       = "max_steps"
	terminatedLoopDetected   = "loop_detected"
	terminatedStepTimeout    = "step_timeout"
	terminatedBudgetExceeded = "budget_exceeded"
)

// NativeExecutor 是最小 native tool calling 执行器。
//
// 它只消费 provider 返回的结构化 llm.ToolCall，不解析 ReAct 时代的 Thought/Action 文本。
// 这样做牺牲了一些“看起来什么文本都能跑”的便利，但换来工具参数、trace、评估和权限控制都能
// 依赖稳定结构，而不是依赖脆弱的自然语言解析。
type NativeExecutor struct {
	provider llm.Provider
	registry *Registry
}

// NewExecutor 创建 Agent executor。
func NewExecutor(provider llm.Provider, registry *Registry) *NativeExecutor {
	return &NativeExecutor{
		provider: provider,
		registry: registry,
	}
}

// Run 执行 native tool calling 循环，直到拿到最终答案或命中安全边界。
func (e *NativeExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if err := validateExecutor(e); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	limit := normalizeLimit(req.Limit)
	messages := []llm.Message{{Role: llm.RoleUser, Content: req.Query}}
	result := Result{}
	var lastCall *llm.ToolCall

	for {
		response, err := e.provider.Chat(ctx, &llm.ChatRequest{
			Model:    req.Model,
			Messages: messages,
			Tools:    e.registry.LLMTools(),
		})
		if err != nil {
			return result, fmt.Errorf("agent provider chat: %w", err)
		}

		result.TokensUsed += response.Usage.TotalTokens
		if exceededTokenBudget(result.TokensUsed, limit.TokenBudget) {
			result.TerminatedBy = terminatedBudgetExceeded
			return result, nil
		}

		if response.FinishReason != llm.FinishToolCall {
			result.Answer = response.Content
			result.TerminatedBy = terminatedFinished
			return result, nil
		}
		if len(response.ToolCalls) == 0 {
			return result, fmt.Errorf("agent provider returned finish_reason=%q without tool calls", response.FinishReason)
		}

		for _, toolCall := range response.ToolCalls {
			if lastCall != nil && sameToolCall(*lastCall, toolCall) {
				result.TerminatedBy = terminatedLoopDetected
				return result, nil
			}

			step, terminated, err := e.invokeTool(ctx, toolCall, limit)
			if err != nil {
				return result, err
			}
			result.Steps = append(result.Steps, step)
			result.StepsTaken = len(result.Steps)
			if terminated != "" {
				result.TerminatedBy = terminated
				return result, nil
			}
			if result.StepsTaken >= limit.MaxSteps {
				result.TerminatedBy = terminatedMaxSteps
				return result, nil
			}

			callCopy := cloneToolCall(toolCall)
			lastCall = &callCopy
			messages = append(messages, toolResultMessage(step.Result))
		}
	}
}

func (e *NativeExecutor) invokeTool(ctx context.Context, toolCall llm.ToolCall, limit Limit) (StepResult, string, error) {
	tool, err := e.registry.Get(toolCall.Name)
	if err != nil {
		return StepResult{}, "", err
	}

	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(limit.StepTimeoutS)*time.Second)
	defer cancel()

	startedAt := time.Now()
	content, err := tool.Invoke(stepCtx, cloneMap(toolCall.Arguments))
	step := StepResult{
		ToolCall: cloneToolCall(toolCall),
		Result: llm.ToolResult{
			ToolCallID: toolCall.ID,
			Name:       toolCall.Name,
			Content:    content,
			IsError:    err != nil,
		},
		LatencyMs: time.Since(startedAt).Milliseconds(),
	}
	if err == nil {
		return step, "", nil
	}
	if stepCtx.Err() != nil {
		return step, terminatedStepTimeout, nil
	}
	return step, "", fmt.Errorf("invoke tool %q: %w", toolCall.Name, err)
}

func validateExecutor(executor *NativeExecutor) error {
	if executor == nil {
		return fmt.Errorf("agent executor is required")
	}
	if executor.provider == nil {
		return fmt.Errorf("agent executor provider is required")
	}
	if executor.registry == nil {
		return fmt.Errorf("agent executor registry is required")
	}
	return nil
}

func normalizeLimit(limit Limit) Limit {
	if limit.MaxSteps <= 0 {
		limit.MaxSteps = defaultMaxSteps
	}
	if limit.StepTimeoutS <= 0 {
		limit.StepTimeoutS = defaultStepTimeoutS
	}
	return limit
}

func exceededTokenBudget(tokensUsed int, tokenBudget int) bool {
	return tokenBudget > 0 && tokensUsed > tokenBudget
}

func sameToolCall(previous llm.ToolCall, current llm.ToolCall) bool {
	return previous.Name == current.Name && reflect.DeepEqual(previous.Arguments, current.Arguments)
}

func toolResultMessage(result llm.ToolResult) llm.Message {
	return llm.Message{
		Role:       llm.RoleTool,
		Content:    result.Content,
		Name:       result.Name,
		ToolCallID: result.ToolCallID,
	}
}

func cloneToolCall(source llm.ToolCall) llm.ToolCall {
	return llm.ToolCall{
		ID:        source.ID,
		Name:      source.Name,
		Arguments: cloneMap(source.Arguments),
	}
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}

	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneMapValue(value)
	}
	return cloned
}

func cloneMapValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneMapValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
