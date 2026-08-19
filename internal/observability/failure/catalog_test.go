package failure

import (
	"slices"
	"testing"
)

// T110 失败域目录契约测试（RED 先行，T120 实现 catalog.go 使其 GREEN）。
//
// 覆盖的生产风险：观测故障与业务故障互相归因。目录必须把每个失败域钉在它
// 唯一真实的证据源上——Prometheus scrape 的证据是 target up/scrape 遥测、
// Grafana 的证据是 datasource query 结果、score worker 的证据是 queue 遥测、
// 模型故障的证据是 provider 响应；任何域都不允许借用 `otelcol_exporter_*
// send_failed` 信号冒充自己的失败（对应 FR-007 与 data-model §13）。
//
// 测试写死的事实来自 deploy/observability 资产，保证目录与真实部署不漂移：
// - collector-grafana.yaml: exporter 组件 ID `otlp/tempo`、`otlphttp/loki`、
//   `otlphttp/langfuse`，持久队列 storage 名 `tempo`/`loki`/`langfuse`。
// - observability.rules.yaml: tempo/langfuse 的失败信号为
//   `otelcol_exporter_send_failed_spans_total`，loki 为
//   `otelcol_exporter_send_failed_log_records_total`。

func allDomainsFixture() []Domain {
	return []Domain{
		DomainTempoExporter,
		DomainLokiExporter,
		DomainLangfuseExporter,
		DomainPrometheusScrape,
		DomainGrafanaQuery,
		DomainScoreWorker,
		DomainQueueFull,
		DomainStorageUnwritable,
		DomainCollectorRestart,
		DomainCollectorShutdown,
		DomainModelUpstream,
	}
}

func allObservabilitySources() []EvidenceSource {
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

func TestCatalogCoversExactlyTheDeclaredDomains(t *testing.T) {
	domains := AllDomains()
	if len(domains) != len(allDomainsFixture()) {
		t.Fatalf("AllDomains() size = %d, want %d", len(domains), len(allDomainsFixture()))
	}
	for _, want := range allDomainsFixture() {
		if !slices.Contains(domains, want) {
			t.Errorf("AllDomains() 缺少失败域 %q", want)
		}
	}
}

func TestLookupResolvesEveryDeclaredDomain(t *testing.T) {
	for _, domain := range allDomainsFixture() {
		def, ok := Lookup(domain)
		if !ok {
			t.Fatalf("Lookup(%q) ok = false, want true", domain)
		}
		if def.Domain != domain {
			t.Errorf("Lookup(%q).Domain = %q, want %q", domain, def.Domain, domain)
		}
	}
}

func TestLookupUnknownDomainRejected(t *testing.T) {
	if _, ok := Lookup("not_a_failure_domain"); ok {
		t.Fatal("Lookup(unknown) ok = true, want false：未知域不得被猜测出证据源")
	}
}

// 三个 exporter 域的证据必须是 Collector 组件遥测 + 各自持久队列快照，且
// 指标族名、组件 ID、队列名与真实部署资产逐字一致。
func TestExporterDomainsCarryRealCollectorFacts(t *testing.T) {
	tests := []struct {
		domain      Domain
		wantMetric  string
		wantComp    string
		wantQueue   string
	}{
		{DomainTempoExporter, "otelcol_exporter_send_failed_spans_total", "otlp/tempo", "tempo"},
		{DomainLokiExporter, "otelcol_exporter_send_failed_log_records_total", "otlphttp/loki", "loki"},
		{DomainLangfuseExporter, "otelcol_exporter_send_failed_spans_total", "otlphttp/langfuse", "langfuse"},
	}
	for _, tc := range tests {
		t.Run(string(tc.domain), func(t *testing.T) {
			def, ok := Lookup(tc.domain)
			if !ok {
				t.Fatalf("Lookup(%q) 未解析", tc.domain)
			}
			if !slices.Contains(def.EvidenceSources, EvidenceCollectorComponentTelemetry) {
				t.Errorf("证据源缺少 %q：exporter 失败必须由 Collector 组件遥测证明", EvidenceCollectorComponentTelemetry)
			}
			if !slices.Contains(def.EvidenceSources, EvidenceCollectorQueueSnapshot) {
				t.Errorf("证据源缺少 %q：exporter 故障期间必须能看到对应持久队列快照", EvidenceCollectorQueueSnapshot)
			}
			if def.ExporterMetricName != tc.wantMetric {
				t.Errorf("ExporterMetricName = %q, want %q", def.ExporterMetricName, tc.wantMetric)
			}
			if def.CollectorComponentID != tc.wantComp {
				t.Errorf("CollectorComponentID = %q, want %q", def.CollectorComponentID, tc.wantComp)
			}
			if def.StorageQueueName != tc.wantQueue {
				t.Errorf("StorageQueueName = %q, want %q", def.StorageQueueName, tc.wantQueue)
			}
		})
	}
}

// Prometheus scrape 的证据是 target up/scrape 遥测，绝不能借用
// `otelcol_exporter_send_failed`（data-model §13 明确禁止冒充 exporter 证据）。
func TestPrometheusScrapeUsesOnlyTargetTelemetry(t *testing.T) {
	def, ok := Lookup(DomainPrometheusScrape)
	if !ok {
		t.Fatal("Lookup(DomainPrometheusScrape) 未解析")
	}
	if !slices.Equal(def.EvidenceSources, []EvidenceSource{EvidencePrometheusTargetTelemetry}) {
		t.Errorf("EvidenceSources = %v, want 仅 [%s]", def.EvidenceSources, EvidencePrometheusTargetTelemetry)
	}
	if def.ExporterMetricName != "" {
		t.Errorf("ExporterMetricName = %q, want 空：Prometheus scrape 不得使用 otelcol exporter 指标", def.ExporterMetricName)
	}
	if def.CollectorComponentID != "" || def.StorageQueueName != "" {
		t.Errorf("组件 ID/队列名应为空，got %q/%q", def.CollectorComponentID, def.StorageQueueName)
	}
	for _, forbidden := range []EvidenceSource{EvidenceCollectorComponentTelemetry, EvidenceCollectorQueueSnapshot} {
		if !slices.Contains(def.Forbidden, forbidden) {
			t.Errorf("Forbidden 缺少 %q：scrape 失败不得混入 Collector 证据", forbidden)
		}
	}
}

// Grafana query 的证据是 datasource health/query 结果，不是 Collector 投递指标。
func TestGrafanaQueryUsesOnlyDatasourceEvidence(t *testing.T) {
	def, ok := Lookup(DomainGrafanaQuery)
	if !ok {
		t.Fatal("Lookup(DomainGrafanaQuery) 未解析")
	}
	if !slices.Equal(def.EvidenceSources, []EvidenceSource{EvidenceGrafanaDatasource}) {
		t.Errorf("EvidenceSources = %v, want 仅 [%s]", def.EvidenceSources, EvidenceGrafanaDatasource)
	}
	if def.ExporterMetricName != "" {
		t.Errorf("ExporterMetricName = %q, want 空", def.ExporterMetricName)
	}
	if !slices.Contains(def.Forbidden, EvidenceCollectorComponentTelemetry) {
		t.Error("Forbidden 缺少 collector_component_telemetry")
	}
}

// score worker 的证据是 queued/sent/failed/dropped 队列遥测，不是 otelcol 信号。
func TestScoreWorkerUsesOnlyWorkerTelemetry(t *testing.T) {
	def, ok := Lookup(DomainScoreWorker)
	if !ok {
		t.Fatal("Lookup(DomainScoreWorker) 未解析")
	}
	if !slices.Equal(def.EvidenceSources, []EvidenceSource{EvidenceScoreWorkerTelemetry}) {
		t.Errorf("EvidenceSources = %v, want 仅 [%s]", def.EvidenceSources, EvidenceScoreWorkerTelemetry)
	}
	if def.ExporterMetricName != "" {
		t.Errorf("ExporterMetricName = %q, want 空：score worker 不得伪造 otelcol_exporter_send_failed", def.ExporterMetricName)
	}
	if !slices.Contains(def.Forbidden, EvidenceCollectorComponentTelemetry) {
		t.Error("Forbidden 缺少 collector_component_telemetry")
	}
}

// queue full 的证据是队列快照（queue_size/capacity/dropped），与 send_failed 区分：
// 积压是队列现象，投递失败是 exporter 现象，二者不得互相替代。
func TestQueueFullUsesOnlyQueueSnapshot(t *testing.T) {
	def, ok := Lookup(DomainQueueFull)
	if !ok {
		t.Fatal("Lookup(DomainQueueFull) 未解析")
	}
	if !slices.Equal(def.EvidenceSources, []EvidenceSource{EvidenceCollectorQueueSnapshot}) {
		t.Errorf("EvidenceSources = %v, want 仅 [%s]", def.EvidenceSources, EvidenceCollectorQueueSnapshot)
	}
	if def.ExporterMetricName != "" {
		t.Errorf("ExporterMetricName = %q, want 空", def.ExporterMetricName)
	}
	if !slices.Contains(def.Forbidden, EvidenceCollectorComponentTelemetry) {
		t.Error("Forbidden 缺少 collector_component_telemetry：queue full 不得用 send_failed 诊断")
	}
}

// 磁盘不可写是持久队列存储故障，必须由 storage error 证据证明，且不得借用
// exporter send_failed（T124 要求报告中保留 preflight 与 runtime storage 区别）。
func TestStorageUnwritableUsesOnlyStorageError(t *testing.T) {
	def, ok := Lookup(DomainStorageUnwritable)
	if !ok {
		t.Fatal("Lookup(DomainStorageUnwritable) 未解析")
	}
	if !slices.Equal(def.EvidenceSources, []EvidenceSource{EvidenceCollectorStorageError}) {
		t.Errorf("EvidenceSources = %v, want 仅 [%s]", def.EvidenceSources, EvidenceCollectorStorageError)
	}
	if def.ExporterMetricName != "" {
		t.Errorf("ExporterMetricName = %q, want 空", def.ExporterMetricName)
	}
	if !slices.Contains(def.Forbidden, EvidenceCollectorComponentTelemetry) {
		t.Error("Forbidden 缺少 collector_component_telemetry")
	}
}

// restart/shutdown 的证据是容器生命周期状态 + 队列快照（T113 drain 证据来源）。
func TestCollectorLifecycleDomainsUseLifecycleAndQueueEvidence(t *testing.T) {
	wantSources := []EvidenceSource{EvidenceCollectorLifecycle, EvidenceCollectorQueueSnapshot}
	for _, domain := range []Domain{DomainCollectorRestart, DomainCollectorShutdown} {
		t.Run(string(domain), func(t *testing.T) {
			def, ok := Lookup(domain)
			if !ok {
				t.Fatalf("Lookup(%q) 未解析", domain)
			}
			if !slices.Equal(def.EvidenceSources, wantSources) {
				t.Errorf("EvidenceSources = %v, want %v", def.EvidenceSources, wantSources)
			}
			if def.ExporterMetricName != "" || def.CollectorComponentID != "" || def.StorageQueueName != "" {
				t.Errorf("指标/组件/队列字段应为空，got %q/%q/%q",
					def.ExporterMetricName, def.CollectorComponentID, def.StorageQueueName)
			}
			for _, forbidden := range []EvidenceSource{
				EvidencePrometheusTargetTelemetry,
				EvidenceGrafanaDatasource,
				EvidenceScoreWorkerTelemetry,
				EvidenceProviderResponse,
			} {
				if !slices.Contains(def.Forbidden, forbidden) {
					t.Errorf("Forbidden 缺少 %q", forbidden)
				}
			}
		})
	}
}

// FR-007 核心断言：模型故障的证据只有 provider 响应（429/5xx/timeout），
// 与全部观测证据源隔离——模型失败不得被归类为观测投递故障。
func TestModelUpstreamIsolatedFromAllObservabilitySources(t *testing.T) {
	def, ok := Lookup(DomainModelUpstream)
	if !ok {
		t.Fatal("Lookup(DomainModelUpstream) 未解析")
	}
	if !slices.Equal(def.EvidenceSources, []EvidenceSource{EvidenceProviderResponse}) {
		t.Errorf("EvidenceSources = %v, want 仅 [%s]", def.EvidenceSources, EvidenceProviderResponse)
	}
	if def.ExporterMetricName != "" || def.CollectorComponentID != "" || def.StorageQueueName != "" {
		t.Errorf("指标/组件/队列字段应为空，got %q/%q/%q",
			def.ExporterMetricName, def.CollectorComponentID, def.StorageQueueName)
	}
	for _, forbidden := range allObservabilitySources() {
		if !slices.Contains(def.Forbidden, forbidden) {
			t.Errorf("Forbidden 缺少 %q：模型故障不得被归类为任何观测投递故障", forbidden)
		}
	}
}

// 全局不变量：任何域的允许证据源与其禁止证据源不得重叠（证据源不混用），
// 且非 exporter 域不得携带 exporter 指标族。
func TestNoEvidenceSourceMixingInvariant(t *testing.T) {
	for _, domain := range allDomainsFixture() {
		t.Run(string(domain), func(t *testing.T) {
			def, ok := Lookup(domain)
			if !ok {
				t.Fatalf("Lookup(%q) 未解析", domain)
			}
			if len(def.EvidenceSources) == 0 {
				t.Error("EvidenceSources 为空：每个失败域必须有真实证据源")
			}
			if len(def.Forbidden) == 0 {
				t.Error("Forbidden 为空：每个失败域都必须显式声明禁止混用的证据源")
			}
			for _, source := range def.EvidenceSources {
				if slices.Contains(def.Forbidden, source) {
					t.Errorf("证据源 %q 同时出现在允许与禁止列表中", source)
				}
			}
			isExporterDomain := domain == DomainTempoExporter ||
				domain == DomainLokiExporter ||
				domain == DomainLangfuseExporter
			if def.ExporterMetricName != "" && !isExporterDomain {
				t.Errorf("非 exporter 域 %q 携带了 exporter 指标 %q", domain, def.ExporterMetricName)
			}
			if def.ExporterMetricName == "" && isExporterDomain {
				t.Errorf("exporter 域 %q 缺少 ExporterMetricName", domain)
			}
		})
	}
}
