// Package openai 提供 OpenAI-compatible Chat Completions adapter。
//
// P0 阶段先采用 OpenAI-compatible 协议，是因为 DeepSeek、OpenAI 兼容代理、
// 本地网关等都可以共享这类 HTTP/JSON 形态；但框架上层仍然只依赖 llm.Provider。
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
)

const providerName = "openai"

const chatCompletionsPath = "/chat/completions"

const defaultRequestTimeout = 60 * time.Second

// Config 是 OpenAI-compatible provider 的稳定装配配置。
//
// APIKey 只在服务端注入并参与请求认证，绝不能进入客户端、日志或错误信息。
// Capabilities 允许 P0 用静态/配置表声明模型能力，避免运行时网络探测带来不稳定门禁。
type Config struct {
	BaseURL      string
	APIKey       string
	DefaultModel string
	HTTPClient   *http.Client
	Capabilities map[string]llm.ProviderCapabilities
}

// Provider 实现 llm.Provider，并把 OpenAI-compatible 协议映射到框架内部契约。
//
// 字段保持私有，避免调用方绕过构造函数修改 baseURL、apiKey 或 capabilities。
// 这类配置一旦在运行期漂移，会让 trace、成本统计和故障诊断很难复现。
type Provider struct {
	baseURL      string
	apiKey       string
	defaultModel string
	httpClient   *http.Client
	capabilities map[string]llm.ProviderCapabilities
}

// NewProvider 创建 provider，并在装配阶段快速暴露缺失配置。
//
// 生产环境里，baseURL/API key/default model 属于启动期配置问题；如果延迟到用户请求
// 进入 LLM 路径后才失败，resilience 层会难以区分“系统配置错误”和“上游临时不可用”。
func NewProvider(config Config) (*Provider, error) {
	baseURL, err := normalizeBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("openai config apiKey is required")
	}
	defaultModel := strings.TrimSpace(config.DefaultModel)
	if defaultModel == "" {
		return nil, fmt.Errorf("openai config default model is required")
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		// 不直接复用 http.DefaultClient：它的 Timeout 默认为 0，意味着调用方若传入
		// context.Background()，连接或响应读取可能无限阻塞。独立 client 也避免修改
		// 全局默认客户端后影响进程内其它 HTTP 调用。
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}

	return &Provider{
		baseURL:      baseURL,
		apiKey:       apiKey,
		defaultModel: defaultModel,
		httpClient:   httpClient,
		capabilities: cloneCapabilities(config.Capabilities),
	}, nil
}

// Name 返回 provider 稳定标识。
//
// 这个值会进入 trace、限流 key、健康检查和后续多 provider failover 日志，不能使用
// baseURL 或模型名这类可能包含环境细节、也可能随部署变化的字段。
func (p *Provider) Name() string {
	return providerName
}

// Capabilities 返回模型能力声明。
//
// P0 不做运行时能力探测：当前 adapter 已完整实现普通/流式 tool calling、
// streaming 与 strict structured output，因此默认声明这些协议能力；prompt caching、
// vision 等仍依赖具体模型或供应商的能力保持关闭。
func (p *Provider) Capabilities(model string) llm.ProviderCapabilities {
	if p == nil {
		return llm.ProviderCapabilities{}
	}
	if capability, ok := p.capabilities[model]; ok {
		return capability
	}
	return defaultCapabilities()
}

// Chat 目前只先保留请求边界校验；HTTP 请求映射与响应解析分别由 T030/T031 实现。
func (p *Provider) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if err := validateChatRequest(req); err != nil {
		return nil, err
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	resp, err := p.doChatRequest(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseChatResponse(resp)
}

// ChatStream 发起 OpenAI-compatible SSE 请求，并返回按序关闭的 chunk channel。
func (p *Provider) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	if err := validateChatRequest(req); err != nil {
		return nil, err
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	resp, err := p.doChatRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if err := classifyHTTPStatusError(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}

	chunks := make(chan llm.ChatChunk, 1)
	go streamChatChunks(ctx, resp.Body, chunks)
	return chunks, nil
}

func normalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("openai config baseURL is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("openai config baseURL is invalid: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("openai config baseURL must include scheme and host")
	}

	return strings.TrimRight(trimmed, "/"), nil
}

func cloneCapabilities(source map[string]llm.ProviderCapabilities) map[string]llm.ProviderCapabilities {
	if len(source) == 0 {
		return map[string]llm.ProviderCapabilities{}
	}

	cloned := make(map[string]llm.ProviderCapabilities, len(source))
	maps.Copy(cloned, source)
	return cloned
}

func defaultCapabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{
		ToolCalling:         true,
		StrictStructuredOut: true,
		Streaming:           true,
		StreamingToolCall:   true,
	}
}

func validateChatRequest(req *llm.ChatRequest) error {
	if req == nil {
		return fmt.Errorf("openai chat request is required")
	}
	if strings.TrimSpace(req.Model) == "" {
		return fmt.Errorf("openai chat request model is required")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("openai chat request messages are required")
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}

	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("openai request deadline exceeded: %w", errors.Join(llm.ErrUpstream, err))
	}
	return err
}

func (p *Provider) doChatRequest(ctx context.Context, req *llm.ChatRequest, stream bool) (*http.Response, error) {
	payload := mapChatRequest(req, stream)

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode openai chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+chatCompletionsPath, &body)
	if err != nil {
		return nil, fmt.Errorf("create openai chat request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("send openai chat request: %w", errors.Join(llm.ErrUpstream, err))
	}
	return resp, nil
}

func mapChatRequest(req *llm.ChatRequest, stream bool) openAIChatRequest {
	mapped := openAIChatRequest{
		Model:           strings.TrimSpace(req.Model),
		Messages:        mapMessages(req.Messages),
		Tools:           mapTools(req.Tools),
		ResponseFormat:  mapStructuredOutput(req.StructuredOutput),
		ReasoningEffort: strings.TrimSpace(req.ReasoningEffort),
		Stream:          stream,
	}
	mapped.Temperature = req.Temperature
	if req.MaxTokens != 0 {
		mapped.MaxTokens = req.MaxTokens
	}
	return mapped
}

func mapMessages(messages []llm.Message) []openAIMessage {
	mapped := make([]openAIMessage, 0, len(messages))
	for _, message := range messages {
		mapped = append(mapped, openAIMessage{
			Role:       string(message.Role),
			Content:    message.Content,
			Name:       strings.TrimSpace(message.Name),
			ToolCallID: strings.TrimSpace(message.ToolCallID),
		})
	}
	return mapped
}

func mapTools(tools []llm.Tool) []openAITool {
	if len(tools) == 0 {
		return nil
	}

	mapped := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		mapped = append(mapped, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
				Strict:      tool.Strict,
			},
		})
	}
	return mapped
}

func mapStructuredOutput(output *llm.StructuredOutput) *openAIResponseFormat {
	if output == nil {
		return nil
	}

	return &openAIResponseFormat{
		Type: "json_schema",
		JSONSchema: openAIJSONSchema{
			Name:   output.Name,
			Schema: output.Schema,
			Strict: output.Strict,
		},
	}
}

type openAIChatRequest struct {
	Model           string                `json:"model"`
	Messages        []openAIMessage       `json:"messages"`
	Tools           []openAITool          `json:"tools,omitempty"`
	ResponseFormat  *openAIResponseFormat `json:"response_format,omitempty"`
	ReasoningEffort string                `json:"reasoning_effort,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxTokens       int                   `json:"max_tokens,omitempty"`
	Stream          bool                  `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content,omitempty"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

type openAIResponseFormat struct {
	Type       string           `json:"type"`
	JSONSchema openAIJSONSchema `json:"json_schema"`
}

type openAIJSONSchema struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict"`
}

type openAIChatResponse struct {
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

type openAIChoice struct {
	Message      openAIResponseMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type openAIResponseMessage struct {
	Content   string           `json:"content"`
	ToolCalls []openAIToolCall `json:"tool_calls"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func parseChatResponse(resp *http.Response) (*llm.ChatResponse, error) {
	if err := classifyHTTPStatusError(resp); err != nil {
		return nil, err
	}

	var decoded openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode openai chat response: %w", err)
	}

	result := &llm.ChatResponse{
		Model: decoded.Model,
		Usage: llm.Usage{
			InputTokens:  decoded.Usage.PromptTokens,
			OutputTokens: decoded.Usage.CompletionTokens,
			TotalTokens:  decoded.Usage.TotalTokens,
		},
	}
	if len(decoded.Choices) == 0 {
		return result, nil
	}

	choice := decoded.Choices[0]
	result.Content = choice.Message.Content
	result.FinishReason = mapFinishReason(choice.FinishReason)

	toolCalls, err := mapToolCalls(choice.Message.ToolCalls)
	if err != nil {
		return nil, err
	}
	result.ToolCalls = toolCalls
	return result, nil
}

func mapFinishReason(reason string) llm.FinishReason {
	switch reason {
	case string(llm.FinishStop):
		return llm.FinishStop
	case string(llm.FinishLength):
		return llm.FinishLength
	case string(llm.FinishToolCall):
		return llm.FinishToolCall
	case string(llm.FinishContentFilter):
		return llm.FinishContentFilter
	default:
		return llm.FinishReason(reason)
	}
}

func mapToolCalls(calls []openAIToolCall) ([]llm.ToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	mapped := make([]llm.ToolCall, 0, len(calls))
	for _, call := range calls {
		arguments, err := decodeToolArguments(call.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("decode openai tool call %q arguments: %w", call.ID, err)
		}
		mapped = append(mapped, llm.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
	}
	return mapped, nil
}

func decodeToolArguments(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}

	var arguments map[string]any
	if err := json.Unmarshal([]byte(trimmed), &arguments); err != nil {
		return nil, err
	}
	return arguments, nil
}
