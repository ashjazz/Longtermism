package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
)

type openAIErrorResponse struct {
	Error openAIErrorBody `json:"error"`
}

type openAIErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// classifyHTTPStatusError 把 OpenAI-compatible HTTP 状态映射到框架错误语义。
//
// 429/5xx 属于可重试或可降级的上游错误，必须包装 llm.ErrUpstream；
// 400/401/403 这类调用方、认证或权限问题不能进入重试/熔断路径。
// 错误文本只保留 status/type/code/message，不包含 Authorization header 或 API key。
func classifyHTTPStatusError(resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	body := decodeErrorBody(resp)
	message := buildHTTPErrorMessage(resp.StatusCode, body)
	if isRetryableHTTPStatus(resp.StatusCode) {
		return fmt.Errorf("%s: %w", message, llm.ErrUpstream)
	}
	return errors.New(message)
}

func decodeErrorBody(resp *http.Response) openAIErrorBody {
	if resp == nil || resp.Body == nil {
		return openAIErrorBody{}
	}

	var decoded openAIErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return openAIErrorBody{}
	}
	return decoded.Error
}

func buildHTTPErrorMessage(statusCode int, body openAIErrorBody) string {
	parts := []string{
		fmt.Sprintf("openai chat request failed with status %d", statusCode),
	}
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

func isRetryableHTTPStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
}
