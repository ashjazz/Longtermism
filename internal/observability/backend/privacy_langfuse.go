package backend

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/internal/observability/privacy"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// 本文件实现 Langfuse trace/score 两个 privacy surface 的 concrete adapter（T191/T197）。
// 设计边界：
//   - 不具备任何网络/文件系统/日志能力（AST 契约测试守护）：所有查询都经由既有
//     protected Langfuse client 的 loopback-only、no-proxy、no-redirect 传输执行，
//     凭据只存在于客户端的 Authorization header，本文件永远接触不到。
//   - 证据方法为 bounded_platform_document：先对平台返回的完整文档做 bounded scan，
//     再做语义校验。语义失败但已确认泄漏时仍保留泄漏计数——歧义不能抹掉事实。
//   - score surface 在发出任何网络请求前，必须先通过同一持久化 projection store
//     证明存在恰好一条本地已投递（sent）快照；平台字段一律不得猜测。

var errPrivacyLangfuseSurface = errors.New("privacy Langfuse surface unavailable")

// PrivacyLangfuseScanRequest 是一次 Langfuse privacy surface 扫描的封闭输入。所有身份
// 字段都必须来自 fixture/run manifest，且经过严格字符集与窗口校验，防止把查询变成
// 注入或宽查原语。字段名刻意不包含 attempt/query/verified 等可伪造证据概念。
type PrivacyLangfuseScanRequest struct {
	Surface                                      smoke.PrivacySmokeSurface
	RunID, Marker, ForbiddenCanary               string
	RequestID, AITraceID, ServiceTraceID, SpanID string
	StartedAt, Deadline                          time.Time
	Limit                                        int
}

// PrivacyLangfuseSurfaceEvidence 只在一次真实受保护查询、完整协议校验与全文档扫描之后
// 才能构造。私有字段阻止调用方伪造 attempted/verified 或写入未经证明的零命中。
type PrivacyLangfuseSurfaceEvidence struct {
	surface              smoke.PrivacySmokeSurface
	evidenceMethod       string
	scannerPolicyVersion string
	counts               map[string]int
}

func (evidence *PrivacyLangfuseSurfaceEvidence) Surface() smoke.PrivacySmokeSurface {
	if evidence == nil {
		return ""
	}
	return evidence.surface
}

func (evidence *PrivacyLangfuseSurfaceEvidence) EvidenceMethod() string {
	if evidence == nil {
		return ""
	}
	return evidence.evidenceMethod
}

func (evidence *PrivacyLangfuseSurfaceEvidence) ScannerPolicyVersion() string {
	if evidence == nil {
		return ""
	}
	return evidence.scannerPolicyVersion
}

func (evidence *PrivacyLangfuseSurfaceEvidence) Counts() map[string]int {
	if evidence == nil {
		return nil
	}
	return clonePrivacyLangfuseCounts(evidence.counts)
}

// MarshalJSON 恒返回错误：evidence 是密封的进程内事实，任何序列化都会制造一条可泄漏
// 摘要的旁路（对应 JSON 契约测试要求零值 evidence 不可序列化）。
func (PrivacyLangfuseSurfaceEvidence) MarshalJSON() ([]byte, error) {
	return nil, errPrivacyLangfuseSurface
}

// PrivacyLangfuseSurfaces 是 trace/score 两个 Langfuse privacy surface 的 concrete adapter。
// lookup 与 score backend 持有同一 store 实例：生产 constructor 用指针恒等证明这一点，
// 防止“两个恰好实现同一方法”的 store 造成 split-brain 证据。
type PrivacyLangfuseSurfaces struct {
	trace  *LangfuseChatSmokeQueryClient
	score  *LangfuseScoreSmokeBackend
	lookup scoreProjectionLookup
}

// NewPrivacyLangfuseSurfaces 是唯一的生产装配入口。第三个参数必须是 score backend
// 实际持有的同一个 *ScoreProjectionStore 实例；任何 nil 或身份不一致都 fail-fast，
// 不允许用泛型接口伪造依赖。
func NewPrivacyLangfuseSurfaces(trace *LangfuseChatSmokeQueryClient, score *LangfuseScoreSmokeBackend, store *localeval.ScoreProjectionStore) (*PrivacyLangfuseSurfaces, error) {
	if trace == nil || trace.query == nil || score == nil || score.query == nil || store == nil || score.store != store {
		return nil, newPrivacyLangfuseError()
	}
	return &PrivacyLangfuseSurfaces{trace: trace, score: score, lookup: store}, nil
}

// newPrivacyLangfuseSurfacesForTest 是包内测试 seam：T191 契约测试用 fake lookup 构造
// score backend，本函数复用其内部 store 实例，避免测试走生产持久化路径。生产装配必须
// 经过 NewPrivacyLangfuseSurfaces，此 seam 永远不暴露到包外。
func newPrivacyLangfuseSurfacesForTest(trace *LangfuseChatSmokeQueryClient, score *LangfuseScoreSmokeBackend) (*PrivacyLangfuseSurfaces, error) {
	if trace == nil || trace.query == nil || score == nil || score.query == nil || score.store == nil {
		return nil, newPrivacyLangfuseError()
	}
	return &PrivacyLangfuseSurfaces{trace: trace, score: score, lookup: score.store}, nil
}

// Scan 对单个 surface 执行完整证据链：封闭请求校验 -> 受保护查询 -> bounded 解码 ->
// 全文档扫描 -> 锁定版本语义校验 -> 密封 evidence。任何一步失败都返回零值 evidence 与
// 只含稳定类别的错误，不回显查询、身份、端点或平台原文。
func (surfaces *PrivacyLangfuseSurfaces) Scan(ctx context.Context, request PrivacyLangfuseScanRequest) (PrivacyLangfuseSurfaceEvidence, error) {
	if surfaces == nil || surfaces.trace == nil || surfaces.score == nil || surfaces.lookup == nil ||
		ctx == nil || ctx.Err() != nil || !validPrivacyLangfuseRequest(request) {
		return PrivacyLangfuseSurfaceEvidence{}, newPrivacyLangfuseError()
	}
	var (
		counts map[string]int
		err    error
	)
	switch request.Surface {
	case smoke.PrivacySmokeSurfaceLangfuseTrace:
		counts, err = surfaces.scanTrace(ctx, request)
	case smoke.PrivacySmokeSurfaceLangfuseScore:
		counts, err = surfaces.scanScore(ctx, request)
	default:
		return PrivacyLangfuseSurfaceEvidence{}, newPrivacyLangfuseError()
	}
	if err != nil || ctx.Err() != nil {
		return PrivacyLangfuseSurfaceEvidence{}, newPrivacyLangfuseError()
	}
	return PrivacyLangfuseSurfaceEvidence{
		surface: request.Surface, evidenceMethod: "bounded_platform_document",
		scannerPolicyVersion: "1", counts: clonePrivacyLangfuseCounts(counts),
	}, nil
}

// scanTrace 走 observations v1：七条件结构化 filter + 精确窗口 + 单页，绝不宽查。
// 完整文档先扫描后校验，泄漏计数优先于语义歧义保留。
func (surfaces *PrivacyLangfuseSurfaces) scanTrace(ctx context.Context, request PrivacyLangfuseScanRequest) (map[string]int, error) {
	filter, err := privacyLangfuseTraceFilter(request)
	if err != nil {
		return nil, err
	}
	body, err := surfaces.trace.privacyObservationsDocument(ctx, filter, request.StartedAt, request.Deadline, request.Limit)
	if err != nil {
		return nil, err
	}
	document, err := decodePrivacyGrafanaDocument(body)
	if err != nil {
		return nil, err
	}
	counts, err := scanPrivacyGrafanaDocuments(request.ForbiddenCanary, document)
	if err != nil {
		return nil, err
	}
	if err := validatePrivacyLangfuseTraceDocument(document, request); err != nil {
		if privacyLangfuseHasLeaks(counts) {
			return counts, nil
		}
		return nil, err
	}
	return counts, nil
}

// scanScore 先证明本地恰好一条已投递 projection 快照（fail-closed，任何缺一致都禁止
// 网络），再按稳定 ProjectionID + 平台 trace/observation identity + 窗口查询 scores v3。
func (surfaces *PrivacyLangfuseSurfaces) scanScore(ctx context.Context, request PrivacyLangfuseScanRequest) (map[string]int, error) {
	records, err := surfaces.lookup.FindByRunID(ctx, request.Marker)
	if err != nil || len(records) != 1 || !validPrivacyLangfuseSnapshot(records[0], request) {
		return nil, newPrivacyLangfuseError()
	}
	body, err := surfaces.score.privacyScoresDocument(ctx, records[0].ProjectionID, records[0].PlatformTraceID, records[0].PlatformObservationID, request.StartedAt, request.Deadline, request.Limit)
	if err != nil {
		return nil, err
	}
	document, err := decodePrivacyGrafanaDocument(body)
	if err != nil {
		return nil, err
	}
	counts, err := scanPrivacyGrafanaDocuments(request.ForbiddenCanary, document)
	if err != nil {
		return nil, err
	}
	if err := validatePrivacyLangfuseScoreDocument(document, request, records[0].ProjectionID); err != nil {
		if privacyLangfuseHasLeaks(counts) {
			return counts, nil
		}
		return nil, err
	}
	return counts, nil
}

// privacyLangfuseTraceFilter 构造封闭七条件 filter：三个 metadata identity、平台
// traceId/id 与 [StartedAt, Deadline) 时间窗。上界用严格小于，与 poller 的窗口语义一致，
// 避免把 deadline 时刻的迟到数据算进本次 run。
func privacyLangfuseTraceFilter(request PrivacyLangfuseScanRequest) (string, error) {
	filters := []map[string]string{
		{"type": "stringObject", "column": "metadata", "key": "longtermism.smoke.run_id", "operator": "=", "value": request.Marker},
		{"type": "stringObject", "column": "metadata", "key": "request_id", "operator": "=", "value": request.RequestID},
		{"type": "stringObject", "column": "metadata", "key": "ai_trace_id", "operator": "=", "value": request.AITraceID},
		{"type": "string", "column": "traceId", "operator": "=", "value": request.ServiceTraceID},
		{"type": "string", "column": "id", "operator": "=", "value": request.SpanID},
		{"type": "datetime", "column": "startTime", "operator": ">=", "value": request.StartedAt.Format(time.RFC3339Nano)},
		{"type": "datetime", "column": "startTime", "operator": "<", "value": request.Deadline.Format(time.RFC3339Nano)},
	}
	encoded, err := json.Marshal(filters)
	return string(encoded), err
}

// validPrivacyLangfuseRequest 是网络前闸门：surface 白名单、不透明 ID 字符集、
// 平台十六进制 identity、1..100 limit、UTC 窗口在 [StartedAt, Deadline) 且不超过一分钟、
// 窗口终点不能陈旧超过一分钟、canary 必须是合法合成值。全部纯内存判断，零外连。
func validPrivacyLangfuseRequest(request PrivacyLangfuseScanRequest) bool {
	if request.Surface != smoke.PrivacySmokeSurfaceLangfuseTrace && request.Surface != smoke.PrivacySmokeSurfaceLangfuseScore {
		return false
	}
	if !safePrivacyLangfuseOpaque(request.RunID) || !safePrivacyLangfuseOpaque(request.Marker) ||
		!safePrivacyLangfuseOpaque(request.RequestID) || !safePrivacyLangfuseOpaque(request.AITraceID) ||
		!isLowerHex(request.ServiceTraceID, 32) || !isLowerHex(request.SpanID, 16) || request.Limit < 1 || request.Limit > 100 ||
		request.StartedAt.IsZero() || request.Deadline.IsZero() || request.StartedAt.Location() != time.UTC || request.Deadline.Location() != time.UTC ||
		!request.Deadline.After(request.StartedAt) || request.Deadline.Sub(request.StartedAt) > time.Minute ||
		!request.Deadline.After(time.Now().Add(-time.Minute)) {
		return false
	}
	_, err := privacy.NewScanner([]string{request.ForbiddenCanary})
	return err == nil
}

// safePrivacyLangfuseOpaque 与 Grafana privacy surface 共用同一套不透明 ID 校验：
// 允许 [A-Za-z0-9._-] 且禁止敏感词，任何引号/斜杠/换行/管道/百分号/非 ASCII 都会被拒绝，
// 从字符集层面排除 filter/query 注入与身份伪造。
func safePrivacyLangfuseOpaque(value string) bool {
	return safePrivacyGrafanaOpaque(value)
}

// validPrivacyLangfuseSnapshot 证明本地存在恰好一条与请求完全一致的已投递快照。
// 任何字段缺失、身份漂移、非 sent 状态、负 attempt、时间越界都 fail-closed；
// 平台没有义务修正本地事实，adapter 也没有权利猜测。
func validPrivacyLangfuseSnapshot(snapshot localeval.ScoreProjectionSnapshot, request PrivacyLangfuseScanRequest) bool {
	return snapshot.RunID == request.Marker && snapshot.RequestID == request.RequestID && snapshot.AITraceID == request.AITraceID &&
		snapshot.PlatformTraceID == request.ServiceTraceID && snapshot.PlatformObservationID == request.SpanID &&
		snapshot.ProjectionID != "" && snapshot.EvalRunID != "" && snapshot.Attempt >= 0 &&
		snapshot.Status == langfuse.ScoreProjectionStatusSent && !snapshot.CreatedAt.IsZero() && !snapshot.ObservedAt.IsZero() &&
		!snapshot.CreatedAt.After(snapshot.ObservedAt) &&
		!snapshot.ObservedAt.Before(request.StartedAt) && snapshot.ObservedAt.Before(request.Deadline)
}

// validatePrivacyLangfuseTraceDocument 校验锁定版本 observations v1 返回的语义事实：
// 恰好一行、meta 单页单条目、行身份/时间窗/metadata 与请求完全一致。它与 scan 解耦，
// 保证语义歧义不会把已确认泄漏清零。
func validatePrivacyLangfuseTraceDocument(document any, request PrivacyLangfuseScanRequest) error {
	root, ok := document.(map[string]any)
	rows, rowsOK := root["data"].([]any)
	meta, metaOK := root["meta"].(map[string]any)
	page, pageOK := privacyLangfuseInt(meta["page"])
	limit, limitOK := privacyLangfuseInt(meta["limit"])
	totalItems, itemsOK := privacyLangfuseInt(meta["totalItems"])
	totalPages, pagesOK := privacyLangfuseInt(meta["totalPages"])
	if !ok || !rowsOK || !metaOK || !pageOK || page != 1 || !limitOK || limit != request.Limit ||
		!itemsOK || !pagesOK || totalPages != 1 || totalItems != len(rows) || len(rows) != 1 {
		return errPrivacyLangfuseSurface
	}
	row, ok := rows[0].(map[string]any)
	id, idOK := row["id"].(string)
	traceID, traceOK := row["traceId"].(string)
	started, startOK := privacyLangfuseTime(row["startTime"])
	metadata, metadataOK := row["metadata"].(map[string]any)
	if !ok || !idOK || id != request.SpanID || !traceOK || traceID != request.ServiceTraceID ||
		!startOK || !privacyLangfuseInWindow(started, request) || !metadataOK ||
		metadata["longtermism.smoke.run_id"] != request.Marker || metadata["request_id"] != request.RequestID ||
		metadata["ai_trace_id"] != request.AITraceID {
		return errPrivacyLangfuseSurface
	}
	return nil
}

// validatePrivacyLangfuseScoreDocument 校验锁定版本 scores v3 返回的语义事实：meta 单页
// 且 cursor 为空（不允许分页追逐）、恰好一行、score id/subject/timestamp 与本地已投递
// projection 完全一致。
func validatePrivacyLangfuseScoreDocument(document any, request PrivacyLangfuseScanRequest, projectionID string) error {
	root, ok := document.(map[string]any)
	rows, rowsOK := root["data"].([]any)
	meta, metaOK := root["meta"].(map[string]any)
	limit, limitOK := privacyLangfuseInt(meta["limit"])
	if !ok || !rowsOK || !metaOK || !limitOK || limit != request.Limit || len(rows) != 1 {
		return errPrivacyLangfuseSurface
	}
	if cursor, present := meta["cursor"]; present && cursor != nil {
		return errPrivacyLangfuseSurface
	}
	row, ok := rows[0].(map[string]any)
	id, idOK := row["id"].(string)
	subject, subjectOK := row["subject"].(map[string]any)
	observed, timeOK := privacyLangfuseTime(row["timestamp"])
	if !ok || !idOK || id != projectionID || !subjectOK || subject["kind"] != "observation" ||
		subject["id"] != request.SpanID || subject["traceId"] != request.ServiceTraceID ||
		!timeOK || !privacyLangfuseInWindow(observed, request) {
		return errPrivacyLangfuseSurface
	}
	return nil
}

// privacyLangfuseInt 只接受 json.Number（bounded decode 已启用 UseNumber），拒绝浮点与
// 负数，防止把畸形 meta 当成单页证据。
func privacyLangfuseInt(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	if err != nil || parsed < 0 || parsed > int64(^uint(0)>>1) {
		return 0, false
	}
	return int(parsed), true
}

func privacyLangfuseTime(value any) (time.Time, bool) {
	rendered, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, rendered)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func privacyLangfuseInWindow(value time.Time, request PrivacyLangfuseScanRequest) bool {
	return !value.Before(request.StartedAt) && value.Before(request.Deadline)
}

func privacyLangfuseHasLeaks(counts map[string]int) bool {
	for _, count := range counts {
		if count > 0 {
			return true
		}
	}
	return false
}

func clonePrivacyLangfuseCounts(input map[string]int) map[string]int {
	result := make(map[string]int, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

type privacyLangfuseError struct{}

func (privacyLangfuseError) Error() string { return "privacy Langfuse surface unavailable" }
func (privacyLangfuseError) Class() string { return "unexpected_evidence" }
func (privacyLangfuseError) Unwrap() error { return errPrivacyLangfuseSurface }
func newPrivacyLangfuseError() error       { return privacyLangfuseError{} }

var _ json.Marshaler = PrivacyLangfuseSurfaceEvidence{}
