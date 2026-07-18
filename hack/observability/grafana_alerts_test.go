package observability

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const alertRulesRelativePath = "../../deploy/observability/grafana/alerts/observability.rules.yaml"

var sensitiveAlertKeyPattern = regexp.MustCompile(`(?i)authorization|api.?key|credential|password|secret|token|request.?id|trace.?id|span.?id|prompt|raw|payload|endpoint`)

type alertProvisioning struct {
	APIVersion int              `yaml:"apiVersion"`
	Groups     []alertRuleGroup `yaml:"groups"`
}

type alertRuleGroup struct {
	Interval string      `yaml:"interval"`
	Rules    []alertRule `yaml:"rules"`
}

type alertRule struct {
	UID          string            `yaml:"uid"`
	Title        string            `yaml:"title"`
	Condition    string            `yaml:"condition"`
	For          string            `yaml:"for"`
	NoDataState  string            `yaml:"noDataState"`
	ExecErrState string            `yaml:"execErrState"`
	Labels       map[string]string `yaml:"labels"`
	Annotations  map[string]string `yaml:"annotations"`
	Data         []alertQuery      `yaml:"data"`
}

type alertQuery struct {
	DatasourceUID string         `yaml:"datasourceUid"`
	RefID         string         `yaml:"refId"`
	TimeRange     relativeRange  `yaml:"relativeTimeRange"`
	Model         map[string]any `yaml:"model"`
}

type relativeRange struct {
	From int `yaml:"from"`
	To   int `yaml:"to"`
}

// TestGrafanaAlertRulesContract 固定初版告警的失效域边界。
// 这里验证的是配置能表达“触发后可恢复”的条件；实际 firing/resolved 证据由 T117
// 的受控故障场景验证，静态文件不能把“规则存在”伪装成“告警已生效”。
func TestGrafanaAlertRulesContract(t *testing.T) {
	provisioning := loadAlertProvisioning(t)
	rules := flattenAlertRules(t, provisioning)
	assertUniqueRuleUIDs(t, rules)
	for _, rule := range rules {
		assertLowSensitivityMetadata(t, rule)
		queries := prometheusExpressions(t, rule)
		assertExecutableCondition(t, rule)
		assertNoHighCardinalityPromQLLabels(t, rule, strings.Join(queries, "\n"))
	}

	tests := []struct {
		name           string
		uid            string
		queryFragments []string
		components     []string
	}{
		{
			name:           "HTTP error rate",
			uid:            "longtermism-http-error-rate",
			queryFragments: []string{"rate(", "longtermism_http_server_request_count", "http_response_status_class", "5xx", "clamp_min", ">"},
		},
		{
			name:           "exporter delivery failure",
			uid:            "longtermism-exporter-delivery-failure",
			queryFragments: []string{"otelcol_exporter_send_failed", "otelcol_exporter_enqueue_failed", ">"},
			components:     []string{"otlp/tempo", "otlphttp/loki", "otlphttp/langfuse"},
		},
		{
			name:           "exporter queue saturation",
			uid:            "longtermism-exporter-queue-saturation",
			queryFragments: []string{"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity", "/", ">"},
			components:     []string{"otlp/tempo", "otlphttp/loki", "otlphttp/langfuse"},
		},
		{
			name:           "exporter queue age",
			uid:            "longtermism-exporter-queue-age",
			queryFragments: []string{"queue", "age", ">"},
			components:     []string{"otlp/tempo", "otlphttp/loki", "otlphttp/langfuse"},
		},
		{
			name:           "Collector storage pressure",
			uid:            "longtermism-collector-storage-pressure",
			queryFragments: []string{"longtermism_collector_storage_utilization_ratio", ">"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := findRuleByUID(t, rules, tt.uid)
			assertRecoverableRuleState(t, rule)
			query := strings.Join(conditionPrometheusExpressions(t, rule), "\n")
			assertQueryFragments(t, rule, query, tt.queryFragments)
			assertComponentMetrics(t, rule, query)
			assertComponentSelectors(t, rule, query, tt.components)
		})
	}
}

func loadAlertProvisioning(t *testing.T) alertProvisioning {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate alert contract source")
	}
	body, err := os.ReadFile(filepath.Clean(filepath.Join(filepath.Dir(sourcePath), alertRulesRelativePath)))
	if err != nil {
		t.Fatalf("read alert rules: %v", err)
	}
	assertNoSensitiveYAMLComments(t, string(body))

	var provisioning alertProvisioning
	if err := yaml.Unmarshal(body, &provisioning); err != nil {
		t.Fatalf("decode alert rules YAML: %v", err)
	}
	if provisioning.APIVersion != 1 || len(provisioning.Groups) == 0 {
		t.Fatal("alert provisioning must define apiVersion 1 and non-empty groups")
	}
	return provisioning
}

func assertNoSensitiveYAMLComments(t *testing.T, document string) {
	t.Helper()
	for _, line := range strings.Split(document, "\n") {
		commentIndex := strings.Index(line, "#")
		if commentIndex >= 0 && sensitiveAlertKeyPattern.MatchString(line[commentIndex+1:]) {
			t.Fatal("alert rule comments must not contain sensitive or high-cardinality data")
		}
	}
}

func flattenAlertRules(t *testing.T, provisioning alertProvisioning) []alertRule {
	t.Helper()
	rules := make([]alertRule, 0)
	for _, group := range provisioning.Groups {
		if strings.TrimSpace(group.Interval) == "" || len(group.Rules) == 0 {
			t.Fatal("every alert rule group must define an interval and rules")
		}
		rules = append(rules, group.Rules...)
	}
	return rules
}

func assertUniqueRuleUIDs(t *testing.T, rules []alertRule) {
	t.Helper()
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if !regexp.MustCompile(`^[a-z0-9_-]{1,40}$`).MatchString(rule.UID) {
			t.Fatalf("rule UID %q must be stable Grafana-safe text", rule.UID)
		}
		if _, exists := seen[rule.UID]; exists {
			t.Fatalf("duplicate alert rule UID %q", rule.UID)
		}
		seen[rule.UID] = struct{}{}
	}
}

func findRuleByUID(t *testing.T, rules []alertRule, uid string) alertRule {
	t.Helper()
	for _, rule := range rules {
		if rule.UID == uid {
			return rule
		}
	}
	t.Fatalf("missing alert rule %q", uid)
	return alertRule{}
}

func assertRecoverableRuleState(t *testing.T, rule alertRule) {
	t.Helper()
	duration, err := time.ParseDuration(rule.For)
	if strings.TrimSpace(rule.Title) == "" || strings.TrimSpace(rule.Condition) == "" || err != nil || duration <= 0 {
		t.Fatalf("rule %q must define title, condition, and a non-zero for window", rule.UID)
	}
	if rule.NoDataState != "OK" || rule.ExecErrState != "Error" {
		t.Fatalf("rule %q must keep no-data separate from query errors for reliable resolution", rule.UID)
	}
}

func assertLowSensitivityMetadata(t *testing.T, rule alertRule) {
	t.Helper()
	if strings.TrimSpace(rule.Annotations["summary"]) == "" || strings.TrimSpace(rule.Annotations["runbook"]) == "" {
		t.Fatalf("rule %q must provide low-sensitivity summary and runbook annotations", rule.UID)
	}
	for category, values := range map[string]map[string]string{"labels": rule.Labels, "annotations": rule.Annotations} {
		for key, value := range values {
			if sensitiveAlertKeyPattern.MatchString(key) || sensitiveAlertKeyPattern.MatchString(value) {
				t.Fatalf("rule %q %s contains sensitive or high-cardinality data", rule.UID, category)
			}
		}
	}
}

func prometheusExpressions(t *testing.T, rule alertRule) []string {
	t.Helper()
	expressions := make([]string, 0)
	for _, query := range rule.Data {
		if query.DatasourceUID != "prometheus" {
			continue
		}
		expression, ok := query.Model["expr"].(string)
		if ok && strings.TrimSpace(expression) != "" {
			expressions = append(expressions, expression)
		}
	}
	if len(expressions) == 0 {
		t.Fatalf("rule %q must contain a Prometheus expression query", rule.UID)
	}
	return expressions
}

func assertExecutableCondition(t *testing.T, rule alertRule) {
	t.Helper()
	queries := alertQueriesByRefID(t, rule)
	condition, exists := queries[rule.Condition]
	if !exists || condition.DatasourceUID != "__expr__" || !hasThreshold(condition.Model) {
		t.Fatalf("rule %q condition %q must reference a Grafana expression with a threshold", rule.UID, rule.Condition)
	}
	dependencies, valid := prometheusDependencies(rule.Condition, queries, map[string]bool{})
	if !valid || len(dependencies) == 0 {
		t.Fatalf("rule %q condition %q must depend on a Prometheus query", rule.UID, rule.Condition)
	}
}

func alertQueriesByRefID(t *testing.T, rule alertRule) map[string]alertQuery {
	t.Helper()
	queries := make(map[string]alertQuery, len(rule.Data))
	for _, query := range rule.Data {
		if query.RefID == "" || queries[query.RefID].RefID != "" {
			t.Fatalf("rule %q must use unique non-empty query refIds", rule.UID)
		}
		if query.DatasourceUID == "prometheus" {
			if query.TimeRange.From <= 0 || query.TimeRange.To != 0 || strings.TrimSpace(stringModel(query.Model, "expr")) == "" {
				t.Fatalf("rule %q Prometheus query %q must define a bounded lookback and expr", rule.UID, query.RefID)
			}
		} else if query.DatasourceUID != "__expr__" {
			t.Fatalf("rule %q query %q uses unsupported datasource UID %q", rule.UID, query.RefID, query.DatasourceUID)
		}
		queries[query.RefID] = query
	}
	return queries
}

func conditionPrometheusExpressions(t *testing.T, rule alertRule) []string {
	t.Helper()
	queries := alertQueriesByRefID(t, rule)
	dependencies, valid := prometheusDependencies(rule.Condition, queries, map[string]bool{})
	if !valid {
		t.Fatalf("rule %q condition contains a cyclic expression dependency", rule.UID)
	}
	expressions := make([]string, 0, len(dependencies))
	for refID := range dependencies {
		expressions = append(expressions, stringModel(queries[refID].Model, "expr"))
	}
	if len(expressions) == 0 {
		t.Fatalf("rule %q condition must reach a Prometheus expression", rule.UID)
	}
	return expressions
}

func stringModel(model map[string]any, key string) string {
	value, _ := model[key].(string)
	return value
}

func hasThreshold(model map[string]any) bool {
	expression := stringModel(model, "expression")
	if strings.Contains(expression, ">") || strings.Contains(expression, "<") {
		return true
	}
	conditions, hasConditions := model["conditions"].([]any)
	if !hasConditions || len(conditions) == 0 {
		return false
	}
	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]any)
		if !ok || condition["type"] != "query" {
			return false
		}
		evaluator, ok := condition["evaluator"].(map[string]any)
		query, hasQuery := condition["query"].(map[string]any)
		params, hasParams := query["params"].([]any)
		if !ok || !hasQuery || !hasParams || len(params) == 0 || strings.TrimSpace(stringModel(evaluator, "type")) == "" {
			return false
		}
	}
	return true
}

func prometheusDependencies(refID string, queries map[string]alertQuery, visiting map[string]bool) (map[string]struct{}, bool) {
	if visiting[refID] {
		return nil, false
	}
	visiting[refID] = true
	defer delete(visiting, refID)
	query, exists := queries[refID]
	if !exists {
		return nil, false
	}
	if query.DatasourceUID == "prometheus" {
		return map[string]struct{}{refID: {}}, true
	}
	dependencies := make(map[string]struct{})
	for _, candidate := range modelRefIDs(query.Model) {
		if _, exists := queries[candidate]; !exists {
			return nil, false
		}
		candidateDependencies, valid := prometheusDependencies(candidate, queries, visiting)
		if !valid {
			return nil, false
		}
		for dependency := range candidateDependencies {
			dependencies[dependency] = struct{}{}
		}
	}
	return dependencies, true
}

func modelReferencesRefID(model map[string]any, refID string) bool {
	for _, candidate := range modelRefIDs(model) {
		if candidate == refID {
			return true
		}
	}
	return false
}

func modelRefIDs(model map[string]any) []string {
	references := make([]string, 0)
	expression := strings.TrimSpace(stringModel(model, "expression"))
	if expression != "" {
		for _, match := range regexp.MustCompile(`\$([A-Za-z][A-Za-z0-9_]*)`).FindAllStringSubmatch(expression, -1) {
			references = append(references, match[1])
		}
		if regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`).MatchString(expression) {
			references = append(references, expression)
		}
	}
	conditions, _ := model["conditions"].([]any)
	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]any)
		if !ok {
			continue
		}
		query, ok := condition["query"].(map[string]any)
		if !ok {
			continue
		}
		params, ok := query["params"].([]any)
		if !ok {
			continue
		}
		if reference, ok := params[0].(string); ok {
			references = append(references, reference)
		}
	}
	return references
}

func assertQueryFragments(t *testing.T, rule alertRule, query string, fragments []string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(strings.ToLower(query), strings.ToLower(fragment)) {
			t.Fatalf("rule %q query is missing required fragment %q", rule.UID, fragment)
		}
	}
}

func assertNoHighCardinalityPromQLLabels(t *testing.T, rule alertRule, query string) {
	t.Helper()
	for _, label := range []string{"request_id", "trace_id", "span_id", "ai_trace_id", "session_id", "user_id", "run_id", "prompt_hash", "raw_route", "smoke_run_id"} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(label) + `\b`).MatchString(query) {
			t.Fatalf("rule %q query uses forbidden high-cardinality label %q", rule.UID, label)
		}
	}
}

func assertComponentSelectors(t *testing.T, rule alertRule, query string, components []string) {
	t.Helper()
	for _, component := range components {
		if !hasExporterSelector(query, component) {
			t.Fatalf("rule %q query does not scope stable exporter component %q", rule.UID, component)
		}
	}
}

func assertComponentMetrics(t *testing.T, rule alertRule, query string) {
	t.Helper()
	components := []string{"otlp/tempo", "otlphttp/loki", "otlphttp/langfuse"}
	if rule.UID == "longtermism-exporter-delivery-failure" {
		for _, component := range components {
			metrics := []string{"otelcol_exporter_send_failed_spans", "otelcol_exporter_enqueue_failed_spans"}
			if component == "otlphttp/loki" {
				metrics = []string{"otelcol_exporter_send_failed_log_records", "otelcol_exporter_enqueue_failed_log_records"}
			}
			assertMetricSelectorsForComponent(t, rule, query, component, metrics)
		}
	}
	if rule.UID == "longtermism-exporter-queue-saturation" {
		for _, component := range components {
			assertMetricSelectorsForComponent(t, rule, query, component, []string{"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity"})
		}
	}
	if rule.UID == "longtermism-exporter-queue-age" {
		for _, component := range components {
			assertQueueAgeSelectorForComponent(t, rule, query, component)
		}
	}
	if rule.UID == "longtermism-collector-storage-pressure" {
		for _, storage := range []string{"file_storage/tempo", "file_storage/loki", "file_storage/langfuse"} {
			assertStorageSelector(t, rule, query, storage)
		}
	}
}

func assertMetricSelectorsForComponent(t *testing.T, rule alertRule, query, component string, metrics []string) {
	t.Helper()
	for _, metric := range metrics {
		pattern := regexp.MustCompile(regexp.QuoteMeta(metric) + `\s*\{([^}]*)\}`)
		matched := false
		for _, selector := range pattern.FindAllStringSubmatch(query, -1) {
			if hasExporterSelector(selector[1], component) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("rule %q must scope %q to exporter %q", rule.UID, metric, component)
		}
	}
}

func assertQueueAgeSelectorForComponent(t *testing.T, rule alertRule, query, component string) {
	t.Helper()
	pattern := regexp.MustCompile(`(?i)[a-z_:][a-z0-9_:]*queue[a-z0-9_:]*age[a-z0-9_:]*\s*\{([^}]*)\}`)
	for _, selector := range pattern.FindAllStringSubmatch(query, -1) {
		if hasExporterSelector(selector[1], component) {
			return
		}
	}
	t.Fatalf("rule %q must scope a queue-age metric to exporter %q", rule.UID, component)
}

func assertStorageSelector(t *testing.T, rule alertRule, query, storage string) {
	t.Helper()
	pattern := regexp.MustCompile(`longtermism_collector_storage_utilization_ratio\s*\{([^}]*)\}`)
	for _, selector := range pattern.FindAllStringSubmatch(query, -1) {
		if strings.Contains(selector[1], storage) && regexp.MustCompile(`(?:^|,)\s*storage\s*=~?\s*"`).MatchString(selector[1]) {
			return
		}
	}
	t.Fatalf("rule %q must scope storage evidence to %q", rule.UID, storage)
}

func hasExporterSelector(query, component string) bool {
	pattern := `(?:^|[,{])\s*exporter\s*=~?\s*"[^"]*` + regexp.QuoteMeta(component) + `[^"]*"`
	return regexp.MustCompile(pattern).MatchString(query)
}
