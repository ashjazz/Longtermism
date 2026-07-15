package obs

import (
	"fmt"
	"strings"
)

const (
	BaggageRequestID      = "request_id"
	BaggageServiceTraceID = "service_trace_id"
	BaggageSpanID         = "span_id"
	BaggageAITraceID      = "ai_trace_id"
	BaggageSessionID      = "session_id"
	BaggageEvalRunID      = "eval_run_id"
)

var allowedBaggageKeys = map[string]struct{}{
	BaggageRequestID:      {},
	BaggageServiceTraceID: {},
	BaggageSpanID:         {},
	BaggageAITraceID:      {},
	BaggageSessionID:      {},
	BaggageEvalRunID:      {},
}

var forbiddenBaggageKeys = map[string]struct{}{
	"api_key":           {},
	"authorization":     {},
	"external_response": {},
	"password":          {},
	"prompt":            {},
	"prompt_content":    {},
	"raw_query":         {},
	"tool_args":         {},
	"tool_arguments":    {},
}

// BaggageFieldsFromCorrelationIdentity 生成允许跨服务传播的低敏关联字段。
//
// baggage 会被基础设施平面用于跨进程传播，因此只能放稳定身份摘要，不能放
// query、prompt、tool 参数或外部响应。返回 map 是新分配的，调用方修改它不会
// 影响后续从同一 identity 再生成的字段。
func BaggageFieldsFromCorrelationIdentity(identity CorrelationIdentity) (map[string]string, error) {
	fields := map[string]string{
		BaggageRequestID:      identity.RequestID,
		BaggageServiceTraceID: identity.ServiceTraceID,
		BaggageSpanID:         identity.SpanID,
		BaggageAITraceID:      identity.AITraceID,
		BaggageSessionID:      identity.SessionID,
		BaggageEvalRunID:      identity.EvalRunID,
	}

	for key, value := range fields {
		if err := ValidateBaggageFieldSafety(key, value); err != nil {
			return nil, err
		}
	}

	return fields, nil
}

// ValidateBaggageFieldSafety 校验单个 baggage 字段是否满足核心层的基础安全规则。
//
// 它不是应用出口的最终传播策略：应用层可以在此基础上采用更窄的 allowlist，例如
// 不重复传播 OTel trace/span ID，或默认关闭 session_id。后续若需要传递租户 hash
// 或 feature 等低敏字段，必须先在这里通过安全审查，再由具体出口显式启用。
func ValidateBaggageFieldSafety(key, value string) error {
	normalizedKey := strings.ToLower(strings.TrimSpace(key))
	if normalizedKey == "" {
		return fmt.Errorf("baggage field key is empty")
	}
	if _, forbidden := forbiddenBaggageKeys[normalizedKey]; forbidden {
		return fmt.Errorf("baggage field %q is forbidden", key)
	}
	if _, allowed := allowedBaggageKeys[normalizedKey]; !allowed {
		return fmt.Errorf("baggage field %q is not in allowlist", key)
	}
	if ContainsSensitivePayloadValue(value) {
		return fmt.Errorf("baggage field %q contains sensitive value", key)
	}
	return nil
}
