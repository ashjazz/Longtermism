package obs

import (
	"fmt"
	"strings"
)

// ObservationType 标识一条 AI 语义观测记录描述的阶段类型。
//
// 这些类型来自 Observability v1 契约：generation、retriever、tool、agent、
// evaluator。保持 string enum 可以让日志、span attribute 和平台 adapter 共享
// 同一组稳定值，同时避免把平台 SDK 类型泄露进核心包。
type ObservationType string

const (
	ObservationTypeGeneration ObservationType = "generation"
	ObservationTypeRetriever  ObservationType = "retriever"
	ObservationTypeTool       ObservationType = "tool"
	ObservationTypeAgent      ObservationType = "agent"
	ObservationTypeEvaluator  ObservationType = "evaluator"
)

var allowedObservationTypes = map[ObservationType]struct{}{
	ObservationTypeGeneration: {},
	ObservationTypeRetriever:  {},
	ObservationTypeTool:       {},
	ObservationTypeAgent:      {},
	ObservationTypeEvaluator:  {},
}

// String 返回 observation type 的稳定序列化值。
func (observationType ObservationType) String() string {
	return string(observationType)
}

// ValidateObservationType 校验 observation type 是否属于契约定义的稳定集合。
//
// 这里刻意不做大小写归一化：generation 和 Generation 代表不同输入质量。
// 快速失败能帮助 mapper 或调用方尽早发现字段拼写漂移。
func ValidateObservationType(observationType ObservationType) error {
	value := observationType.String()
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("observation type is empty")
	}
	if _, ok := allowedObservationTypes[observationType]; !ok {
		return fmt.Errorf("unknown observation type %q", value)
	}
	return nil
}
