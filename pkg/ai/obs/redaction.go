package obs

import (
	"regexp"
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
	"raw_output":        {},
	"raw_query":         {},
	"raw_response":      {},
	"response_content":  {},
	"tool_args":         {},
	"tool_arguments":    {},
}

// sensitivePayloadPatterns 既用于判定也用于去敏，避免不同出口维护两套凭据/PII
// 规则。模式匹配到的是值本身；调用方不得把匹配结果或原文写入错误、日志或报告。
var sensitivePayloadPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bauthorization\s*:\s*(?:bearer|basic|token)\s+[^\s,;]+`),
	regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`),
	regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/=_-]+`),
	regexp.MustCompile(`(?i)\btoken(?:\s+|\s*[:=]\s*)[A-Za-z0-9._~+/-]+`),
	regexp.MustCompile(`(?i)\b(?:x-)?api[_ -]?key\s*[:=]\s*[^\s,;]+`),
	regexp.MustCompile(`(?i)\b(?:cookie|set-cookie|session(?:_id)?)\s*[:=]\s*[^\s,;]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
	regexp.MustCompile(`\b1[3-9][0-9]{9}\b`),
	regexp.MustCompile(`\b[0-9]{17}[0-9Xx]\b`),
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
	for _, pattern := range sensitivePayloadPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}

	sensitiveFragments := []string{
		"bearer ",
		"external_response",
		"password",
		"prompt:",
		"raw query",
		"raw output",
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

	return false
}

// RedactSensitivePayloadText 生成仍可用于受控调试的低敏文本。无法通过稳定模式
// 去敏的敏感值会被调用方整体丢弃，而不是带着猜测结果继续外发。
func RedactSensitivePayloadText(value string) string {
	redacted := value
	for _, pattern := range sensitivePayloadPatterns {
		redacted = pattern.ReplaceAllString(redacted, "[REDACTED]")
	}
	redacted = strings.TrimSpace(redacted)
	if ContainsSensitivePayloadValue(redacted) {
		return ""
	}
	return redacted
}
