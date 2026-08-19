// Package failure 是 US3 故障恢复能力的领域骨架：把"失败域"钉死在它们
// 唯一真实的证据源上，供故障注入 runner（exporter/persistent_queue/
// storage/score worker）与观测资产（T133 dashboard）共享同一份静态事实。
//
// 生产约束（FR-007 / data-model §13）：Prometheus scrape、Grafana query 与
// score worker 的失败各有自己的证据源，绝不能借用 otelcol_exporter_send_failed
// 之类 Collector exporter 信号冒充；反之 exporter 失败也不得用 pull/query
// 遥测诊断。本包的目录就是这道边界的唯一事实源。
package failure

import "slices"

// Domain 标识一个可注入、可诊断的失败域。
type Domain string

const (
	// DomainTempoExporter：OTLP traces 出口（组件 otlp/tempo）投递失败。
	DomainTempoExporter Domain = "tempo_exporter"
	// DomainLokiExporter：OTLP logs 出口（组件 otlphttp/loki）投递失败。
	DomainLokiExporter Domain = "loki_exporter"
	// DomainLangfuseExporter：AI 平面出口（组件 otlphttp/langfuse）投递失败。
	DomainLangfuseExporter Domain = "langfuse_exporter"
	// DomainPrometheusScrape：Prometheus 抓取应用/Collector target 失败。
	DomainPrometheusScrape Domain = "prometheus_scrape"
	// DomainGrafanaQuery：Grafana datasource 健康/查询失败。
	DomainGrafanaQuery Domain = "grafana_query"
	// DomainScoreWorker：评分投影 worker 的队列/同步失败。
	DomainScoreWorker Domain = "score_worker"
	// DomainQueueFull：Collector exporter 持久队列达到容量上限。
	DomainQueueFull Domain = "queue_full"
	// DomainStorageUnwritable：Collector 持久队列落盘路径不可写。
	DomainStorageUnwritable Domain = "storage_unwritable"
	// DomainCollectorRestart：Collector 重启（含跨重启队列恢复验证）。
	DomainCollectorRestart Domain = "collector_restart"
	// DomainCollectorShutdown：Collector 停机（含 shutdown 超时观察）。
	DomainCollectorShutdown Domain = "collector_shutdown"
	// DomainModelUpstream：模型服务自身失败（429/5xx/timeout）。这是业务失败域，
	// 与全部观测证据源隔离，防止观测故障被归类为模型故障或反之（FR-007）。
	DomainModelUpstream Domain = "model_upstream"
)

// EvidenceSource 标识一个真实证据源。目录的作用就是保证失败域只引用自己
// 的证据源，不与其他域的遥测混用。
type EvidenceSource string

const (
	// EvidenceCollectorComponentTelemetry：Collector exporter 组件自身遥测
	// （otelcol_exporter_send_failed_* / enqueue_failed_* 指标族）。
	EvidenceCollectorComponentTelemetry EvidenceSource = "collector_component_telemetry"
	// EvidenceCollectorQueueSnapshot：Collector exporter 持久队列快照
	// （queue_size/queue_capacity/dropped）。
	EvidenceCollectorQueueSnapshot EvidenceSource = "collector_queue_snapshot"
	// EvidenceCollectorStorageError：持久队列 file_storage 落盘错误。
	EvidenceCollectorStorageError EvidenceSource = "collector_storage_error"
	// EvidenceCollectorLifecycle：容器生命周期状态（stop/restart/exit state）。
	EvidenceCollectorLifecycle EvidenceSource = "collector_lifecycle"
	// EvidencePrometheusTargetTelemetry：target up / scrape duration/error 遥测。
	EvidencePrometheusTargetTelemetry EvidenceSource = "prometheus_target_telemetry"
	// EvidenceGrafanaDatasource：datasource health 与 query 结果。
	EvidenceGrafanaDatasource EvidenceSource = "grafana_datasource"
	// EvidenceScoreWorkerTelemetry：score worker queued/sent/failed/dropped。
	EvidenceScoreWorkerTelemetry EvidenceSource = "score_worker_telemetry"
	// EvidenceProviderResponse：模型 provider 的响应状态（429/5xx/timeout）。
	EvidenceProviderResponse EvidenceSource = "provider_response"
)

// Definition 是单个失败域的静态目录条目。字段值与真实部署资产逐字一致
// （collector-grafana.yaml 的组件/队列名、observability.rules.yaml 的指标族），
// T110 契约测试会把这些值当作漂移检测器。
type Definition struct {
	Domain Domain
	// EvidenceSources 该域允许引用的真实证据源。
	EvidenceSources []EvidenceSource
	// ExporterMetricName 仅 exporter 域：Collector 组件 send_failed 指标族。
	// 非 exporter 域必须为空——Prometheus/Grafana/score worker 不得伪造
	// otelcol_exporter_send_failed（data-model §13）。
	ExporterMetricName string
	// CollectorComponentID 仅 exporter 域：Collector 组件 ID。
	CollectorComponentID string
	// StorageQueueName 仅 exporter 域：持久队列 storage 名。
	StorageQueueName string
	// Forbidden 该域明确禁止混用的证据源。它把"证据源不得混用"编码成
	// 可断言的不变量，而不是依赖调用方自觉。
	Forbidden []EvidenceSource
}

// catalog 是静态事实表。顺序即 AllDomains 的返回顺序，保持稳定以便报告
// 与测试断言。
var catalog = []Definition{
	{
		Domain:               DomainTempoExporter,
		EvidenceSources:      []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot},
		ExporterMetricName:   "otelcol_exporter_send_failed_spans_total",
		CollectorComponentID: "otlp/tempo",
		StorageQueueName:     "tempo",
		Forbidden:            nonExporterEvidenceSources(),
	},
	{
		Domain:               DomainLokiExporter,
		EvidenceSources:      []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot},
		ExporterMetricName:   "otelcol_exporter_send_failed_log_records_total",
		CollectorComponentID: "otlphttp/loki",
		StorageQueueName:     "loki",
		Forbidden:            nonExporterEvidenceSources(),
	},
	{
		Domain:               DomainLangfuseExporter,
		EvidenceSources:      []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot},
		ExporterMetricName:   "otelcol_exporter_send_failed_spans_total",
		CollectorComponentID: "otlphttp/langfuse",
		StorageQueueName:     "langfuse",
		Forbidden:            nonExporterEvidenceSources(),
	},
	{
		Domain:          DomainPrometheusScrape,
		EvidenceSources: []EvidenceSource{EvidencePrometheusTargetTelemetry},
		Forbidden:       []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot, EvidenceCollectorStorageError, EvidenceScoreWorkerTelemetry, EvidenceProviderResponse},
	},
	{
		Domain:          DomainGrafanaQuery,
		EvidenceSources: []EvidenceSource{EvidenceGrafanaDatasource},
		Forbidden:       []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot, EvidencePrometheusTargetTelemetry, EvidenceScoreWorkerTelemetry, EvidenceProviderResponse},
	},
	{
		Domain:          DomainScoreWorker,
		EvidenceSources: []EvidenceSource{EvidenceScoreWorkerTelemetry},
		Forbidden:       []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot, EvidenceCollectorStorageError, EvidenceCollectorLifecycle, EvidencePrometheusTargetTelemetry, EvidenceGrafanaDatasource, EvidenceProviderResponse},
	},
	{
		Domain:          DomainQueueFull,
		EvidenceSources: []EvidenceSource{EvidenceCollectorQueueSnapshot},
		Forbidden:       []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorStorageError, EvidenceCollectorLifecycle, EvidencePrometheusTargetTelemetry, EvidenceGrafanaDatasource, EvidenceScoreWorkerTelemetry, EvidenceProviderResponse},
	},
	{
		Domain:          DomainStorageUnwritable,
		EvidenceSources: []EvidenceSource{EvidenceCollectorStorageError},
		Forbidden:       []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot, EvidenceCollectorLifecycle, EvidencePrometheusTargetTelemetry, EvidenceGrafanaDatasource, EvidenceScoreWorkerTelemetry, EvidenceProviderResponse},
	},
	{
		Domain:          DomainCollectorRestart,
		EvidenceSources: []EvidenceSource{EvidenceCollectorLifecycle, EvidenceCollectorQueueSnapshot},
		Forbidden:       nonCollectorEvidenceSources(),
	},
	{
		Domain:          DomainCollectorShutdown,
		EvidenceSources: []EvidenceSource{EvidenceCollectorLifecycle, EvidenceCollectorQueueSnapshot},
		Forbidden:       nonCollectorEvidenceSources(),
	},
	{
		Domain:          DomainModelUpstream,
		EvidenceSources: []EvidenceSource{EvidenceProviderResponse},
		Forbidden:       allObservabilityEvidenceSources(),
	},
}

// nonExporterEvidenceSources 是 exporter 域禁止引用的证据源：pull/query/
// 业务侧遥测不能冒充投递故障证据。
func nonExporterEvidenceSources() []EvidenceSource {
	return []EvidenceSource{
		EvidencePrometheusTargetTelemetry,
		EvidenceGrafanaDatasource,
		EvidenceScoreWorkerTelemetry,
		EvidenceProviderResponse,
	}
}

// nonCollectorEvidenceSources 是 lifecycle 域禁止引用的证据源：容器状态
// 不能借用业务或 pull/query 遥测。
func nonCollectorEvidenceSources() []EvidenceSource {
	return []EvidenceSource{
		EvidencePrometheusTargetTelemetry,
		EvidenceGrafanaDatasource,
		EvidenceScoreWorkerTelemetry,
		EvidenceProviderResponse,
		EvidenceCollectorStorageError,
	}
}

// allObservabilityEvidenceSources 是模型故障域必须隔离的全部观测证据源。
// 模型失败只有 provider 响应一个证据源，其余全部禁止（FR-007 双向隔离）。
func allObservabilityEvidenceSources() []EvidenceSource {
	return []EvidenceSource{
		EvidenceCollectorComponentTelemetry,
		EvidenceCollectorQueueSnapshot,
		EvidenceCollectorStorageError,
		EvidenceCollectorLifecycle,
		EvidencePrometheusTargetTelemetry,
		EvidenceGrafanaDatasource,
		EvidenceScoreWorkerTelemetry,
	}
}

// Lookup 返回失败域的目录条目（深拷贝切片，保护静态目录不被调用方改写）。
// 未知域返回 ok=false：调用方不得猜测证据源，缺失即事实缺失（语义优先约束）。
func Lookup(domain Domain) (Definition, bool) {
	for _, definition := range catalog {
		if definition.Domain == domain {
			return cloneDefinition(definition), true
		}
	}
	return Definition{}, false
}

func cloneDefinition(definition Definition) Definition {
	definition.EvidenceSources = slices.Clone(definition.EvidenceSources)
	definition.Forbidden = slices.Clone(definition.Forbidden)
	return definition
}

// AllDomains 返回目录声明的全部失败域，顺序稳定。
func AllDomains() []Domain {
	domains := make([]Domain, 0, len(catalog))
	for _, definition := range catalog {
		domains = append(domains, definition.Domain)
	}
	return domains
}
