package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"

	"time"
)

// 历史 T087/T107 runner-core（RunPrivacySmoke 及其聚合报告）已被 T198 组合取代：
// T186 锁定的 schema 要求 privacy 场景携带八项密封证明，聚合 check 报告无法诚实满足，
// 且 Phase 9 已声明其离线通过不再构成真实后端验收。surface 常量与 target 类型保留，
// 因为它们仍是组合与 adapter 的公共语义。

// PrivacySmokeSurface is a closed, low-cardinality set of storage/query boundaries. Surface names
// are used only to route exact canary searches and are never persisted in the smoke report.
type PrivacySmokeSurface string

const (
	PrivacySmokeSurfaceAPI            PrivacySmokeSurface = "api"
	PrivacySmokeSurfaceApplicationLog PrivacySmokeSurface = "application_log"
	PrivacySmokeSurfaceCollectorQueue PrivacySmokeSurface = "collector_queue"
	PrivacySmokeSurfaceTempo          PrivacySmokeSurface = "tempo"
	PrivacySmokeSurfaceLoki           PrivacySmokeSurface = "loki"
	PrivacySmokeSurfaceLangfuseTrace  PrivacySmokeSurface = "langfuse_trace"
	PrivacySmokeSurfaceLangfuseScore  PrivacySmokeSurface = "langfuse_score"
	PrivacySmokeSurfaceReport         PrivacySmokeSurface = "report"
)

// PrivacySmokeTarget 是组合交给每个 surface adapter 的完整不可变目标：身份、窗口与
// manifest ref 全部来自 fixture 结果原样传递，adapter 不得自行猜测或回填。
type PrivacySmokeTarget struct {
	RunID, Marker, ForbiddenCanary string
	RequestID, AITraceID           string
	ServiceTraceID, SpanID         string
	ManifestRef                    string
	Limit                          int
	Surface                        PrivacySmokeSurface
	StartedAt, Deadline            time.Time
}

// PrivacySmokeBackend 是历史 T087/T107 泛型端口的残留接口，现在只由 T180 seam 契约与
// backend 包内的包内测试 seam 实现；真实隐私验收一律走 PrivacySurfaceScanner。
type PrivacySmokeBackend interface {
	Search(context.Context, PrivacySmokeTarget) (int, error)
}

// Privacy composition（T198）：fixture → 八 surface → 密封 evidence → 统一报告。
// 与上方 T087/T107 历史 runner-core 不同，本组合是真实后端隐私验收的生产编排边界：
// 生产依赖只接受 T193-T197 提供的受保护 concrete capabilities，泛型端口退为包内测试 seam。
// ---------------------------------------------------------------------------

const maximumPrivacySurfaceTimeout = 30 * time.Second

var errPrivacyCompositionFailed = errors.New("privacy composition failed")

// privacyCompositionExecutionOrder 先读全部本地工件、再触发远程查询；report surface 在
// 执行序中位于远程之前（它读取的是既有 chat fixture report），但在 schema 序中排最后。
var privacyCompositionExecutionOrder = [...]PrivacySmokeSurface{
	PrivacySmokeSurfaceAPI, PrivacySmokeSurfaceApplicationLog, PrivacySmokeSurfaceCollectorQueue,
	PrivacySmokeSurfaceReport, PrivacySmokeSurfaceTempo, PrivacySmokeSurfaceLoki,
	PrivacySmokeSurfaceLangfuseTrace, PrivacySmokeSurfaceLangfuseScore,
}

// privacyCompositionSchemaOrder 是版本控制 schema 的 prefixItems 固定顺序。
var privacyCompositionSchemaOrder = [...]PrivacySmokeSurface{
	PrivacySmokeSurfaceAPI, PrivacySmokeSurfaceApplicationLog, PrivacySmokeSurfaceCollectorQueue,
	PrivacySmokeSurfaceTempo, PrivacySmokeSurfaceLoki, PrivacySmokeSurfaceLangfuseTrace,
	PrivacySmokeSurfaceLangfuseScore, PrivacySmokeSurfaceReport,
}

// PrivacyCompositionRequest 是生产组合的封闭输入。RunID/Marker/窗口必须来自命令装配，
// 组合自身从不生成身份或猜测时间窗。
type PrivacyCompositionRequest struct {
	RunID, Marker, Profile, ForbiddenCanary string
	StartedAt, Deadline                     time.Time
	SurfaceTimeout                          time.Duration
}

// PrivacyCompositionFixtureRunner 只接受 RunPrivacyFixture 的完整输入并返回其密封结果。
type PrivacyCompositionFixtureRunner interface {
	Run(context.Context, PrivacyFixtureRequest) (PrivacyFixtureResult, error)
}

// PrivacySurfaceEvidence 是 concrete adapter 产出的密封证据接口。组合只读取这五个低敏
// 事实；adapter 包之外的调用方无法构造通过校验的实现，因此默认零值不可能伪装 attempted。
type PrivacySurfaceEvidence interface {
	Surface() PrivacySmokeSurface
	EvidenceMethod() string
	ScannerPolicyVersion() string
	Counts() map[string]int
	// CollectorProofVerified 只在 collector_queue surface 要求为 true；它由 adapter 在
	// 完整校验 composite proof 文档之后才能置位。
	CollectorProofVerified() bool
	// FailureClass 非空表示一次真实 attempted 查询的封闭失败（远端适配器经 error 通道返回）。
	FailureClass() string
}

// PrivacySurfaceScanner 由 concrete 组合后端实现：一次调用恰好覆盖一个 surface 并返回
// 密封证据；未密封的错误只能以普通 error 表达，组合必须 fail-closed。
type PrivacySurfaceScanner interface {
	Scan(context.Context, PrivacySmokeTarget) (PrivacySurfaceEvidence, error)
}

type PrivacyCompositionDependencies struct {
	Fixture  PrivacyCompositionFixtureRunner
	Surfaces PrivacySurfaceScanner
	Clock    PollerClock
}

// privacyCompositionRequest/dependencies 是 T192 契约测试使用的包内 seam 类型：测试 fake
// 直接返回私有的 privacyCompositionSurfaceEvidence 值，经 ForTest 入口适配到生产接口。
type privacyCompositionRequest struct {
	Profile, ForbiddenCanary, RunID, Marker string
	StartedAt, Deadline                     time.Time
	SurfaceTimeout                          time.Duration
}

type privacyCompositionFixtureRunner interface {
	Run(context.Context, PrivacyFixtureRequest) (PrivacyFixtureResult, error)
}

type privacyCompositionScanner interface {
	Scan(context.Context, PrivacySmokeTarget) (privacyCompositionSurfaceEvidence, error)
}

type privacyCompositionDependencies struct {
	Fixture  privacyCompositionFixtureRunner
	Surfaces privacyCompositionScanner
	Clock    PollerClock
}

type privacyCompositionCollectorBindings struct {
	RuntimeConfigDigestVerified, PrequeueArtifactHashVerified, ComponentIdentityVerified, ExportAdmissionCorrelated bool
}

// privacyCompositionSurfaceEvidence 同时实现 PrivacySurfaceEvidence 与 error：正常证据经
// 第一返回值传递，attempted 失败经 error 通道携带同一密封类型，普通 error 表示未密封失败。
type privacyCompositionSurfaceEvidence struct {
	surface      PrivacySmokeSurface
	method       string
	policy       string
	counts       map[string]int
	bindings     privacyCompositionCollectorBindings
	failureClass string
}

func (evidence privacyCompositionSurfaceEvidence) Surface() PrivacySmokeSurface {
	return evidence.surface
}
func (evidence privacyCompositionSurfaceEvidence) EvidenceMethod() string { return evidence.method }
func (evidence privacyCompositionSurfaceEvidence) ScannerPolicyVersion() string {
	return evidence.policy
}
func (evidence privacyCompositionSurfaceEvidence) Counts() map[string]int {
	return clonePrivacyCounts(evidence.counts)
}

func (evidence privacyCompositionSurfaceEvidence) CollectorProofVerified() bool {
	return evidence.bindings.RuntimeConfigDigestVerified && evidence.bindings.PrequeueArtifactHashVerified &&
		evidence.bindings.ComponentIdentityVerified && evidence.bindings.ExportAdmissionCorrelated
}

func (evidence privacyCompositionSurfaceEvidence) FailureClass() string { return evidence.failureClass }

// Error 让 attempted 失败证据可以经 error 通道传递，同时保持固定低敏消息，绝不携带 query
// 或平台原文。
func (privacyCompositionSurfaceEvidence) Error() string { return "privacy surface query failed" }

// 以下两个构造函数是 T192 契约测试的包内 seam；生产 adapter 以各自 sealed evidence 实现
// 同一 PrivacySurfaceEvidence 接口，不允许包外伪造。
func newPrivacyCompositionSurfaceEvidenceForTest(surface PrivacySmokeSurface, method, policy string, counts map[string]int, bindings privacyCompositionCollectorBindings) privacyCompositionSurfaceEvidence {
	return privacyCompositionSurfaceEvidence{surface: surface, method: method, policy: policy, counts: clonePrivacyCounts(counts), bindings: bindings}
}

func newPrivacyCompositionAttemptedFailureForTest(surface PrivacySmokeSurface, failureClass string) privacyCompositionSurfaceEvidence {
	return privacyCompositionSurfaceEvidence{surface: surface, method: privacyCompositionMethod(surface), policy: "1", counts: privacyCompositionZeroCounts(), failureClass: failureClass}
}

// runPrivacyCompositionForTest 把包内 seam 依赖适配进生产组合，保证测试与生产共用同一套
// 校验与报告逻辑，而不是维护第二份语义。nil seam 保持为 nil 接口透传，让生产组合在触发
// fixture 之前完成整图校验。
func runPrivacyCompositionForTest(ctx context.Context, request privacyCompositionRequest, deps privacyCompositionDependencies) (*SmokeReport, error) {
	var fixture PrivacyCompositionFixtureRunner
	var surfaces PrivacySurfaceScanner
	if deps.Fixture != nil {
		fixture = privacyCompositionFixtureAdapter{deps.Fixture}
	}
	if deps.Surfaces != nil {
		surfaces = privacyCompositionScannerAdapter{deps.Surfaces}
	}
	return RunPrivacyComposition(ctx, PrivacyCompositionRequest{
		Profile: request.Profile, ForbiddenCanary: request.ForbiddenCanary, RunID: request.RunID,
		Marker: request.Marker, StartedAt: request.StartedAt, Deadline: request.Deadline,
		SurfaceTimeout: request.SurfaceTimeout,
	}, PrivacyCompositionDependencies{Fixture: fixture, Surfaces: surfaces, Clock: deps.Clock})
}

type privacyCompositionFixtureAdapter struct {
	inner privacyCompositionFixtureRunner
}

func (adapter privacyCompositionFixtureAdapter) Run(ctx context.Context, request PrivacyFixtureRequest) (PrivacyFixtureResult, error) {
	if adapter.inner == nil {
		return PrivacyFixtureResult{}, errPrivacyCompositionFailed
	}
	return adapter.inner.Run(ctx, request)
}

type privacyCompositionScannerAdapter struct{ inner privacyCompositionScanner }

func (adapter privacyCompositionScannerAdapter) Scan(ctx context.Context, target PrivacySmokeTarget) (PrivacySurfaceEvidence, error) {
	if adapter.inner == nil {
		return nil, errPrivacyCompositionFailed
	}
	evidence, err := adapter.inner.Scan(ctx, target)
	return PrivacySurfaceEvidence(evidence), err
}

type privacyCompositionEntry struct {
	status       string
	counts       map[string]int
	failureClass string
}

// RunPrivacyComposition 是生产隐私验收的组合入口：先证明整个依赖图完整，再以请求原样
// 驱动受保护 fixture，把 fixture 的 window/identity 逐字交给八个 surface，每个 surface
// 使用独立的即时短预算；本地工件失败立即中止，attempted 远端失败与已确认泄漏则进入
// schema-valid 的 failed 报告。
func RunPrivacyComposition(ctx context.Context, request PrivacyCompositionRequest, deps PrivacyCompositionDependencies) (*SmokeReport, error) {
	if ctx == nil || ctx.Err() != nil || deps.Fixture == nil || deps.Surfaces == nil || deps.Clock == nil ||
		!validPrivacyCompositionRequest(request) {
		return nil, newPrivacyCompositionError()
	}
	fixture, err := deps.Fixture.Run(ctx, PrivacyFixtureRequest{
		RunID: request.RunID, Marker: request.Marker, Profile: request.Profile,
		ForbiddenCanary: request.ForbiddenCanary, StartedAt: request.StartedAt, Deadline: request.Deadline,
	})
	if err != nil || !validPrivacyCompositionFixture(fixture, request) {
		return nil, newPrivacyCompositionError()
	}

	entries := make(map[PrivacySmokeSurface]privacyCompositionEntry, len(privacyCompositionExecutionOrder))
	for _, surface := range privacyCompositionExecutionOrder {
		entry, err := runPrivacyCompositionSurface(ctx, deps.Surfaces, privacyCompositionTarget(fixture, request, surface), request.SurfaceTimeout)
		if err != nil {
			return nil, newPrivacyCompositionError()
		}
		entries[surface] = entry
	}

	finishedAt := deps.Clock.Now().UTC()
	// 与历史 runner 相同的时钟偏差容错：finished_at 只描述报告完成时刻，任何偏差都夹回
	// fixture 窗口，避免虚假完成时间破坏 schema 契约。
	if finishedAt.Before(fixture.StartedAt) {
		finishedAt = fixture.StartedAt
	}
	if finishedAt.After(fixture.Deadline) {
		finishedAt = fixture.Deadline
	}
	return buildPrivacyCompositionReport(request, fixture, finishedAt, entries)
}

// runPrivacyCompositionSurface 为单个 surface 创建独立、即时（just-in-time）的短预算
// context 并在调用后立即取消，防止前面的 surface 消耗后面 surface 的查询时间。预算
// 裁剪刻意使用 wall-clock 而非注入的 deps.Clock：surface 预算是实时资源约束，不是可
// 回放的事实，且 T192 契约钉死 deps.Clock 只被 finished_at 观察一次。密封的 attempted
// 失败经 error 通道返回，未密封错误或零值证据都会让组合 fail-closed。
func runPrivacyCompositionSurface(parent context.Context, surfaces PrivacySurfaceScanner, target PrivacySmokeTarget, timeout time.Duration) (privacyCompositionEntry, error) {
	budget := timeout
	if deadline := time.Now().Add(budget); deadline.After(target.Deadline) {
		budget = time.Until(target.Deadline)
	}
	if budget <= 0 {
		return privacyCompositionEntry{}, errPrivacyCompositionFailed
	}
	surfaceCtx, cancel := context.WithTimeout(parent, budget)
	evidence, scanErr := surfaces.Scan(surfaceCtx, target)
	cancel()
	if scanErr != nil {
		sealed, ok := scanErr.(PrivacySurfaceEvidence)
		// error 通道的密封证据必须携带非空 failure class：空类目意味着"attempted"语义
		// 未真正成立，组合不得把诊断空洞的失败写进报告。
		if !ok || !validPrivacyCompositionEvidence(sealed, target.Surface) || sealed.FailureClass() == "" {
			return privacyCompositionEntry{}, errPrivacyCompositionFailed
		}
		// 已确认泄漏优先：adapter 若以"证据含命中 + error"返回（如语义失败但泄漏已
		// 确认），计数必须原样保留，禁止无条件清零。
		sealedCounts := sealed.Counts()
		if privacyCompositionCountsHaveHits(sealedCounts) {
			return privacyCompositionEntry{status: "failed", counts: sealedCounts, failureClass: "unexpected_evidence"}, nil
		}
		return privacyCompositionEntry{status: "failed", counts: privacyCompositionZeroCounts(), failureClass: sealed.FailureClass()}, nil
	}
	if !validPrivacyCompositionEvidence(evidence, target.Surface) {
		return privacyCompositionEntry{}, errPrivacyCompositionFailed
	}
	counts := evidence.Counts()
	if privacyCompositionCountsHaveHits(counts) {
		return privacyCompositionEntry{status: "failed", counts: counts, failureClass: "unexpected_evidence"}, nil
	}
	if evidence.FailureClass() != "" {
		return privacyCompositionEntry{status: "failed", counts: counts, failureClass: evidence.FailureClass()}, nil
	}
	return privacyCompositionEntry{status: "passed", counts: counts}, nil
}

func validPrivacyCompositionRequest(request PrivacyCompositionRequest) bool {
	return contains(allowedProfiles, request.Profile) && isSafePollMarker(request.ForbiddenCanary) &&
		isSafePollMarker(request.RunID) && isSafePollMarker(request.Marker) &&
		!request.StartedAt.IsZero() && request.StartedAt.Location() == time.UTC &&
		request.Deadline.Location() == time.UTC && request.Deadline.After(request.StartedAt) &&
		request.Deadline.Sub(request.StartedAt) <= time.Minute &&
		request.SurfaceTimeout > 0 && request.SurfaceTimeout <= maximumPrivacySurfaceTimeout
}

// validPrivacyCompositionFixture 拒绝任何与请求身份/窗口不一致的 fixture 结果，也拒绝
// 未受保护或未成功的请求证明；任何 ref 都必须是通过 registered 工件约束的安全 basename。
func validPrivacyCompositionFixture(result PrivacyFixtureResult, request PrivacyCompositionRequest) bool {
	return result.RunID == request.RunID && result.Marker == request.Marker &&
		result.StartedAt.Equal(request.StartedAt) && result.Deadline.Equal(request.Deadline) &&
		safePrivacyOpaqueID(result.RequestID) && safePrivacyOpaqueID(result.AITraceID) &&
		privacyTraceIDPattern.MatchString(result.ServiceTraceID) && privacySpanIDPattern.MatchString(result.SpanID) &&
		safePrivacyArtifactRef(result.ManifestRef) && safePrivacyArtifactRef(result.APISummaryRef) &&
		safePrivacyArtifactRef(result.ApplicationLogRef) && safePrivacyArtifactRef(result.ChatReportRef) &&
		safePrivacyArtifactRef(result.CollectorArtifactRef) && result.RequestSent && result.ChatSucceeded
}

func privacyCompositionTarget(result PrivacyFixtureResult, request PrivacyCompositionRequest, surface PrivacySmokeSurface) PrivacySmokeTarget {
	return PrivacySmokeTarget{
		RunID: result.RunID, Marker: result.Marker, ForbiddenCanary: request.ForbiddenCanary,
		RequestID: result.RequestID, AITraceID: result.AITraceID, ServiceTraceID: result.ServiceTraceID,
		SpanID: result.SpanID, ManifestRef: result.ManifestRef, Limit: 100,
		Surface: surface, StartedAt: result.StartedAt, Deadline: result.Deadline,
	}
}

// validPrivacyCompositionEvidence 是密封证据的完整事实校验：surface/method/policy/counts
// 与 collector 绑定缺一不可；failure class 若存在必须属于稳定错误类集合，防止伪造类目
// 流入报告。
func validPrivacyCompositionEvidence(evidence PrivacySurfaceEvidence, surface PrivacySmokeSurface) bool {
	if evidence == nil || evidence.Surface() != surface || evidence.EvidenceMethod() != privacyCompositionMethod(surface) ||
		evidence.ScannerPolicyVersion() != "1" || !validPrivacyCompositionCounts(evidence.Counts()) {
		return false
	}
	if surface == PrivacySmokeSurfaceCollectorQueue && !evidence.CollectorProofVerified() {
		return false
	}
	return evidence.FailureClass() == "" || contains(allowedErrorClasses, evidence.FailureClass())
}

// privacyCompositionMethod 固定每个 surface 的 schema evidence method（与 T186 证据能力
// 矩阵一致），也是 adapter 证明种类与报告枚举之间的唯一映射点。
func privacyCompositionMethod(surface PrivacySmokeSurface) string {
	return map[PrivacySmokeSurface]string{
		PrivacySmokeSurfaceAPI:            "bounded_memory_scan",
		PrivacySmokeSurfaceApplicationLog: "projection_and_exact_query",
		PrivacySmokeSurfaceCollectorQueue: "configuration_and_telemetry",
		PrivacySmokeSurfaceReport:         "contained_artifact_scan",
		PrivacySmokeSurfaceTempo:          "bounded_trace_document",
		PrivacySmokeSurfaceLoki:           "exact_structured_query",
		PrivacySmokeSurfaceLangfuseTrace:  "bounded_platform_document",
		PrivacySmokeSurfaceLangfuseScore:  "bounded_platform_document",
	}[surface]
}

func validPrivacyCompositionCounts(counts map[string]int) bool {
	if len(counts) != len(privacyCompositionCategories) {
		return false
	}
	for _, category := range privacyCompositionCategories {
		if count, ok := counts[category]; !ok || count < 0 {
			return false
		}
	}
	return true
}

func privacyCompositionZeroCounts() map[string]int {
	counts := make(map[string]int, len(privacyCompositionCategories))
	for _, category := range privacyCompositionCategories {
		counts[category] = 0
	}
	return counts
}

func privacyCompositionCountsHaveHits(counts map[string]int) bool {
	for _, count := range counts {
		if count > 0 {
			return true
		}
	}
	return false
}

var privacyCompositionCategories = [...]string{"synthetic_canary", "credential", "authorization", "token", "recognized_pii"}

// buildPrivacyCompositionReport 把八个密封 surface 证明投影为 schema-valid 报告。泄漏计数
// 优先于任何查询失败类别；报告构造后立即做序列化 guard，确保 canary 不会经报告外发。
func buildPrivacyCompositionReport(request PrivacyCompositionRequest, fixture PrivacyFixtureResult, finishedAt time.Time, entries map[PrivacySmokeSurface]privacyCompositionEntry) (*SmokeReport, error) {
	failed := false
	var totalHits int64
	for _, surface := range privacyCompositionExecutionOrder {
		entry, ok := entries[surface]
		if !ok {
			return nil, newPrivacyCompositionError()
		}
		for _, count := range entry.counts {
			if int64(count) > math.MaxInt64-totalHits {
				return nil, newPrivacyCompositionError()
			}
			totalHits += int64(count)
		}
		if entry.status == "failed" {
			failed = true
		}
	}
	check := BackendCheckInput{
		Backend: "privacy", Status: "passed", FailureStage: "none",
		Evidence: map[string]any{"forbidden_marker_hits": totalHits},
	}
	if failed {
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", privacyCompositionCheckClass(entries)
	}
	evidenceInputs := make([]PrivacySmokeReportEvidenceInput, 0, len(privacyCompositionSchemaOrder))
	for _, surface := range privacyCompositionSchemaOrder {
		entry := entries[surface]
		evidenceInputs = append(evidenceInputs, PrivacySmokeReportEvidenceInput{
			Surface: surface, EvidenceMethod: privacyCompositionMethod(surface), Status: entry.status,
			ScannerPolicyVersion: "1", Counts: entry.counts,
			CollectorProofVerified: surface == PrivacySmokeSurfaceCollectorQueue,
		})
	}
	report, err := BuildSmokeReport(SmokeReportInput{
		RunID: fixture.RunID, Marker: fixture.Marker, Profile: request.Profile, Scenario: "privacy",
		StartedAt: fixture.StartedAt, Deadline: fixture.Deadline, FinishedAt: finishedAt,
		Checks:          []BackendCheckInput{check},
		PrivacyEvidence: evidenceInputs,
		Cleanup:         SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
	if err != nil {
		return nil, newPrivacyCompositionError()
	}
	encoded, err := json.Marshal(report)
	if err != nil || bytes.Contains(encoded, []byte(request.ForbiddenCanary)) {
		return nil, newPrivacyCompositionError()
	}
	return report, nil
}

// privacyCompositionCheckClass：已确认泄漏（任意类别命中、任意 surface）是首要安全事实，
// 必须优先于任何较早出现的查询失败类别；否则取执行序中第一个失败 surface 的封闭类别。
func privacyCompositionCheckClass(entries map[PrivacySmokeSurface]privacyCompositionEntry) string {
	for _, surface := range privacyCompositionExecutionOrder {
		entry := entries[surface]
		if entry.status == "failed" && privacyCompositionCountsHaveHits(entry.counts) {
			return "unexpected_evidence"
		}
	}
	for _, surface := range privacyCompositionExecutionOrder {
		entry := entries[surface]
		if entry.status == "failed" {
			return entry.failureClass
		}
	}
	return "query_failed"
}

type privacyCompositionError struct{}

func (privacyCompositionError) Error() string { return errPrivacyCompositionFailed.Error() }
func (privacyCompositionError) Class() string { return "privacy_composition_failed" }
func (privacyCompositionError) Unwrap() error { return errPrivacyCompositionFailed }
func newPrivacyCompositionError() error       { return privacyCompositionError{} }
