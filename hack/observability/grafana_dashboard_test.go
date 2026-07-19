package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const overviewDashboardRelativePath = "../../deploy/observability/grafana/dashboards/observability-overview.json"
const grafanaDatasourcesRelativePath = "../../deploy/observability/grafana/provisioning/datasources.yaml"

// TestGrafanaOverviewDashboardContract 固定 Grafana 首屏需要回答的运行问题。
// 这里刻意只接受 provisioned UID；生产排障时依赖展示名或默认数据源会让
// dashboard 在复制、重建或多数据源环境中悄悄查询错误的后端。
func TestGrafanaOverviewDashboardContract(t *testing.T) {
	dashboard := loadOverviewDashboard(t)
	panels := dashboardPanels(t, dashboard)
	assertTargetedPanelsUseProvisionedDatasourceUIDs(t, panels)

	tests := []struct {
		name                     string
		title                    string
		datasourceUID            string
		queryFragments           []string
		requiresTraceCorrelation bool
	}{
		{
			name:           "request rate uses Prometheus",
			title:          "HTTP Request Rate",
			datasourceUID:  "prometheus",
			queryFragments: []string{"longtermism_http_server_request_count"},
		},
		{
			name:           "error rate uses Prometheus",
			title:          "HTTP Error Rate",
			datasourceUID:  "prometheus",
			queryFragments: []string{"longtermism_http_server_request_count", "http_response_status_class", "5xx"},
		},
		{
			name:           "latency uses Prometheus",
			title:          "HTTP Latency p95",
			datasourceUID:  "prometheus",
			queryFragments: []string{"histogram_quantile", "longtermism_http_server_request_duration"},
		},
		{
			name:           "export failure uses Collector metrics",
			title:          "Collector Export Failures",
			datasourceUID:  "prometheus",
			queryFragments: []string{"otelcol_exporter"},
		},
		{
			name:           "queue backlog uses Collector metrics",
			title:          "Collector Queue Backlog",
			datasourceUID:  "prometheus",
			queryFragments: []string{"otelcol_exporter_queue"},
		},
		{
			name:          "logs link to Tempo trace",
			title:         "Logs to Trace Correlation",
			datasourceUID: "loki",
			// trace_id is structured metadata after filelog moves the fixed message into
			// the log body. line_format deliberately renders it for Grafana's derived
			// field, which then provides the per-record Tempo data link.
			queryFragments:           []string{"trace_id", "line_format", "{{.trace_id}}"},
			requiresTraceCorrelation: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := findPanelByTitle(t, panels, tt.title)
			assertDatasourceUID(t, panel, tt.datasourceUID)
			assertPanelQuery(t, panel, tt.queryFragments)
			assertNoHighCardinalityMetricLabels(t, panel)
			if tt.title == "Collector Export Failures" {
				assertComponentScopedFailureQueries(t, panel)
			}
			if tt.title == "Collector Queue Backlog" {
				assertComponentScopedQueueQueries(t, panel)
			}
			if tt.requiresTraceCorrelation {
				assertLokiTempoDerivedField(t, panel)
			}
		})
	}
}

func loadOverviewDashboard(t *testing.T) map[string]any {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate dashboard contract source")
	}
	body, err := os.ReadFile(filepath.Clean(filepath.Join(filepath.Dir(sourcePath), overviewDashboardRelativePath)))
	if err != nil {
		t.Fatalf("read overview dashboard: %v", err)
	}

	var dashboard map[string]any
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatalf("decode overview dashboard JSON: %v", err)
	}
	return dashboard
}

func dashboardPanels(t *testing.T, dashboard map[string]any) []map[string]any {
	t.Helper()
	rawPanels, ok := dashboard["panels"].([]any)
	if !ok || len(rawPanels) == 0 {
		t.Fatal("overview dashboard must define panels")
	}

	panels := make([]map[string]any, 0, len(rawPanels))
	for _, rawPanel := range rawPanels {
		panel, ok := rawPanel.(map[string]any)
		if !ok {
			t.Fatal("overview dashboard panel must be an object")
		}
		panels = append(panels, panel)
	}
	return panels
}

func assertTargetedPanelsUseProvisionedDatasourceUIDs(t *testing.T, panels []map[string]any) {
	t.Helper()
	allowedUIDs := map[string]bool{"prometheus": true, "loki": true, "tempo": true}
	for _, panel := range panels {
		if _, hasTargets := panel["targets"]; !hasTargets {
			continue
		}
		if !allowedUIDs[panelDatasourceUID(panel)] {
			t.Fatalf("panel %q uses an unprovisioned datasource UID %q", panel["title"], panelDatasourceUID(panel))
		}
	}
}

func findPanelByTitle(t *testing.T, panels []map[string]any, title string) map[string]any {
	t.Helper()
	for _, panel := range panels {
		if panel["title"] == title {
			return panel
		}
	}
	t.Fatalf("overview dashboard missing %q panel", title)
	return nil
}

func assertDatasourceUID(t *testing.T, panel map[string]any, want string) {
	t.Helper()
	datasource, ok := panel["datasource"].(map[string]any)
	if !ok || datasource["uid"] != want {
		t.Fatalf("panel %q datasource UID = %#v, want %q", panel["title"], panel["datasource"], want)
	}
}

func assertPanelQuery(t *testing.T, panel map[string]any, fragments []string) {
	t.Helper()
	query := panelQuery(t, panel)
	for _, fragment := range fragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("panel %q query is missing required fragment %q", panel["title"], fragment)
		}
	}
}

func panelQuery(t *testing.T, panel map[string]any) string {
	t.Helper()
	return strings.Join(panelQueries(t, panel), "\n")
}

func panelQueries(t *testing.T, panel map[string]any) []string {
	t.Helper()
	targets, ok := panel["targets"].([]any)
	if !ok || len(targets) == 0 {
		t.Fatalf("panel %q must define query targets", panel["title"])
	}

	queries := make([]string, 0, len(targets))
	for _, rawTarget := range targets {
		target, ok := rawTarget.(map[string]any)
		if !ok {
			t.Fatalf("panel %q target must be an object", panel["title"])
		}
		expr, ok := target["expr"].(string)
		if !ok || strings.TrimSpace(expr) == "" {
			t.Fatalf("panel %q target must define a non-empty expr", panel["title"])
		}
		queries = append(queries, expr)
	}
	return queries
}

var metricSelectorPattern = regexp.MustCompile(`\{([^}]*)\}`)
var promQLLabelListPattern = regexp.MustCompile(`\b(?:by|without|on|ignoring|group_left|group_right)\s*\(([^)]*)\)`)
var promQLDerivedLabelPattern = regexp.MustCompile(`\b(?:count_values|label_replace|label_join|sort_by_label|sort_by_label_desc)\s*\(`)
var quotedStringPattern = regexp.MustCompile(`"([^"]+)"`)
var exporterMatcherPattern = regexp.MustCompile(`(?:^|,)\s*exporter\s*(?:=|=~)\s*"([^"]+)"`)
var forbiddenMetricLabels = []string{
	"request_id", "trace_id", "span_id", "ai_trace_id", "session_id", "user_id", "run_id", "prompt_hash", "raw_route", "smoke_run_id",
}

func assertNoHighCardinalityMetricLabels(t *testing.T, panel map[string]any) {
	t.Helper()
	if panelDatasourceUID(panel) != "prometheus" {
		return
	}
	query := panelQuery(t, panel)
	for _, selector := range metricSelectorPattern.FindAllStringSubmatch(query, -1) {
		assertNoForbiddenMetricLabel(t, panel, selector[1])
	}
	for _, labels := range promQLLabelListPattern.FindAllStringSubmatch(query, -1) {
		assertNoForbiddenMetricLabel(t, panel, labels[1])
	}
	for _, call := range promQLFunctionCalls(query, promQLDerivedLabelPattern) {
		for _, quoted := range quotedStringPattern.FindAllStringSubmatch(call, -1) {
			assertNoForbiddenMetricLabel(t, panel, quoted[1])
		}
	}
}

func assertNoForbiddenMetricLabel(t *testing.T, panel map[string]any, labels string) {
	t.Helper()
	for _, label := range forbiddenMetricLabels {
		if regexp.MustCompile(`(^|,)\s*` + regexp.QuoteMeta(label) + `\s*(?:=|!|~|,|$)`).MatchString(labels) {
			t.Fatalf("panel %q uses forbidden high-cardinality metric label %q", panel["title"], label)
		}
	}
}

func panelDatasourceUID(panel map[string]any) string {
	datasource, _ := panel["datasource"].(map[string]any)
	uid, _ := datasource["uid"].(string)
	return uid
}

func assertLokiTempoDerivedField(t *testing.T, panel map[string]any) {
	t.Helper()
	if _, hasPanelLink := panel["links"]; hasPanelLink {
		t.Fatalf("panel %q must rely on the per-record Loki derived field, not a panel-level trace link", panel["title"])
	}

	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate dashboard contract source")
	}
	datasources, err := os.ReadFile(filepath.Clean(filepath.Join(filepath.Dir(sourcePath), grafanaDatasourcesRelativePath)))
	if err != nil {
		t.Fatalf("read Grafana datasource provisioning: %v", err)
	}
	for _, fragment := range []string{"name: TraceID", "matcherRegex: '([a-f0-9]{32})'", "url: '${__value.raw}'", "datasourceUid: tempo"} {
		if !strings.Contains(string(datasources), fragment) {
			t.Fatalf("Loki datasource derived field is missing required fragment %q", fragment)
		}
	}
}

func assertComponentScopedFailureQueries(t *testing.T, panel map[string]any) {
	t.Helper()
	requirements := []struct {
		component string
		fragments []string
	}{
		{"otlp/tempo", []string{"otelcol_exporter_send_failed_spans_total", "otelcol_exporter_enqueue_failed_spans_total"}},
		{"otlphttp/loki", []string{"otelcol_exporter_send_failed_log_records_total", "otelcol_exporter_enqueue_failed_log_records_total"}},
		{"otlphttp/langfuse", []string{"otelcol_exporter_send_failed_spans_total", "otelcol_exporter_enqueue_failed_spans_total"}},
	}
	for _, requirement := range requirements {
		assertTargetHasComponentMetrics(t, panel, requirement.component, requirement.fragments)
	}
}

func assertComponentScopedQueueQueries(t *testing.T, panel map[string]any) {
	t.Helper()
	for _, component := range []string{"otlp/tempo", "otlphttp/loki", "otlphttp/langfuse"} {
		assertTargetHasComponentMetrics(t, panel, component, []string{"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity"})
	}
}

func assertTargetHasComponentMetrics(t *testing.T, panel map[string]any, component string, metrics []string) {
	t.Helper()
	for _, query := range panelQueries(t, panel) {
		if hasAllComponentMetricSelectors(query, component, metrics) {
			return
		}
	}
	t.Fatalf("panel %q has no component-scoped query for %q", panel["title"], component)
}

func hasAllComponentMetricSelectors(query, component string, metrics []string) bool {
	for _, metric := range metrics {
		selectorPattern := regexp.MustCompile(regexp.QuoteMeta(metric) + `\s*\{([^}]*)\}`)
		found := false
		for _, selector := range selectorPattern.FindAllStringSubmatch(query, -1) {
			if selectorHasExporterComponent(selector[1], component) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func selectorHasExporterComponent(selector, component string) bool {
	for _, matcher := range exporterMatcherPattern.FindAllStringSubmatch(selector, -1) {
		if strings.Contains(matcher[1], component) {
			return true
		}
	}
	return false
}

func promQLFunctionCalls(query string, pattern *regexp.Regexp) []string {
	starts := pattern.FindAllStringIndex(query, -1)
	calls := make([]string, 0, len(starts))
	for _, start := range starts {
		open := strings.LastIndex(query[start[0]:start[1]], "(") + start[0]
		if call, ok := balancedCall(query, open); ok {
			calls = append(calls, call)
		}
	}
	return calls
}

func balancedCall(query string, open int) (string, bool) {
	depth := 0
	inString := false
	escaped := false
	for index := open; index < len(query); index++ {
		character := query[index]
		if inString {
			if character == '\\' && !escaped {
				escaped = true
				continue
			}
			if character == '"' && !escaped {
				inString = false
			}
			escaped = false
			continue
		}
		if character == '"' {
			inString = true
		} else if character == '(' {
			depth++
		} else if character == ')' {
			depth--
			if depth == 0 {
				return query[open+1 : index], true
			}
		}
	}
	return "", false
}
