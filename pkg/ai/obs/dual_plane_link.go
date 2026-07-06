package obs

import (
	"fmt"
	"strings"
)

// DualPlaneLinkInput 是双平面关联层的最小输入。
//
// 这里刻意不从 context、trace payload 或平台返回值里猜身份：基础设施平面、
// AI 语义平面和 eval evidence 要能闭环，必须依赖调用链显式传入的稳定身份。
type DualPlaneLinkInput struct {
	Identity        CorrelationIdentity
	AIObservationID string
	EvalSampleID    string
}

// DualPlaneHTTPParentLink 描述基础设施平面的父 span 入口。
type DualPlaneHTTPParentLink struct {
	RequestID      string
	ServiceTraceID string
	SpanID         string
}

// DualPlaneAIChildLink 描述 AI 语义观测对基础设施 span 的子关联。
type DualPlaneAIChildLink struct {
	RequestID      string
	ServiceTraceID string
	ParentSpanID   string
	AITraceID      string
	ObservationID  string
}

// DualPlaneEvalLink 描述 eval evidence 到请求、基础 span 和 AI trace 的回链。
type DualPlaneEvalLink struct {
	EvalRunID      string
	SampleID       string
	RequestID      string
	AITraceID      string
	ServiceTraceID string
	ParentSpanID   string
}

// DualPlaneLinks 是一次请求在双平面观测体系中的可回查关联快照。
type DualPlaneLinks struct {
	HTTPParent DualPlaneHTTPParentLink
	AIChild    DualPlaneAIChildLink
	EvalLink   DualPlaneEvalLink
}

// BuildDualPlaneLinks 构建基础设施平面、AI 语义平面与 eval evidence 的关联关系。
//
// v1 的规则很克制：只复制已确认的事实身份，缺任何关键字段都 fail fast。这样
// 后续真实 OTel/Langfuse adapter 即使映射格式不同，也不能悄悄制造不存在的链路。
func BuildDualPlaneLinks(input DualPlaneLinkInput) (DualPlaneLinks, error) {
	if err := validateDualPlaneLinkInput(input); err != nil {
		return DualPlaneLinks{}, err
	}

	identity := input.Identity
	return DualPlaneLinks{
		HTTPParent: DualPlaneHTTPParentLink{
			RequestID:      identity.RequestID,
			ServiceTraceID: identity.ServiceTraceID,
			SpanID:         identity.SpanID,
		},
		AIChild: DualPlaneAIChildLink{
			RequestID:      identity.RequestID,
			ServiceTraceID: identity.ServiceTraceID,
			ParentSpanID:   identity.SpanID,
			AITraceID:      identity.AITraceID,
			ObservationID:  input.AIObservationID,
		},
		EvalLink: DualPlaneEvalLink{
			EvalRunID:      identity.EvalRunID,
			SampleID:       input.EvalSampleID,
			RequestID:      identity.RequestID,
			AITraceID:      identity.AITraceID,
			ServiceTraceID: identity.ServiceTraceID,
			ParentSpanID:   identity.SpanID,
		},
	}, nil
}

func validateDualPlaneLinkInput(input DualPlaneLinkInput) error {
	identity := input.Identity
	if strings.TrimSpace(identity.RequestID) == "" {
		return fmt.Errorf("dual plane link request_id is required")
	}
	if strings.TrimSpace(identity.ServiceTraceID) == "" {
		return fmt.Errorf("dual plane link service_trace_id is required")
	}
	if strings.TrimSpace(identity.SpanID) == "" {
		return fmt.Errorf("dual plane link span_id is required")
	}
	if strings.TrimSpace(identity.AITraceID) == "" {
		return fmt.Errorf("dual plane link ai_trace_id is required")
	}
	if strings.TrimSpace(input.AIObservationID) == "" {
		return fmt.Errorf("dual plane link ai_observation_id is required")
	}
	if strings.TrimSpace(identity.EvalRunID) == "" {
		return fmt.Errorf("dual plane link eval_run_id is required")
	}
	if strings.TrimSpace(input.EvalSampleID) == "" {
		return fmt.Errorf("dual plane link eval_sample_id is required")
	}
	return nil
}
