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

// PayloadPolicyInput 是应用层传入核心包的最小配置。RawContentEnabled 是独立的
// 明确授权，避免把“local 环境”错误地等同于“允许记录完整原文”。
type PayloadPolicyInput struct {
	Mode              PayloadMode
	Environment       string
	RawContentEnabled bool
}

// PayloadPolicy 是不可变的内容出口策略快照。
type PayloadPolicy struct {
	mode     PayloadMode
	resolved bool
}

// Mode 返回已解析策略的模式。零值策略没有有效模式，调用方必须通过
// ResolvePayloadPolicy 获得可用策略，避免直接构造未知模式绕过配置校验。
func (policy PayloadPolicy) Mode() PayloadMode {
	return policy.mode
}

// PayloadContent 是尚未离开应用边界的原始候选内容。
type PayloadContent struct {
	Input  string
	Output string
}

// PayloadSnapshot 是唯一可以继续传递到观测管道的内容快照。
// 高风险字段不属于 PayloadContent 或 PayloadSnapshot，避免调用方把它们“误以为
// 已脱敏”后再次外发。
type PayloadSnapshot struct {
	input  string
	output string
}

// LocalRawPayload 是仅供 local/test 调试查看的完整原文工件。它与可外发的
// PayloadSnapshot 分离：即使 raw mode 被显式开启，也不能把原文传给 OTel、日志、
// Collector、queue、Langfuse、报告或 evidence。
type LocalRawPayload struct {
	input  string
	output string
}

// Input 返回完整的本地调试输入。调用方必须仅将它交给受限的本地 sink 或测试内存
// sink；不得写入 stdout、日志或外部观测后端。
func (payload LocalRawPayload) Input() string {
	return payload.input
}

// Output 返回完整的本地调试输出；其处理边界与 Input 相同。
func (payload LocalRawPayload) Output() string {
	return payload.output
}

// MarshalJSON 明确拒绝序列化，防止正常的 JSON exporter、报告或日志路径误收原文。
func (payload LocalRawPayload) MarshalJSON() ([]byte, error) {
	return nil, errors.New("local raw payload must not be serialized")
}

// String 阻止 fmt 和常见日志 fallback 反射私有字段时泄露完整原文。
func (payload LocalRawPayload) String() string {
	return "LocalRawPayload(redacted)"
}

// GoString 同样保护 %#v；它与 String 保持固定、低敏的诊断文本。
func (payload LocalRawPayload) GoString() string {
	return "obs.LocalRawPayload(redacted)"
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

// ResolvePayloadPolicy 验证内容策略。content_raw 只允许 local/test 加显式授权；
// 它仅解锁 LocalRawPayload，不会改变标准观测快照的脱敏与外发边界。
func ResolvePayloadPolicy(input PayloadPolicyInput) (PayloadPolicy, error) {
	if input.RawContentEnabled && input.Mode != PayloadModeContentRaw {
		return PayloadPolicy{}, errors.New("raw content opt in requires content_raw mode")
	}

	switch input.Mode {
	case PayloadModeMetadataOnly, PayloadModeContentRedacted:
		return PayloadPolicy{mode: input.Mode, resolved: true}, nil
	case PayloadModeContentRaw:
		if !input.RawContentEnabled {
			return PayloadPolicy{}, errors.New("raw content mode requires explicit opt in")
		}
		if !isLocalOrTestEnvironment(input.Environment) {
			return PayloadPolicy{}, errors.New("raw content mode requires a local or test environment")
		}
		return PayloadPolicy{mode: input.Mode, resolved: true}, nil
	default:
		return PayloadPolicy{}, errors.New("payload mode is unsupported")
	}
}

// Sanitize 将候选内容变为可观测快照。应用侧先做这一步，保证敏感值不会先进入
// Collector 持久队列再等待下游过滤；redacted 只保留已去敏的普通文本，绝不是
// 敏感检测的旁路。
func (policy PayloadPolicy) Sanitize(content PayloadContent) PayloadSnapshot {
	if !policy.resolved || policy.mode == PayloadModeMetadataOnly {
		return PayloadSnapshot{}
	}

	return PayloadSnapshot{
		input:  sanitizePayloadText(content.Input),
		output: sanitizePayloadText(content.Output),
	}
}

// LocalRawPayload 只在已解析的 content_raw policy 下返回完整输入输出。标准
// PayloadSnapshot 仍应通过 Sanitize 获取，以维持 exporter 前的脱敏边界。
func (policy PayloadPolicy) LocalRawPayload(content PayloadContent) (LocalRawPayload, error) {
	if !policy.resolved || policy.mode != PayloadModeContentRaw {
		return LocalRawPayload{}, errors.New("local raw payload requires an enabled content_raw policy")
	}
	return LocalRawPayload{input: content.Input, output: content.Output}, nil
}

func isLocalOrTestEnvironment(environment string) bool {
	normalized := strings.ToLower(strings.TrimSpace(environment))
	return normalized == payloadEnvironmentLocal || normalized == payloadEnvironmentTest
}

func sanitizePayloadText(value string) string {
	return RedactSensitivePayloadText(value)
}
