package langfuse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

const (
	projectionIDDomain         = "longtermism:langfuse-score:v1"
	maxProjectionFactBytes     = 256
	maxScoreProjectionAttempts = 100
)

var (
	errInvalidScoreTarget     = errors.New("score projection target is invalid")
	errInvalidScoreEvidence   = errors.New("score projection evidence is invalid")
	errInvalidProjectionInput = errors.New("score projection input is invalid")
	errInvalidTransition      = errors.New("score projection transition is invalid")
)

// ScoreTargetKind 区分 trace 级 score 与某个 generation observation 的 score。
// 身份来源始终是 mapper 产物，不能由名称或查询时间窗口推断。
type ScoreTargetKind string

const (
	ScoreTargetKindTrace       ScoreTargetKind = "trace"
	ScoreTargetKindObservation ScoreTargetKind = "observation"
)

// ScoreTarget 把 platform identity 与 mapper provenance 封装为不可拆分的值对象。
// 所有字段保持私有，避免校验后被替换为另一个格式合法但来源错误的 ID。
type ScoreTarget struct {
	kind                  ScoreTargetKind
	platformTraceID       string
	platformObservationID string
	mapped                bool
}

func (target ScoreTarget) Kind() ScoreTargetKind {
	return target.kind
}

func (target ScoreTarget) PlatformTraceID() string {
	return target.platformTraceID
}

func (target ScoreTarget) PlatformObservationID() string {
	return target.platformObservationID
}

// NewScoreTarget 只接受 T097 mapper 证明过的原生 OTel identity。领域 ai_trace_id
// 即使存在，也只能作为关联 metadata，绝不能成为平台 score target 的 fallback。
func NewScoreTarget(source TraceProjection, kind ScoreTargetKind) (ScoreTarget, error) {
	if !source.mapped || !isValidNativeIdentity(source.platformTraceID, source.platformObservationID) {
		return ScoreTarget{}, errInvalidScoreTarget
	}
	return newScoreTarget(source.platformTraceID, source.platformObservationID, kind)
}

func newScoreTarget(traceID, observationID string, kind ScoreTargetKind) (ScoreTarget, error) {
	traceID = strings.ToLower(traceID)
	observationID = strings.ToLower(observationID)

	switch kind {
	case ScoreTargetKindTrace:
		return ScoreTarget{kind: kind, platformTraceID: traceID, mapped: true}, nil
	case ScoreTargetKindObservation:
		return ScoreTarget{
			kind:                  kind,
			platformTraceID:       traceID,
			platformObservationID: observationID,
			mapped:                true,
		}, nil
	default:
		return ScoreTarget{}, errInvalidScoreTarget
	}
}

type ScoreProjectionStatus string

const (
	ScoreProjectionStatusQueued                ScoreProjectionStatus = "queued"
	ScoreProjectionStatusSending               ScoreProjectionStatus = "sending"
	ScoreProjectionStatusRetryWait             ScoreProjectionStatus = "retry_wait"
	ScoreProjectionStatusSent                  ScoreProjectionStatus = "sent"
	ScoreProjectionStatusDroppedQueueFull      ScoreProjectionStatus = "dropped_queue_full"
	ScoreProjectionStatusFailedPermanent       ScoreProjectionStatus = "failed_permanent"
	ScoreProjectionStatusFailedShutdownTimeout ScoreProjectionStatus = "failed_shutdown_timeout"
	ScoreProjectionStatusNotConfigured         ScoreProjectionStatus = "not_configured"
)

// ScoreProjectionInput 是本地 evidence 进入异步平台投影队列的边界 DTO。
// 它不携带 endpoint、credential 或 raw payload。
type ScoreProjectionInput struct {
	Target      ScoreTarget
	Evidence    aieval.EvaluationEvidence
	MaxAttempts int
	CreatedAt   time.Time
}

// ScoreProjection 是值语义状态快照。Transition 每次返回新副本，并深拷贝
// Evidence 中的指针，防止 worker 重试或调用方修改历史 evidence。
type ScoreProjection struct {
	ProjectionID string
	Target       ScoreTarget
	Evidence     aieval.EvaluationEvidence
	Status       ScoreProjectionStatus
	Attempt      int
	CreatedAt    time.Time
	maxAttempts  int
}

// ScoreProjectionRecoverySnapshot is the complete low-sensitivity state needed to
// reconstruct one durable projection after a process restart. It deliberately keeps
// endpoint credentials and provider responses outside the recovery boundary.
type ScoreProjectionRecoverySnapshot struct {
	ProjectionID          string
	Evidence              aieval.EvaluationEvidence
	TargetKind            ScoreTargetKind
	PlatformTraceID       string
	PlatformObservationID string
	Status                ScoreProjectionStatus
	Attempt               int
	CreatedAt             time.Time
	MaxAttempts           int
}

// ScoreProjectionRecoveryInput is the lifecycle-facing name for the persisted
// recovery contract. The snapshot alias is retained for storage DTO composition.
type ScoreProjectionRecoveryInput = ScoreProjectionRecoverySnapshot

func RecoverScoreProjection(input ScoreProjectionRecoveryInput) (ScoreProjection, error) {
	return RestoreScoreProjection(input)
}

// RestoreScoreProjection revalidates every persisted fact instead of trusting the
// serialized representation. In particular, a stored ProjectionID must still equal
// the deterministic ID derived from the target and evidence.
func RestoreScoreProjection(snapshot ScoreProjectionRecoverySnapshot) (ScoreProjection, error) {
	target, err := newScoreTarget(snapshot.PlatformTraceID, snapshot.PlatformObservationID, snapshot.TargetKind)
	if err != nil || !isValidScoreTarget(target) || !isValidProjectionEvidence(snapshot.Evidence) ||
		target.platformTraceID != snapshot.Evidence.ServiceTraceID || snapshot.CreatedAt.IsZero() ||
		snapshot.MaxAttempts <= 0 || snapshot.MaxAttempts > maxScoreProjectionAttempts ||
		snapshot.Attempt < 0 || snapshot.Attempt > snapshot.MaxAttempts || !isRecoverableProjectionStatus(snapshot.Status) {
		return ScoreProjection{}, errInvalidProjectionInput
	}
	projection := ScoreProjection{
		ProjectionID: snapshot.ProjectionID,
		Target:       target,
		Evidence:     cloneProjectionEvidence(snapshot.Evidence),
		Status:       snapshot.Status,
		Attempt:      snapshot.Attempt,
		CreatedAt:    snapshot.CreatedAt.UTC(),
		maxAttempts:  snapshot.MaxAttempts,
	}
	if projection.ProjectionID == "" || projection.ProjectionID != deriveProjectionID(target, projection.Evidence) {
		return ScoreProjection{}, errInvalidProjectionInput
	}
	return projection, nil
}

func isRecoverableProjectionStatus(status ScoreProjectionStatus) bool {
	switch status {
	case ScoreProjectionStatusQueued, ScoreProjectionStatusSending, ScoreProjectionStatusRetryWait,
		ScoreProjectionStatusSent, ScoreProjectionStatusDroppedQueueFull,
		ScoreProjectionStatusFailedPermanent, ScoreProjectionStatusFailedShutdownTimeout,
		ScoreProjectionStatusNotConfigured:
		return true
	default:
		return false
	}
}

// Snapshot transfers an isolated value to queue/client ownership. ScoreProjection
// itself uses value semantics, but EvaluationEvidence contains a threshold pointer;
// this method prevents two goroutines from sharing that mutable leaf.
func (projection ScoreProjection) Snapshot() ScoreProjection {
	cloned := projection
	cloned.Evidence = cloneProjectionEvidence(projection.Evidence)
	return cloned
}

func NewScoreProjection(input ScoreProjectionInput) (ScoreProjection, error) {
	if !isValidScoreTarget(input.Target) || input.MaxAttempts <= 0 || input.MaxAttempts > maxScoreProjectionAttempts {
		return ScoreProjection{}, errInvalidProjectionInput
	}
	if !isValidProjectionEvidence(input.Evidence) || input.Target.platformTraceID != input.Evidence.ServiceTraceID {
		return ScoreProjection{}, errInvalidScoreEvidence
	}

	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	evidence := cloneProjectionEvidence(input.Evidence)
	return ScoreProjection{
		ProjectionID: deriveProjectionID(input.Target, evidence),
		Target:       input.Target,
		Evidence:     evidence,
		Status:       ScoreProjectionStatusQueued,
		CreatedAt:    createdAt.UTC(),
		maxAttempts:  input.MaxAttempts,
	}, nil
}

// Transition 严格执行投递状态图。失败返回零值和固定低敏错误，不回显任何
// platform/eval identity；receiver 始终保持不变。
func (projection ScoreProjection) Transition(next ScoreProjectionStatus) (ScoreProjection, error) {
	if !projection.canTransition(next) {
		return ScoreProjection{}, errInvalidTransition
	}

	updated := projection
	updated.Evidence = cloneProjectionEvidence(projection.Evidence)
	updated.Status = next
	if projection.Status == ScoreProjectionStatusSending && next == ScoreProjectionStatusRetryWait {
		updated.Attempt++
	}
	return updated, nil
}

func (projection ScoreProjection) canTransition(next ScoreProjectionStatus) bool {
	switch projection.Status {
	case ScoreProjectionStatusQueued:
		return next == ScoreProjectionStatusSending ||
			next == ScoreProjectionStatusDroppedQueueFull ||
			next == ScoreProjectionStatusFailedPermanent ||
			next == ScoreProjectionStatusFailedShutdownTimeout ||
			next == ScoreProjectionStatusNotConfigured
	case ScoreProjectionStatusSending:
		if next == ScoreProjectionStatusRetryWait {
			return projection.Attempt < projection.maxAttempts
		}
		return next == ScoreProjectionStatusSent ||
			next == ScoreProjectionStatusFailedPermanent ||
			next == ScoreProjectionStatusFailedShutdownTimeout
	case ScoreProjectionStatusRetryWait:
		return next == ScoreProjectionStatusQueued || next == ScoreProjectionStatusFailedShutdownTimeout
	default:
		return false
	}
}

func isValidScoreTarget(target ScoreTarget) bool {
	if !target.mapped || target.kind == "" {
		return false
	}
	switch target.kind {
	case ScoreTargetKindTrace:
		return target.platformObservationID == "" && isValidNativeTraceID(target.platformTraceID)
	case ScoreTargetKindObservation:
		return isValidNativeIdentity(target.platformTraceID, target.platformObservationID)
	default:
		return false
	}
}

func isValidProjectionEvidence(evidence aieval.EvaluationEvidence) bool {
	requiredFacts := []string{
		evidence.EvalRunID,
		evidence.RequestID,
		evidence.AITraceID,
		evidence.ServiceTraceID,
		evidence.SpanID,
		evidence.Dataset.Name,
		evidence.Dataset.Version,
		evidence.SampleID,
		evidence.MetricName,
	}
	for _, fact := range requiredFacts {
		if strings.TrimSpace(fact) == "" || strings.TrimSpace(fact) != fact || len(fact) > maxProjectionFactBytes {
			return false
		}
	}
	if math.IsNaN(evidence.Score) || math.IsInf(evidence.Score, 0) || evidence.Score < 0 || evidence.Score > 1 {
		return false
	}
	if evidence.Threshold != nil {
		threshold := *evidence.Threshold
		if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
			return false
		}
	}
	return hasConsistentRegressionEvidence(evidence)
}

func hasConsistentRegressionEvidence(evidence aieval.EvaluationEvidence) bool {
	if evidence.Threshold == nil {
		return evidence.RegressionStatus == aieval.RegressionStatusWarning && evidence.FailureSummary == ""
	}
	if evidence.Score >= *evidence.Threshold {
		return evidence.RegressionStatus == aieval.RegressionStatusPassed && evidence.FailureSummary == ""
	}
	wantSummary := fmt.Sprintf("score %.2f is below threshold %.2f", evidence.Score, *evidence.Threshold)
	return evidence.RegressionStatus == aieval.RegressionStatusFailed && evidence.FailureSummary == wantSummary
}

func cloneProjectionEvidence(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
	cloned := evidence
	if evidence.Threshold != nil {
		threshold := *evidence.Threshold
		cloned.Threshold = &threshold
	}
	return cloned
}

func deriveProjectionID(target ScoreTarget, evidence aieval.EvaluationEvidence) string {
	fields := []string{
		projectionIDDomain,
		string(target.kind),
		target.platformTraceID,
		target.platformObservationID,
		evidence.EvalRunID,
		evidence.Dataset.Name,
		evidence.Dataset.Version,
		evidence.SampleID,
		evidence.RequestID,
		evidence.AITraceID,
		evidence.ServiceTraceID,
		evidence.SpanID,
		evidence.MetricName,
	}

	var canonical strings.Builder
	for _, field := range fields {
		canonical.WriteString(strconv.Itoa(len(field)))
		canonical.WriteByte(':')
		canonical.WriteString(field)
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return "score_" + hex.EncodeToString(digest[:])
}
