package obs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// RequestObservationServiceStage 是请求链路里的基础设施平面阶段。
//
// 它表达 HTTP/service/db/cache 等传统服务事实，只保存低敏诊断字段。请求体、
// query string、header、外部响应原文都不应进入这里。
type RequestObservationServiceStage struct {
	Name           string
	Component      string
	RequestID      string
	ServiceTraceID string
	SpanID         string
	ParentSpanID   string
	Status         string
	ErrorClass     string
	LatencyMs      int64
}

// RequestObservationAIStage 是请求链路里的 AI 语义观测阶段。
//
// AITraceID 表示 AI 语义平面的 trace 身份，ParentSpanID 指回基础设施平面的
// root/current span，让两套平面可以在离线测试和真实平台里互相回查。
type RequestObservationAIStage struct {
	ObservationID   string
	ObservationType ObservationType
	RequestID       string
	ServiceTraceID  string
	ParentSpanID    string
	AITraceID       string
	OutcomeStatus   string
	FailureStatus   string
}

// RequestObservationEvalEvidence 描述 eval evidence 到请求和 AI trace 的回链。
type RequestObservationEvalEvidence struct {
	EvalRunID  string
	SampleID   string
	MetricName string
	RequestID  string
	AITraceID  string
}

// RequestObservationChainInput 是一次请求观测链路的写入 DTO。
type RequestObservationChainInput struct {
	Identity           CorrelationIdentity
	Feature            string
	StartedAt          time.Time
	EndedAt            time.Time
	OutcomeStatus      string
	FailureStatus      string
	OutcomeExplanation string
	ServiceStages      []RequestObservationServiceStage
	AIObservations     []RequestObservationAIStage
	EvalEvidence       []RequestObservationEvalEvidence
}

// RequestObservationChain 是按 request_id 组织好的完整事实链快照。
type RequestObservationChain struct {
	RequestID          string
	ServiceTraceID     string
	RootSpanID         string
	RootAITraceID      string
	SessionID          string
	EvalRunID          string
	Feature            string
	StartedAt          time.Time
	EndedAt            time.Time
	OutcomeStatus      string
	FailureStatus      string
	OutcomeExplanation string
	StageRefs          []string
	ServiceStages      []RequestObservationServiceStage
	AIObservations     []RequestObservationAIStage
	EvalEvidence       []RequestObservationEvalEvidence
}

// RequestObservationChainRecorder 保存可按 request_id 回查的请求链路快照。
//
// v1 先使用内存实现服务默认离线 smoke；真实平台 adapter 可以消费同一份 chain
// 快照做上报，但不应反过来决定核心字段。
type RequestObservationChainRecorder struct {
	mu      sync.RWMutex
	byReqID map[string]RequestObservationChain
}

// NewRequestObservationChainRecorder 创建空的请求链路记录器。
func NewRequestObservationChainRecorder() *RequestObservationChainRecorder {
	return &RequestObservationChainRecorder{
		byReqID: make(map[string]RequestObservationChain),
	}
}

// Record 组装并保存一条请求观测链路。
func (r *RequestObservationChainRecorder) Record(ctx context.Context, input RequestObservationChainInput) (RequestObservationChain, error) {
	if err := validateRequestObservationChainInput(ctx, input); err != nil {
		return RequestObservationChain{}, err
	}

	chain := buildRequestObservationChain(input)
	if r == nil {
		return chain, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.byReqID == nil {
		r.byReqID = make(map[string]RequestObservationChain)
	}
	r.byReqID[chain.RequestID] = cloneRequestObservationChain(chain)
	return cloneRequestObservationChain(chain), nil
}

// FindByRequestID 按 request_id 返回链路快照副本。
func (r *RequestObservationChainRecorder) FindByRequestID(requestID string) (RequestObservationChain, bool) {
	if r == nil {
		return RequestObservationChain{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	chain, ok := r.byReqID[strings.TrimSpace(requestID)]
	if !ok {
		return RequestObservationChain{}, false
	}
	return cloneRequestObservationChain(chain), true
}

func validateRequestObservationChainInput(ctx context.Context, input RequestObservationChainInput) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	identity := input.Identity
	if strings.TrimSpace(identity.RequestID) == "" {
		return fmt.Errorf("request observation chain request_id is required")
	}
	if strings.TrimSpace(identity.ServiceTraceID) == "" {
		return fmt.Errorf("request observation chain service_trace_id is required")
	}
	if strings.TrimSpace(identity.SpanID) == "" {
		return fmt.Errorf("request observation chain root span_id is required")
	}
	if strings.TrimSpace(identity.AITraceID) == "" {
		return fmt.Errorf("request observation chain ai_trace_id is required")
	}
	if strings.TrimSpace(input.Feature) == "" {
		return fmt.Errorf("request observation chain feature is required")
	}
	if len(input.ServiceStages) == 0 && len(input.AIObservations) == 0 && len(input.EvalEvidence) == 0 {
		return fmt.Errorf("request observation chain requires at least one stage")
	}
	if err := validateRequestObservationOutcome(input.OutcomeStatus); err != nil {
		return err
	}
	if requiresOutcomeExplanation(input.OutcomeStatus) && strings.TrimSpace(input.OutcomeExplanation) == "" {
		return fmt.Errorf("request observation chain outcome explanation is required for %q", input.OutcomeStatus)
	}
	return nil
}

func validateRequestObservationOutcome(outcomeStatus string) error {
	switch strings.TrimSpace(outcomeStatus) {
	case "success", "failure", "terminated", "degraded":
		return nil
	default:
		return fmt.Errorf("request observation chain outcome_status %q is not supported", outcomeStatus)
	}
}

func requiresOutcomeExplanation(outcomeStatus string) bool {
	switch strings.TrimSpace(outcomeStatus) {
	case "failure", "terminated", "degraded":
		return true
	default:
		return false
	}
}

func buildRequestObservationChain(input RequestObservationChainInput) RequestObservationChain {
	identity := input.Identity
	serviceStages := cloneRequestObservationServiceStages(input.ServiceStages)
	aiObservations := cloneRequestObservationAIStages(input.AIObservations)
	evalEvidence := cloneRequestObservationEvalEvidence(input.EvalEvidence)

	return RequestObservationChain{
		RequestID:          identity.RequestID,
		ServiceTraceID:     identity.ServiceTraceID,
		RootSpanID:         identity.SpanID,
		RootAITraceID:      identity.AITraceID,
		SessionID:          identity.SessionID,
		EvalRunID:          identity.EvalRunID,
		Feature:            input.Feature,
		StartedAt:          input.StartedAt,
		EndedAt:            input.EndedAt,
		OutcomeStatus:      input.OutcomeStatus,
		FailureStatus:      input.FailureStatus,
		OutcomeExplanation: input.OutcomeExplanation,
		StageRefs:          buildRequestObservationStageRefs(serviceStages, aiObservations, evalEvidence),
		ServiceStages:      serviceStages,
		AIObservations:     aiObservations,
		EvalEvidence:       evalEvidence,
	}
}

func buildRequestObservationStageRefs(serviceStages []RequestObservationServiceStage, aiObservations []RequestObservationAIStage, evalEvidence []RequestObservationEvalEvidence) []string {
	refs := make([]string, 0, len(serviceStages)+len(aiObservations)+len(evalEvidence))
	for _, stage := range serviceStages {
		refs = append(refs, "service:"+stage.Name)
	}
	for _, observation := range aiObservations {
		refs = append(refs, "ai:"+observation.ObservationType.String()+":"+observation.ObservationID)
	}
	for _, evidence := range evalEvidence {
		refs = append(refs, "eval:"+evidence.MetricName+":"+evidence.SampleID)
	}
	return refs
}

func cloneRequestObservationChain(chain RequestObservationChain) RequestObservationChain {
	cloned := chain
	cloned.StageRefs = append([]string(nil), chain.StageRefs...)
	cloned.ServiceStages = cloneRequestObservationServiceStages(chain.ServiceStages)
	cloned.AIObservations = cloneRequestObservationAIStages(chain.AIObservations)
	cloned.EvalEvidence = cloneRequestObservationEvalEvidence(chain.EvalEvidence)
	return cloned
}

func cloneRequestObservationServiceStages(stages []RequestObservationServiceStage) []RequestObservationServiceStage {
	if len(stages) == 0 {
		return nil
	}
	return append([]RequestObservationServiceStage(nil), stages...)
}

func cloneRequestObservationAIStages(stages []RequestObservationAIStage) []RequestObservationAIStage {
	if len(stages) == 0 {
		return nil
	}
	return append([]RequestObservationAIStage(nil), stages...)
}

func cloneRequestObservationEvalEvidence(evidence []RequestObservationEvalEvidence) []RequestObservationEvalEvidence {
	if len(evidence) == 0 {
		return nil
	}
	return append([]RequestObservationEvalEvidence(nil), evidence...)
}
