package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

// === US5：controlled payload canary 扫描（T156，使 T152 GREEN）===
//
// 本文件实现受控验证的隐私边界证明：把合成敏感哨兵（synthetic secret/PII 与
// 业务原文）注入受控链路，然后证明三种生产 payload policy 下，进入观测外发面
// 的内容对哨兵零命中。命中即立即失败，输出只含 surface/类别/计数——检测报告
// 绝不回显命中的原文。

// platformPrivacyCategoryRawCanary 标记"canary 原文直接出现在扫描面"的命中。
// 与生产 scanner 的 forbidden_key/sensitive_value 类别并存：前者证明外发面
// 含哨兵原文，后者证明面内容命中生产敏感模式，两类证据互补。
const platformPrivacyCategoryRawCanary = "raw_canary"

// PlatformPrivacyCanary 是一组注入受控验证的合成敏感哨兵。
//
// 这些值不是任何真实环境的凭据；T152 的锚点断言保证它们持续命中生产 scanner
// 的判定模式，防止"clean"结论退化成假阴性。
type PlatformPrivacyCanary struct {
	APIKey    string
	JWT       string
	Password  string
	MobilePII string
	RawQuery  string
	Prompt    string
}

// PlatformPrivacySurface 是一个受控扫描输出面。ExtraSurfaces 让测试与后续
// adapter 把额外渲染面纳入同一扫描机制，而不是绕过 scanner 自行检查。
type PlatformPrivacySurface struct {
	Name    string
	Payload string
}

// PlatformPrivacyScanConfig 描述一次 platform payload 隐私扫描。
//
// PayloadMode/Environment/RawContentEnabled 三项直接交给生产
// ResolvePayloadPolicy 校验：policy 语义的单一来源在生产包，本扫描不维护
// 第二套模式规则。
type PlatformPrivacyScanConfig struct {
	PayloadMode       obs.PayloadMode
	Environment       string
	RawContentEnabled bool
	Identity          obs.CorrelationIdentity
	Canary            PlatformPrivacyCanary
	ExtraSurfaces     []PlatformPrivacySurface
}

// PlatformPrivacyFinding 是一次零原文的命中摘要。
//
// 与生产 ForbiddenPayloadFinding 同理：finding 从类型层面不携带命中的值，
// 避免"发现了敏感值，然后在错误、日志或报告里又把它打印出来"。
type PlatformPrivacyFinding struct {
	Surface  string
	Category string
	Count    int
}

// PlatformPrivacyScanResult 是扫描的低敏结果。
type PlatformPrivacyScanResult struct {
	Clean           bool
	PolicyMode      obs.PayloadMode
	ScannedSurfaces []string
	Findings        []PlatformPrivacyFinding
}

// RunPlatformPayloadPrivacyScan 执行受控 payload 隐私扫描。
//
// 流程：生产 policy 解析 → 哨兵内容经生产 Sanitize 生成快照 → 组装三个内置
// 扫描面（正式 JSON、debug 渲染、baggage）加 ExtraSurfaces → 对每个面做
// canary marker 逐值扫描与生产模式扫描。任何命中都返回 error（立即失败），
// error 与结果都只含 surface/类别/计数。
func RunPlatformPayloadPrivacyScan(ctx context.Context, config PlatformPrivacyScanConfig) (PlatformPrivacyScanResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PlatformPrivacyScanResult{}, err
	}

	// 生产 policy 校验：未声明 mode、非法 mode 或 content_raw 缺少授权/环境
	// 都在这里 fail-fast，错误文案（如 "payload mode is unsupported"）直接
	// 来自生产，保证两个包对同一配置永远给出同一判定。
	policy, err := obs.ResolvePayloadPolicy(obs.PayloadPolicyInput{
		Mode:              config.PayloadMode,
		Environment:       config.Environment,
		RawContentEnabled: config.RawContentEnabled,
	})
	if err != nil {
		return PlatformPrivacyScanResult{Clean: false}, err
	}

	// 受控内容候选携带业务原文哨兵。content_raw 授权时生产 policy 会允许
	// LocalRawPayload（本地受限调试工件）——它只允许交给本地 sink，本扫描
	// 不把它放入任何外发面，也不对它做任何复制。
	content := obs.PayloadContent{
		Input:  config.Canary.RawQuery,
		Output: config.Canary.Prompt,
	}
	snapshot := policy.Sanitize(content)

	surfaces, err := buildPlatformPrivacySurfaces(config, policy.Mode(), snapshot)
	if err != nil {
		return PlatformPrivacyScanResult{Clean: false}, err
	}

	findings := scanPlatformPrivacySurfaces(surfaces, config.Canary)
	scanned := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		scanned = append(scanned, surface.Name)
	}

	if len(findings) > 0 {
		return PlatformPrivacyScanResult{
			Clean:           false,
			PolicyMode:      policy.Mode(),
			ScannedSurfaces: scanned,
			Findings:        findings,
		}, fmt.Errorf("platform payload privacy scan found sensitive hits: %s", renderPlatformPrivacyFindings(findings))
	}

	return PlatformPrivacyScanResult{
		Clean:           true,
		PolicyMode:      policy.Mode(),
		ScannedSurfaces: scanned,
	}, nil
}

// platformPrivacyPayloadSummary 是受控观测 payload 的外发形态。
//
// 关键取舍：即使 content_redacted 允许"已去敏的普通文本"进入观测管道，受控
// 验证也只外发快照的长度摘要而不是快照文本——生产 RedactSensitivePayloadText
// 会保留不含敏感片段的普通文本（例如没有模式命中的 prompt 原文），把它放进
// 扫描面会让"零哨兵命中"依赖每个哨兵恰好命中某个模式。长度摘要让 policy 的
// 差异（metadata_only 归零、redacted/raw 保留内容）可见，同时保证外发面在
// 结构上就不携带业务原文。
type platformPrivacyPayloadSummary struct {
	RequestID             string `json:"request_id"`
	ServiceTraceID        string `json:"service_trace_id"`
	AITraceID             string `json:"ai_trace_id"`
	EvalRunID             string `json:"eval_run_id"`
	PayloadMode           string `json:"payload_mode"`
	InputSnapshotLength   int    `json:"input_snapshot_length"`
	OutputSnapshotLength  int    `json:"output_snapshot_length"`
	ExternalAttemptsFound bool   `json:"external_attempts_found"`
}

func buildPlatformPrivacySurfaces(config PlatformPrivacyScanConfig, mode obs.PayloadMode, snapshot obs.PayloadSnapshot) ([]PlatformPrivacySurface, error) {
	summary := platformPrivacyPayloadSummary{
		RequestID:             config.Identity.RequestID,
		ServiceTraceID:        config.Identity.ServiceTraceID,
		AITraceID:             config.Identity.AITraceID,
		EvalRunID:             config.Identity.EvalRunID,
		PayloadMode:           string(mode),
		InputSnapshotLength:   len([]rune(snapshot.Input())),
		OutputSnapshotLength:  len([]rune(snapshot.Output())),
		ExternalAttemptsFound: false,
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		return nil, fmt.Errorf("encode platform privacy payload summary: %w", err)
	}

	// baggage 面由生产 allowlist 构造：BaggageFieldsFromCorrelationIdentity 只
	// 产出六个身份键并对每个值做安全校验，本扫描不自行拼装 baggage。
	baggage, err := obs.BaggageFieldsFromCorrelationIdentity(config.Identity)
	if err != nil {
		return nil, fmt.Errorf("build platform privacy baggage fields: %w", err)
	}
	baggageEncoded, err := json.Marshal(baggage)
	if err != nil {
		return nil, fmt.Errorf("encode platform privacy baggage: %w", err)
	}

	surfaces := []PlatformPrivacySurface{
		{Name: "payload_json", Payload: string(encoded)},
		// debug 渲染是最容易被绕过 scanner 的出口：正式 JSON 干净而 %#v 带
		// 原文是真实的泄露路径，因此 debug 面与正式面同源同扫。
		{Name: "payload_debug", Payload: fmt.Sprintf("%#v", summary)},
		{Name: "baggage", Payload: string(baggageEncoded)},
	}
	return append(surfaces, config.ExtraSurfaces...), nil
}

// scanPlatformPrivacySurfaces 对每个面执行双重扫描：
//
//  1. canary marker 逐值扫描——证明哨兵原文没有以任何形式（整体或前缀）出现；
//  2. 生产 ScanForbiddenPayloadFields 模式扫描——证明即使哨兵被变换后重写，
//     面内容仍会被生产规则拦住。
//
// 两类命中都按 (surface, category) 聚合计数，不携带任何值。
func scanPlatformPrivacySurfaces(surfaces []PlatformPrivacySurface, canary PlatformPrivacyCanary) []PlatformPrivacyFinding {
	markers := []string{canary.APIKey, canary.JWT, canary.Password, canary.MobilePII, canary.RawQuery, canary.Prompt}

	counts := map[string]int{}
	key := func(surface, category string) string { return surface + "\x00" + category }

	for _, surface := range surfaces {
		for _, marker := range markers {
			if strings.TrimSpace(marker) == "" {
				continue
			}
			if strings.Contains(surface.Payload, marker) {
				counts[key(surface.Name, platformPrivacyCategoryRawCanary)]++
			}
		}
		for _, finding := range obs.ScanForbiddenPayloadFields(map[string]string{"payload": surface.Payload}) {
			counts[key(surface.Name, finding.Reason)]++
		}
	}

	findings := make([]PlatformPrivacyFinding, 0, len(counts))
	for composite, count := range counts {
		parts := strings.SplitN(composite, "\x00", 2)
		findings = append(findings, PlatformPrivacyFinding{
			Surface:  parts[0],
			Category: parts[1],
			Count:    count,
		})
	}
	// 稳定排序：错误输出可复现，测试与 CI 比较不会因 map 遍历序漂移。
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Surface != findings[j].Surface {
			return findings[i].Surface < findings[j].Surface
		}
		return findings[i].Category < findings[j].Category
	})
	return findings
}

// renderPlatformPrivacyFindings 生成零原文的命中摘要，形如
// "injected_surface(raw_canary=2, sensitive_value=1)"。
func renderPlatformPrivacyFindings(findings []PlatformPrivacyFinding) string {
	bySurface := map[string][]string{}
	order := make([]string, 0)
	for _, finding := range findings {
		if _, seen := bySurface[finding.Surface]; !seen {
			order = append(order, finding.Surface)
		}
		bySurface[finding.Surface] = append(bySurface[finding.Surface], fmt.Sprintf("%s=%d", finding.Category, finding.Count))
	}

	parts := make([]string, 0, len(order))
	for _, surface := range order {
		parts = append(parts, fmt.Sprintf("%s(%s)", surface, strings.Join(bySurface[surface], ", ")))
	}
	return strings.Join(parts, "; ")
}
