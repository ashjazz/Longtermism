package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ashjazz/Longtermism/internal/observability/failure"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// T130 能力收敛的第一块：Collector 组件遥测快照后端。
//
// 它把 Prometheus 上的 otelcol_exporter_* 指标族投影为 smoke runner 需要的
// 按组件快照（ExporterHealthSnapshot / PersistentQueueSnapshot），是
// exporter-failure 与 persistent-queue live composition 的 Backend 实现。
//
// 组件事实只从 failure 目录派生（T120 单一事实源）：组件 ID 与 send_failed
// 指标族来自 catalog，信号族（spans/log_records）从指标名后缀解析——adapter
// 不重复声明任何组件映射，目录漂移时这里直接失效而不是静默错查。
//
// 缺样语义：Prometheus 对从未活动过的 counter 不返回序列。快照解码把空向量
// 视为 0——这是"无流量即无计数"的真实事实（官方 exporterhelper 文档确认
// 失败计数只在事件发生时增长），不是伪造证据；真正需要"必须存在"的
// 事实（如故障注入后的 send_failed 增量）由 runner 的断言强制。

var errMalformedCollectorSnapshot = fmt.Errorf("%w: malformed collector telemetry snapshot", errMalformedSmokeEvidence)

// collectorInstantQuery 是 Prometheus instant 查询的窄端口。
type collectorInstantQuery interface {
	QueryPrometheus(context.Context, string) (BackendQueryResult, error)
}

// GrafanaCollectorSnapshotBackend 查询真实 Collector exporter 组件遥测。
type GrafanaCollectorSnapshotBackend struct {
	client collectorInstantQuery
}

func NewGrafanaCollectorSnapshotBackend(client collectorInstantQuery) *GrafanaCollectorSnapshotBackend {
	return &GrafanaCollectorSnapshotBackend{client: client}
}

// collectorComponent 是目录投影到快照查询的最小事实。
type collectorComponent struct {
	ComponentID string
	Signal      string // spans | log_records
}

// collectorComponents 从 failure 目录派生三个真实出口的查询事实。
func collectorComponents() []collectorComponent {
	components := make([]collectorComponent, 0, 3)
	for _, domain := range []failure.Domain{
		failure.DomainTempoExporter,
		failure.DomainLokiExporter,
		failure.DomainLangfuseExporter,
	} {
		definition, ok := failure.Lookup(domain)
		if !ok {
			continue
		}
		// 目录的 ExporterMetricName 形如 otelcol_exporter_send_failed_<signal>_total。
		signal := strings.TrimPrefix(definition.ExporterMetricName, "otelcol_exporter_send_failed_")
		signal = strings.TrimSuffix(signal, "_total")
		if signal != "spans" && signal != "log_records" {
			continue
		}
		components = append(components, collectorComponent{ComponentID: definition.CollectorComponentID, Signal: signal})
	}
	return components
}

// SnapshotCollectorHealth 实现 smoke.ExporterFailureSmokeBackend：每个真实
// exporter 组件一份只读证据快照。
func (b *GrafanaCollectorSnapshotBackend) SnapshotCollectorHealth(ctx context.Context) ([]smoke.ExporterHealthSnapshot, error) {
	snapshots := make([]smoke.ExporterHealthSnapshot, 0, 3)
	for _, component := range collectorComponents() {
		sent, err := b.queryCollectorMetric(ctx, "otelcol_exporter_sent_"+component.Signal+"_total", component.ComponentID)
		if err != nil {
			return nil, err
		}
		sendFailed, err := b.queryCollectorMetric(ctx, "otelcol_exporter_send_failed_"+component.Signal+"_total", component.ComponentID)
		if err != nil {
			return nil, err
		}
		enqueueFailed, err := b.queryCollectorMetric(ctx, "otelcol_exporter_enqueue_failed_"+component.Signal+"_total", component.ComponentID)
		if err != nil {
			return nil, err
		}
		queueSize, err := b.queryCollectorMetric(ctx, "otelcol_exporter_queue_size", component.ComponentID)
		if err != nil {
			return nil, err
		}
		queueCapacity, err := b.queryCollectorMetric(ctx, "otelcol_exporter_queue_capacity", component.ComponentID)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, smoke.ExporterHealthSnapshot{
			ComponentID:   component.ComponentID,
			Sent:          sent,
			SendFailed:    sendFailed,
			EnqueueFailed: enqueueFailed,
			QueueSize:     queueSize,
			QueueCapacity: queueCapacity,
			// otelcol 没有独立的 dropped 计数器：丢弃事实表现为 enqueue_failed
			//（官方 exporterhelper 文档），runner 的归因不消费该字段。
			Dropped: 0,
		})
	}
	return snapshots, nil
}

// SnapshotCollectorQueue 实现 smoke.PersistentQueueSmokeBackend：固定以
// Tempo 出口（otlp/tempo）验证跨重启投递（与 T130 full aggregate 的
// persistent-queue 子场景目标一致）。
func (b *GrafanaCollectorSnapshotBackend) SnapshotCollectorQueue(ctx context.Context) (smoke.PersistentQueueSnapshot, error) {
	definition, ok := failure.Lookup(failure.DomainTempoExporter)
	if !ok {
		return smoke.PersistentQueueSnapshot{}, errMalformedCollectorSnapshot
	}
	componentID := definition.CollectorComponentID
	signal := strings.TrimSuffix(strings.TrimPrefix(definition.ExporterMetricName, "otelcol_exporter_send_failed_"), "_total")

	sent, err := b.queryCollectorMetric(ctx, "otelcol_exporter_sent_"+signal+"_total", componentID)
	if err != nil {
		return smoke.PersistentQueueSnapshot{}, err
	}
	sendFailed, err := b.queryCollectorMetric(ctx, "otelcol_exporter_send_failed_"+signal+"_total", componentID)
	if err != nil {
		return smoke.PersistentQueueSnapshot{}, err
	}
	enqueueFailed, err := b.queryCollectorMetric(ctx, "otelcol_exporter_enqueue_failed_"+signal+"_total", componentID)
	if err != nil {
		return smoke.PersistentQueueSnapshot{}, err
	}
	queueSize, err := b.queryCollectorMetric(ctx, "otelcol_exporter_queue_size", componentID)
	if err != nil {
		return smoke.PersistentQueueSnapshot{}, err
	}
	queueCapacity, err := b.queryCollectorMetric(ctx, "otelcol_exporter_queue_capacity", componentID)
	if err != nil {
		return smoke.PersistentQueueSnapshot{}, err
	}
	return smoke.PersistentQueueSnapshot{
		ComponentID:   componentID,
		QueueSize:     queueSize,
		QueueCapacity: queueCapacity,
		Sent:          sent,
		SendFailed:    sendFailed,
		EnqueueFailed: enqueueFailed,
	}, nil
}

// queryCollectorMetric 查询单个 exporter 组件指标并解码为整数。空向量
// 解码为 0（无活动即无计数）；小数或畸形 payload 拒绝。
func (b *GrafanaCollectorSnapshotBackend) queryCollectorMetric(ctx context.Context, metric, componentID string) (int64, error) {
	expression := fmt.Sprintf("%s{exporter=%q}", metric, componentID)
	result, err := b.client.QueryPrometheus(ctx, expression)
	if err != nil {
		return 0, err
	}
	return decodeCollectorCounter(result)
}

func decodeCollectorCounter(result BackendQueryResult) (int64, error) {
	var response prometheusVectorResponse
	if err := result.Decode(&response); err != nil {
		return 0, errMalformedCollectorSnapshot
	}
	if response.Status != "success" || response.Data.ResultType != "vector" {
		return 0, errMalformedCollectorSnapshot
	}
	if len(response.Data.Result) == 0 {
		return 0, nil
	}
	var total int64
	for _, sample := range response.Data.Result {
		value, err := decodeCollectorSample(sample.Value)
		if err != nil {
			return 0, err
		}
		total += value
	}
	return total, nil
}

func decodeCollectorSample(value []json.RawMessage) (int64, error) {
	if len(value) != 2 {
		return 0, errMalformedCollectorSnapshot
	}
	var rendered string
	if err := json.Unmarshal(value[1], &rendered); err != nil {
		return 0, errMalformedCollectorSnapshot
	}
	parsed, err := strconv.ParseFloat(rendered, 64)
	if err != nil {
		return 0, errMalformedCollectorSnapshot
	}
	if parsed != float64(int64(parsed)) {
		return 0, errMalformedCollectorSnapshot
	}
	return int64(parsed), nil
}
