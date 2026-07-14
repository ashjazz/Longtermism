package obs

import (
	"encoding/json"
	"errors"
	"strings"
)

// PayloadMode 声明观测内容允许进入受控快照的程度。它不改变密钥、token 或 PII
// 的禁止规则：这些内容在任何 mode 下均不得进入 trace、log、queue 或 report。
type PayloadMode string

const (
	PayloadModeMetadataOnly    PayloadMode = "metadata_only"
	PayloadModeContentRedacted PayloadMode = "content_redacted"
	PayloadModeContentRaw      PayloadMode = "content_raw"

	payloadEnvironmentLocal = "local"
	payloadEnvironmentTest  = "test"
)

// PayloadPolicyInput 是应用层传入核心包的最小配置。Debug 只服务响应诊断，不能
// 提升 payload mode，也不能关闭敏感内容检测。
type PayloadPolicyInput struct {
	Mode        PayloadMode
	Environment string
	Debug       bool
}

// PayloadPolicy 是不可变的内容出口策略快照。
type PayloadPolicy struct {
	mode     PayloadMode
	resolved bool
}

// Mode 返回已解析策略的模式。零值策略没有有效模式，调用方必须通过
// ResolvePayloadPolicy 获得可用策略，避免直接构造 content_raw 绕过环境校验。
func (policy PayloadPolicy) Mode() PayloadMode {
	return policy.mode
}

// PayloadContent 是尚未离开应用边界的原始候选内容。
type PayloadContent struct {
	Input         string
	Output        string
	Authorization string
	UserReference string
	ToolArguments string
}

// PayloadSnapshot 是可以继续传递到观测管道的内容快照。
// Authorization、用户引用和 tool arguments 故意不在此类型中，避免调用方把这些
// 高风险字段“误以为已脱敏”后再次外发。
type PayloadSnapshot struct {
	input  string
	output string
}

// Input 返回由 policy 生成的低敏输入快照。没有公开字段，调用方无法直接构造携带
// 原文的 PayloadSnapshot 并绕过 Sanitize。
func (snapshot PayloadSnapshot) Input() string {
	return snapshot.input
}

// Output 返回由 policy 生成的低敏输出快照。
func (snapshot PayloadSnapshot) Output() string {
	return snapshot.output
}

// MarshalJSON 只序列化由 policy 创建的低敏字段。tool arguments、Authorization 和
// user reference 没有输出通道，因此无法被快照序列化。
func (snapshot PayloadSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Input  string `json:"input,omitempty"`
		Output string `json:"output,omitempty"`
	}{
		Input:  snapshot.input,
		Output: snapshot.output,
	})
}

// ResolvePayloadPolicy 验证内容策略。content_raw 只能在隔离的 local/test 环境中
// 使用，生产环境始终 fail-fast；Debug 不参与任何权限判断。
func ResolvePayloadPolicy(input PayloadPolicyInput) (PayloadPolicy, error) {
	switch input.Mode {
	case PayloadModeMetadataOnly, PayloadModeContentRedacted:
		return PayloadPolicy{mode: input.Mode, resolved: true}, nil
	case PayloadModeContentRaw:
		environment := strings.ToLower(strings.TrimSpace(input.Environment))
		if environment != payloadEnvironmentLocal && environment != payloadEnvironmentTest {
			return PayloadPolicy{}, errors.New("payload mode content_raw requires an isolated local or test environment")
		}
		return PayloadPolicy{mode: input.Mode, resolved: true}, nil
	default:
		return PayloadPolicy{}, errors.New("payload mode is unsupported")
	}
}

// Sanitize 将候选内容变为可观测快照。应用侧先做这一步，保证敏感值不会先进入
// Collector 持久队列再等待下游过滤；raw 只保留普通文本，绝不是敏感检测的旁路。
func (policy PayloadPolicy) Sanitize(content PayloadContent) PayloadSnapshot {
	if !policy.resolved || policy.mode == PayloadModeMetadataOnly {
		return PayloadSnapshot{}
	}

	return PayloadSnapshot{
		input:  sanitizePayloadText(content.Input),
		output: sanitizePayloadText(content.Output),
	}
}

func sanitizePayloadText(value string) string {
	return RedactSensitivePayloadText(value)
}
