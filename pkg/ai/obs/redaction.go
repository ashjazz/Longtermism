package obs

import (
	"sort"
	"strings"
)

const (
	ForbiddenPayloadReasonKey   = "forbidden_key"
	ForbiddenPayloadReasonValue = "sensitive_value"
)

// ForbiddenPayloadFinding 描述普通观测 payload 中被拒绝的字段。
//
// finding 只保留字段名和原因，刻意不携带 value。隐私扫描最容易犯的错误是“发现了
// 敏感值，然后在错误、日志或测试输出里又把它打印出来”；这里从类型层面避免回显。
type ForbiddenPayloadFinding struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

var forbiddenPayloadKeys = map[string]struct{}{
	"api_key":           {},
	"authorization":     {},
	"external_response": {},
	"jwt":               {},
	"password":          {},
	"prompt":            {},
	"prompt_content":    {},
	"raw_query":         {},
	"tool_args":         {},
	"tool_arguments":    {},
}

// ScanForbiddenPayloadFields 扫描普通观测 payload 中禁止外发的 key/value。
//
// 输入 map 不会被修改；返回结果按 key 稳定排序，便于测试、CI 和后续 smoke 输出比较。
// 这个 helper 不负责“脱敏后继续发送”，只负责判断普通观测面是否应该拒绝某个字段。
func ScanForbiddenPayloadFields(fields map[string]string) []ForbiddenPayloadFinding {
	if len(fields) == 0 {
		return nil
	}

	findings := make([]ForbiddenPayloadFinding, 0)
	for key, value := range fields {
		switch {
		case isForbiddenPayloadKey(key):
			findings = append(findings, ForbiddenPayloadFinding{
				Key:    key,
				Reason: ForbiddenPayloadReasonKey,
			})
		case ContainsSensitivePayloadValue(value):
			findings = append(findings, ForbiddenPayloadFinding{
				Key:    key,
				Reason: ForbiddenPayloadReasonValue,
			})
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Key < findings[j].Key
	})
	return findings
}

func isForbiddenPayloadKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	_, forbidden := forbiddenPayloadKeys[normalized]
	return forbidden
}

// ContainsSensitivePayloadValue 判断字符串值是否像敏感原文或凭据。
//
// v1 采用保守启发式，覆盖本阶段明确要求的 query/prompt/tool args/API key/JWT/
// password/external response。后续若接入真实 DLP 或 PII detector，应在这里替换或扩展。
func ContainsSensitivePayloadValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}

	sensitiveFragments := []string{
		"bearer ",
		"external_response",
		"password",
		"prompt:",
		"raw query",
		"sk-",
		"system prompt",
		"tool_args",
		"用户问题",
		"用户原文",
		"完整 prompt",
	}
	for _, fragment := range sensitiveFragments {
		if strings.Contains(normalized, strings.ToLower(fragment)) {
			return true
		}
	}

	return looksLikeJWT(value) || looksLikeEmail(value) || looksLikePhoneNumber(value) || looksLikeChineseNationalID(value)
}

func looksLikeJWT(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	return strings.HasPrefix(parts[0], "eyJ")
}

func looksLikeEmail(value string) bool {
	trimmed := strings.TrimSpace(value)
	at := strings.Index(trimmed, "@")
	if at <= 0 || at == len(trimmed)-1 {
		return false
	}
	return strings.Contains(trimmed[at+1:], ".")
}

func looksLikePhoneNumber(value string) bool {
	digits := digitsOnly(value)
	return len(digits) == 11 && strings.HasPrefix(digits, "1")
}

func looksLikeChineseNationalID(value string) bool {
	digits := digitsOnly(value)
	return len(digits) == 18
}

func digitsOnly(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
