package backend

import (
	"context"
	"errors"
	"strings"
	"time"

	observability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

var errPrivacyBackendQuery = errors.New("privacy:query_failed")

// PrivacyBoundFixture 与以下三个泛型查询端口类型共同构成 T087/T107 历史泛型端口的
// 包内测试 seam（T180 契约守护）。真实隐私验收只经过 NewPrivacySmokeBackend 的 concrete
// capabilities 与 Scan；这个 seam 不再参与任何生产组合。
type PrivacyBoundFixture struct {
	RunID, Marker, RequestID, AITraceID, ServiceTraceID, SpanID string
	StartedAt, Deadline                                         time.Time
	APISummaryRef, ApplicationLogRef                            string
	CollectorArtifactRef, ChatReportRef                         string
	ChatReportKind                                              string
}

type PrivacyManifestResolver interface {
	Resolve(context.Context, string) (PrivacyBoundFixture, error)
}

type PrivacySurfaceQueryRequest struct {
	Target      smoke.PrivacySmokeTarget
	Fixture     PrivacyBoundFixture
	ArtifactRef string
}

type PrivacySurfaceQueryResult struct {
	Count                int
	Attempted, QuerySent bool
}

type PrivacySurfaceQuery interface {
	Search(context.Context, PrivacySurfaceQueryRequest) (PrivacySurfaceQueryResult, error)
}

// PrivacySmokeBackend 是隐私组合的生产 concrete 后端：它绑定 T194 的 contained store、
// T195 本地四 surface、T196 Grafana 两 surface 与 T197 Langfuse 两 surface，同时充当
// fixture 的 artifact writer（把四类工件写入自身持有的 store）。泛型端口的 manifest/
// fixture/remote 字段只在包内测试 seam 构造路径中被设置。
type PrivacySmokeBackend struct {
	store    *smoke.PrivacyArtifactStore
	local    *PrivacyLocalSurfaces
	grafana  *PrivacyGrafanaSurfaces
	langfuse *PrivacyLangfuseSurfaces
	timeout  time.Duration

	manifest        PrivacyManifestResolver
	fixture, remote PrivacySurfaceQuery
}

// NewPrivacySmokeBackend 是唯一的生产构造入口（反射契约固定签名）。local 必须持有与
// store 相同的 *PrivacyArtifactStore 实例：指针恒等是 split-brain 防伪的唯一可信证明，
// 路径文本相等不代表两个 capability 绑定同一已校验目录 FD。
func NewPrivacySmokeBackend(store *smoke.PrivacyArtifactStore, local *PrivacyLocalSurfaces, grafana *PrivacyGrafanaSurfaces, langfuse *PrivacyLangfuseSurfaces, surfaceTimeout time.Duration) (*PrivacySmokeBackend, error) {
	if store == nil || local == nil || grafana == nil || langfuse == nil {
		return nil, newPrivacySmokeBackendError("invalid_capabilities")
	}
	if local.store != store {
		return nil, newPrivacySmokeBackendError("artifact_store_mismatch")
	}
	if surfaceTimeout <= 0 || surfaceTimeout > maximumBackendQueryTimeout {
		return nil, newPrivacySmokeBackendError("invalid_capabilities")
	}
	return &PrivacySmokeBackend{store: store, local: local, grafana: grafana, langfuse: langfuse, timeout: surfaceTimeout}, nil
}

// privacySmokeBackendPortConfig 与 newPrivacySmokeBackendPort 是 T180 契约使用的包内
// 测试 seam：它保留历史泛型端口的全部 fail-closed 路由语义，但不得被生产组合引用。
type privacySmokeBackendPortConfig struct {
	Manifest        PrivacyManifestResolver
	Fixture, Remote PrivacySurfaceQuery
	SurfaceTimeout  time.Duration
}

func newPrivacySmokeBackendPort(config privacySmokeBackendPortConfig) (*PrivacySmokeBackend, error) {
	if config.Manifest == nil || config.Fixture == nil || config.Remote == nil || config.SurfaceTimeout <= 0 || config.SurfaceTimeout > maximumBackendQueryTimeout {
		return nil, errPrivacyBackendQuery
	}
	return &PrivacySmokeBackend{manifest: config.Manifest, fixture: config.Fixture, remote: config.Remote, timeout: config.SurfaceTimeout}, nil
}

// Scan 实现 smoke.PrivacySurfaceScanner：把组合目标路由到对应 concrete adapter 并把
// sealed 证据包装为统一接口。本地 surface 的失败直接上抛（组合 fail-closed 中止）；
// 远端 surface 的 attempted 失败以密封证据经 error 通道返回，由组合记为 report-owned
// 失败而不是中止。未封装的证据不会被构造，默认零值无法冒充 attempted。
func (backend *PrivacySmokeBackend) Scan(ctx context.Context, target smoke.PrivacySmokeTarget) (smoke.PrivacySurfaceEvidence, error) {
	if backend == nil || backend.local == nil || backend.grafana == nil || backend.langfuse == nil {
		return nil, newPrivacySmokeBackendError("invalid_capabilities")
	}
	if ctx == nil || ctx.Err() != nil || !validPrivacySmokeTarget(target) {
		return nil, newPrivacySmokeBackendError("invalid_query")
	}
	switch target.Surface {
	case smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog,
		smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceReport:
		return backend.scanLocal(ctx, target)
	case smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki:
		return backend.scanGrafana(ctx, target)
	case smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore:
		return backend.scanLangfuse(ctx, target)
	default:
		return nil, newPrivacySmokeBackendError("invalid_query")
	}
}

func (backend *PrivacySmokeBackend) scanLocal(ctx context.Context, target smoke.PrivacySmokeTarget) (smoke.PrivacySurfaceEvidence, error) {
	evidence, err := backend.local.Scan(ctx, PrivacyLocalSurfaceScanRequest{
		RunID: target.RunID, Marker: target.Marker, ForbiddenCanary: target.ForbiddenCanary,
		RequestID: target.RequestID, AITraceID: target.AITraceID, ServiceTraceID: target.ServiceTraceID,
		SpanID: target.SpanID, ManifestRef: target.ManifestRef, Surface: target.Surface,
		StartedAt: target.StartedAt, Deadline: target.Deadline,
	})
	if err != nil {
		return nil, err
	}
	// 本地 proof kind 是 T195 的包内表达；组合报告只接受 T186 锁定的 schema method。
	method, ok := privacyBackendMethod(target.Surface)
	if !ok || evidence.Surface() != target.Surface || evidence.LocalProofKind() != privacyBackendLocalProofKind(target.Surface) {
		return nil, newPrivacySmokeBackendError("unexpected_evidence")
	}
	// collectorVerified 是"本地 adapter 已成功验证 composite proof 四绑定"的投影：
	// 本地 scan 只有在 validatePrivacyLocalCollector 通过后才返回 collector 证据，
	// 因此这里按 surface 置位而不是无条件自报。
	return &privacySmokeSurfaceEvidence{
		surface: target.Surface, method: method, policy: evidence.ScannerPolicyVersion(),
		counts: evidence.Counts(), collectorVerified: target.Surface == smoke.PrivacySmokeSurfaceCollectorQueue,
	}, nil
}

func (backend *PrivacySmokeBackend) scanGrafana(ctx context.Context, target smoke.PrivacySmokeTarget) (smoke.PrivacySurfaceEvidence, error) {
	evidence, err := backend.grafana.Scan(ctx, PrivacyGrafanaScanRequest{
		Surface: target.Surface, RunID: target.RunID, Marker: target.Marker, ForbiddenCanary: target.ForbiddenCanary,
		RequestID: target.RequestID, AITraceID: target.AITraceID, ServiceTraceID: target.ServiceTraceID,
		SpanID: target.SpanID, StartedAt: target.StartedAt, Deadline: target.Deadline, Limit: target.Limit,
	})
	if err != nil {
		return nil, privacySmokeBackendFailure(target, err)
	}
	method, ok := privacyBackendMethod(target.Surface)
	if !ok || evidence.Surface() != target.Surface {
		return nil, newPrivacySmokeBackendError("unexpected_evidence")
	}
	return &privacySmokeSurfaceEvidence{
		surface: target.Surface, method: method, policy: evidence.ScannerPolicyVersion(), counts: evidence.Counts(),
	}, nil
}

func (backend *PrivacySmokeBackend) scanLangfuse(ctx context.Context, target smoke.PrivacySmokeTarget) (smoke.PrivacySurfaceEvidence, error) {
	evidence, err := backend.langfuse.Scan(ctx, PrivacyLangfuseScanRequest{
		Surface: target.Surface, RunID: target.RunID, Marker: target.Marker, ForbiddenCanary: target.ForbiddenCanary,
		RequestID: target.RequestID, AITraceID: target.AITraceID, ServiceTraceID: target.ServiceTraceID,
		SpanID: target.SpanID, StartedAt: target.StartedAt, Deadline: target.Deadline, Limit: target.Limit,
	})
	if err != nil {
		return nil, privacySmokeBackendFailure(target, err)
	}
	method, ok := privacyBackendMethod(target.Surface)
	if !ok || evidence.Surface() != target.Surface {
		return nil, newPrivacySmokeBackendError("unexpected_evidence")
	}
	return &privacySmokeSurfaceEvidence{
		surface: target.Surface, method: method, policy: evidence.ScannerPolicyVersion(), counts: evidence.Counts(),
	}, nil
}

// privacySmokeBackendFailure 只把真实 attempted 查询失败（transport/认证/响应校验类别）
// 转换为密封证据；invalid_query 等未 attempted 的拒绝保持普通错误让组合中止。映射依赖
// 组合已先做 validPrivacySmokeTarget 预检：adapter 预检拒绝因此只能由更严的窗口/字符集
// 校验触发，未来收紧 adapter 校验时必须同步收紧该等价关系。
func privacySmokeBackendFailure(target smoke.PrivacySmokeTarget, err error) error {
	type classified interface{ Class() string }
	value, ok := err.(classified)
	if !ok {
		return err
	}
	switch value.Class() {
	case "backend_timeout", "authentication_failed", "backend_unavailable", "malformed_response", "unexpected_evidence", "query_failed":
	default:
		return err
	}
	method, ok := privacyBackendMethod(target.Surface)
	if !ok {
		return err
	}
	return &privacySmokeSurfaceEvidence{
		surface: target.Surface, method: method, policy: "1", counts: privacyBackendZeroCounts(),
		failureClass: value.Class(),
	}
}

func privacyBackendMethod(surface smoke.PrivacySmokeSurface) (string, bool) {
	methods := map[smoke.PrivacySmokeSurface]string{
		smoke.PrivacySmokeSurfaceAPI:            "bounded_memory_scan",
		smoke.PrivacySmokeSurfaceApplicationLog: "projection_and_exact_query",
		smoke.PrivacySmokeSurfaceCollectorQueue: "configuration_and_telemetry",
		smoke.PrivacySmokeSurfaceReport:         "contained_artifact_scan",
		smoke.PrivacySmokeSurfaceTempo:          "bounded_trace_document",
		smoke.PrivacySmokeSurfaceLoki:           "exact_structured_query",
		smoke.PrivacySmokeSurfaceLangfuseTrace:  "bounded_platform_document",
		smoke.PrivacySmokeSurfaceLangfuseScore:  "bounded_platform_document",
	}
	method, ok := methods[surface]
	return method, ok
}

// privacyBackendLocalProofKind 固定每个本地 surface 必须产出的 T195 证明种类，防止
// adapter 以错误工件冒充正确 surface 的证明。
func privacyBackendLocalProofKind(surface smoke.PrivacySmokeSurface) string {
	return map[smoke.PrivacySmokeSurface]string{
		smoke.PrivacySmokeSurfaceAPI:            "bounded_memory_scan",
		smoke.PrivacySmokeSurfaceApplicationLog: "pre_export_projection",
		smoke.PrivacySmokeSurfaceCollectorQueue: "prequeue_configuration_telemetry",
		smoke.PrivacySmokeSurfaceReport:         "contained_artifact_scan",
	}[surface]
}

// privacySmokeSurfaceEvidence 是 backend 侧的 sealed 包装：只由 concrete adapter 证据
// 构造，同时实现 smoke.PrivacySurfaceEvidence 与 error（attempted 失败经 error 通道
// 返回给组合）。
type privacySmokeSurfaceEvidence struct {
	surface           smoke.PrivacySmokeSurface
	method            string
	policy            string
	counts            map[string]int
	collectorVerified bool
	failureClass      string
}

func (evidence *privacySmokeSurfaceEvidence) Surface() smoke.PrivacySmokeSurface {
	if evidence == nil {
		return ""
	}
	return evidence.surface
}

func (evidence *privacySmokeSurfaceEvidence) EvidenceMethod() string {
	if evidence == nil {
		return ""
	}
	return evidence.method
}

func (evidence *privacySmokeSurfaceEvidence) ScannerPolicyVersion() string {
	if evidence == nil {
		return ""
	}
	return evidence.policy
}

func (evidence *privacySmokeSurfaceEvidence) Counts() map[string]int {
	if evidence == nil {
		return nil
	}
	return clonePrivacySmokeBackendCounts(evidence.counts)
}

func (evidence *privacySmokeSurfaceEvidence) CollectorProofVerified() bool {
	return evidence != nil && evidence.collectorVerified
}

func (evidence *privacySmokeSurfaceEvidence) FailureClass() string {
	if evidence == nil {
		return ""
	}
	return evidence.failureClass
}

func (privacySmokeSurfaceEvidence) Error() string { return "privacy surface query failed" }

func clonePrivacySmokeBackendCounts(input map[string]int) map[string]int {
	result := make(map[string]int, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

var privacyBackendCategories = [...]string{"synthetic_canary", "credential", "authorization", "token", "recognized_pii"}

func privacyBackendZeroCounts() map[string]int {
	counts := make(map[string]int, len(privacyBackendCategories))
	for _, category := range privacyBackendCategories {
		counts[category] = 0
	}
	return counts
}

// Write 实现 smoke.PrivacyFixtureArtifactWriter：在把 fixture 输入持久化前补齐两类
// 工件——application log 的 canonical allowlist 投影与 collector 的组合证明（取自
// local adapter 已锁定的配置）。真实的 HTTP 时长与 queue telemetry 事实由 Loki exact
// query 与后续真实运行（T176/T201）承担；这里的投影证明的是 pre-export 记录的
// allowlist 形状与 queue 前隐私契约，而不是伪造运行时观测。
func (backend *PrivacySmokeBackend) Write(ctx context.Context, input smoke.PrivacyFixtureArtifactInput) (smoke.PrivacyFixtureArtifactRefs, error) {
	if backend == nil || backend.store == nil || backend.local == nil {
		return smoke.PrivacyFixtureArtifactRefs{}, newPrivacySmokeBackendError("invalid_capabilities")
	}
	projection, err := privacyBackendApplicationProjection(input)
	if err != nil {
		return smoke.PrivacyFixtureArtifactRefs{}, newPrivacySmokeBackendError("invalid_capabilities")
	}
	input.ApplicationLogProjection = projection
	input.CollectorCompositeProof = privacyBackendCollectorProof(backend.local.config, input)
	return backend.store.Write(ctx, input)
}

// Close 释放后端持有的 contained artifact store；score/其他 store 由命令装配层负责。
func (backend *PrivacySmokeBackend) Close() error {
	if backend == nil || backend.store == nil {
		return nil
	}
	return backend.store.Close()
}

// Search 是 T180 历史泛型端口的 seam 入口：只读已有 fixture 绑定并路由到注入的查询
// fake，生产路径不使用。
func (backend *PrivacySmokeBackend) Search(ctx context.Context, target smoke.PrivacySmokeTarget) (int, error) {
	if backend == nil || ctx == nil || ctx.Err() != nil || !validPrivacySmokeTarget(target) {
		return 0, errPrivacyBackendQuery
	}
	fixture, err := backend.manifest.Resolve(ctx, target.ManifestRef)
	if err != nil || !privacyFixtureMatchesTarget(fixture, target) {
		return 0, errPrivacyBackendQuery
	}
	query, artifactRef, ok := backend.route(target.Surface, fixture)
	if !ok {
		return 0, errPrivacyBackendQuery
	}
	bounded, cancel := context.WithTimeout(ctx, backend.timeout)
	defer cancel()
	result, err := query.Search(bounded, PrivacySurfaceQueryRequest{Target: target, Fixture: fixture, ArtifactRef: artifactRef})
	if err != nil || !result.Attempted || !result.QuerySent || result.Count < 0 {
		return 0, errPrivacyBackendQuery
	}
	return result.Count, nil
}

func (backend *PrivacySmokeBackend) route(surface smoke.PrivacySmokeSurface, fixture PrivacyBoundFixture) (PrivacySurfaceQuery, string, bool) {
	switch surface {
	case smoke.PrivacySmokeSurfaceAPI:
		return backend.fixture, fixture.APISummaryRef, true
	case smoke.PrivacySmokeSurfaceApplicationLog:
		return backend.fixture, fixture.ApplicationLogRef, true
	case smoke.PrivacySmokeSurfaceCollectorQueue:
		return backend.fixture, fixture.CollectorArtifactRef, true
	case smoke.PrivacySmokeSurfaceReport:
		return backend.fixture, fixture.ChatReportRef, true
	case smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore:
		return backend.remote, "", true
	default:
		return nil, "", false
	}
}

func validPrivacySmokeTarget(target smoke.PrivacySmokeTarget) bool {
	return safeManifestIDValue(target.RunID) && safeManifestIDValue(target.Marker) && safeManifestIDValue(target.ForbiddenCanary) &&
		safeManifestIDValue(target.RequestID) && safeManifestIDValue(target.AITraceID) && chatTraceIDPattern.MatchString(target.ServiceTraceID) &&
		chatSpanIDPattern.MatchString(target.SpanID) && safePrivacyManifestRef(target.ManifestRef) && target.Limit > 0 && target.Limit <= 100 &&
		!target.StartedAt.IsZero() && target.Deadline.After(target.StartedAt) && target.Deadline.Sub(target.StartedAt) <= time.Minute && validPrivacySurface(target.Surface)
}

func safePrivacyManifestRef(value string) bool {
	return strings.HasSuffix(value, ".json") && !strings.Contains(value, "/") && !strings.Contains(value, `\`) && len(value) <= 133 && safeManifestIDValue(strings.TrimSuffix(value, ".json"))
}

func validPrivacySurface(surface smoke.PrivacySmokeSurface) bool {
	switch surface {
	case smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue,
		smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceLangfuseTrace,
		smoke.PrivacySmokeSurfaceLangfuseScore, smoke.PrivacySmokeSurfaceReport:
		return true
	default:
		return false
	}
}

func privacyFixtureMatchesTarget(fixture PrivacyBoundFixture, target smoke.PrivacySmokeTarget) bool {
	return fixture.RunID == target.RunID && fixture.Marker == target.Marker && fixture.RequestID == target.RequestID && fixture.AITraceID == target.AITraceID &&
		fixture.ServiceTraceID == target.ServiceTraceID && fixture.SpanID == target.SpanID && fixture.StartedAt.Equal(target.StartedAt) && fixture.Deadline.Equal(target.Deadline) &&
		safePrivacyManifestRef(fixture.APISummaryRef) && safePrivacyManifestRef(fixture.ApplicationLogRef) && safePrivacyManifestRef(fixture.CollectorArtifactRef) &&
		safePrivacyManifestRef(fixture.ChatReportRef) && fixture.ChatReportKind == "chat_fixture_report" && uniquePrivacyArtifactRefs(fixture)
}

func uniquePrivacyArtifactRefs(fixture PrivacyBoundFixture) bool {
	refs := []string{fixture.APISummaryRef, fixture.ApplicationLogRef, fixture.CollectorArtifactRef, fixture.ChatReportRef}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if _, exists := seen[ref]; exists {
			return false
		}
		seen[ref] = struct{}{}
	}
	return true
}

type privacySmokeBackendError struct{ class string }

func (err privacySmokeBackendError) Error() string { return errPrivacyBackendQuery.Error() }
func (err privacySmokeBackendError) Class() string { return err.class }
func (err privacySmokeBackendError) Unwrap() error { return errPrivacyBackendQuery }

func newPrivacySmokeBackendError(class string) error { return privacySmokeBackendError{class: class} }

var _ smoke.PrivacySmokeBackend = (*PrivacySmokeBackend)(nil)
var _ smoke.PrivacySurfaceScanner = (*PrivacySmokeBackend)(nil)
var _ smoke.PrivacyFixtureArtifactWriter = (*PrivacySmokeBackend)(nil)

// privacyBackendApplicationProjection 构造 canonical allowlist 投影：与 T189 锁定契约
// 相同的确定性形状（StartedAt+1s、120ms、200），使本地 scan 的 rebuild-equality 校验
// 可用同一套输入重建。投影证明 pre-export 记录只含 allowlist 字段；真实记录的时长/
// 完成时刻由 Loki exact query 定位，不在此伪造。
func privacyBackendApplicationProjection(input smoke.PrivacyFixtureArtifactInput) (smoke.PrivacyApplicationLogProjection, error) {
	entry, err := observability.BuildHTTPCompletionLog(observability.HTTPCompletionLogInput{
		Timestamp: input.StartedAt.Add(time.Second), RequestID: input.RequestID,
		TraceID: input.ServiceTraceID, SpanID: input.SpanID, RouteTemplate: "/api/v1/chat",
		Method: "POST", StatusCode: 200, Duration: 120 * time.Millisecond,
		IsAIRequest: true, IsSmokeRun: true, AITraceID: input.AITraceID, SmokeRunID: input.Marker,
	})
	if err != nil {
		return smoke.PrivacyApplicationLogProjection{}, err
	}
	record, err := observability.BuildHTTPCompletionOTLPRecord(entry)
	if err != nil {
		return smoke.PrivacyApplicationLogProjection{}, err
	}
	return smoke.PrivacyApplicationLogProjection{
		Timestamp: record.Timestamp, Severity: record.Severity, Body: record.Body, Attributes: record.Attributes,
	}, nil
}

// privacyBackendCollectorProof 从 local adapter 已锁定的配置构造 collector 组合证明。
// 四个绑定（runtime digest/prequeue hash/component/admission）必须与 local scan 校验
// 的 config 一致；queue telemetry 使用封闭的零基线（真实 self-telemetry 事实由后续
// 真实运行 T176/T201 从 Collector 取得），证明重点停留在"queue 前隐私契约已验证"。
func privacyBackendCollectorProof(config PrivacyLocalSurfacesConfig, input smoke.PrivacyFixtureArtifactInput) smoke.PrivacyCollectorCompositeProof {
	return smoke.PrivacyCollectorCompositeProof{
		RuntimeConfigDigest:        config.RuntimeConfigDigest,
		PrequeueArtifactSHA256:     config.ExpectedPrequeueArtifactSHA256,
		ComponentIdentity:          config.CollectorComponent,
		ExportAdmissionCorrelation: config.ExportAdmissionCorrelation,
		ComponentTelemetry: smoke.PrivacyCollectorComponentTelemetry{
			ComponentIdentity: config.CollectorComponent, ObservedAt: input.StartedAt.Add(time.Second),
			WindowStartedAt: input.StartedAt, WindowDeadline: input.Deadline,
			Enqueued: 0, Sent: 0, Failed: 0, QueueSize: 0, QueueCapacity: 100, OldestAgeMS: 0,
		},
	}
}
