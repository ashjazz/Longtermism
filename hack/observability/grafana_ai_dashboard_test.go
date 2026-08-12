package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const aiDashboardRelativePath = "../../deploy/observability/grafana/dashboards/observability-overview.json"

var forbiddenDashboardMetricLabels = []string{
	"request_id",
	"trace_id",
	"span_id",
	"ai_trace_id",
	"session_id",
	"user_id",
	"prompt_hash",
	"eval_run_id",
	"run_id",
	"smoke_run_id",
	"raw_route",
	"service_trace_id",
}

type aiDashboardDocument struct {
	UID    string             `json:"uid"`
	Title  string             `json:"title"`
	Panels []aiDashboardPanel `json:"panels"`
	Raw    map[string]any     `json:"-"`
}

type aiDashboardPanel struct {
	ID         int                 `json:"id"`
	Title      string              `json:"title"`
	Type       string              `json:"type"`
	Datasource aiDashboardSource   `json:"datasource"`
	Targets    []aiDashboardTarget `json:"targets"`
}

type aiDashboardSource struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

type aiDashboardTarget struct {
	Expr string `json:"expr"`
}

// TestGrafanaAIDashboardContract 将可观测性 dashboard 视为发布资产而不是手工 UI
// 截图。它固定能用低基数 Prometheus 事实回答的运营问题；真实数据是否出现由 smoke
// 与 alert 的后续任务验证。
func TestGrafanaAIDashboardContract(t *testing.T) {
	dashboard := loadAIDashboard(t)
	if dashboard.UID != "longtermism-observability-overview" || strings.TrimSpace(dashboard.Title) == "" || len(dashboard.Panels) == 0 {
		t.Fatal("dashboard must have the stable overview UID, a title, and panels")
	}

	panels := panelsByTitle(t, dashboard.Panels)
	tests := []struct {
		name           string
		title          string
		datasourceType string
		datasourceUID  string
		fragments      []string
	}{
		{name: "LLM request outcome", title: "LLM Requests", datasourceType: "prometheus", datasourceUID: "prometheus", fragments: []string{"longtermism_llm_request_count_total", "gen_ai_provider_name", "outcome"}},
		{name: "LLM duration", title: "LLM Duration p95", datasourceType: "prometheus", datasourceUID: "prometheus", fragments: []string{"histogram_quantile", "longtermism_llm_duration_seconds_bucket", "gen_ai_provider_name"}},
		{name: "LLM tokens and cost", title: "LLM Tokens and Cost", datasourceType: "prometheus", datasourceUID: "prometheus", fragments: []string{"longtermism_llm_tokens_token_total", "longtermism_llm_cost_total", "gen_ai_token_type", `currency="USD"`}},
		{name: "evaluation and regression", title: "Evaluation Results", datasourceType: "prometheus", datasourceUID: "prometheus", fragments: []string{"longtermism_eval_result_total", "longtermism_eval_score_bucket", "status", "failed|error"}},
		{name: "Langfuse score projection", title: "Langfuse Score Projection", datasourceType: "prometheus", datasourceUID: "prometheus", fragments: []string{"longtermism_score_projection_total", "backend", "status"}},
		{name: "Langfuse score worker queue", title: "Langfuse Score Worker Queue", datasourceType: "prometheus", datasourceUID: "prometheus", fragments: []string{"longtermism_score_worker_queue", "backend"}},
		{name: "infrastructure trace correlation", title: "Logs to Trace Correlation", datasourceType: "loki", datasourceUID: "loki", fragments: []string{"trace_id", "service_name"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel, exists := panels[tt.title]
			if !exists || panel.Type == "" || panel.Datasource.Type != tt.datasourceType || panel.Datasource.UID != tt.datasourceUID || len(panel.Targets) == 0 {
				t.Fatalf("dashboard panel %q present=%t, want executable %s panel using provisioned datasource %q", tt.title, exists, tt.datasourceType, tt.datasourceUID)
			}
			expression := panelExpressions(panel)
			for _, fragment := range tt.fragments {
				if !strings.Contains(expression, fragment) {
					t.Fatalf("dashboard panel %q is missing required query fragment %q", tt.title, fragment)
				}
			}
		})
	}

	for _, panel := range rawDashboardPanels(dashboard.Raw["panels"]) {
		if rawPanelDatasourceType(panel) != "prometheus" {
			continue
		}
		assertNoForbiddenMetricLabels(t, "Prometheus dashboard panel", stringsFromValue(panel))
	}
	assertNoForbiddenMetricLabels(t, "Grafana dashboard variable", stringsFromValue(dashboard.Raw["templating"]))
}

func loadAIDashboard(t *testing.T) aiDashboardDocument {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Grafana AI dashboard contract source")
	}
	body, err := os.ReadFile(filepath.Clean(filepath.Join(filepath.Dir(sourcePath), aiDashboardRelativePath)))
	if err != nil {
		t.Fatalf("read Grafana AI dashboard: %v", err)
	}
	var dashboard aiDashboardDocument
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatalf("decode Grafana AI dashboard JSON: %v", err)
	}
	if err := json.Unmarshal(body, &dashboard.Raw); err != nil {
		t.Fatalf("decode Grafana AI dashboard raw JSON: %v", err)
	}
	return dashboard
}

// rawDashboardPanels descends into Grafana rows as well as the top-level
// dashboard. Rows are layout containers, but their child panels can still
// query Prometheus; skipping them would create a high-cardinality escape hatch.
func rawDashboardPanels(value any) []map[string]any {
	var panels []map[string]any
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			panels = append(panels, typed)
			if children, exists := typed["panels"]; exists {
				visit(children)
			}
		}
	}
	visit(value)
	return panels
}

func rawPanelDatasourceType(panel map[string]any) string {
	datasource, _ := panel["datasource"].(map[string]any)
	datasourceType, _ := datasource["type"].(string)
	return datasourceType
}

// stringsFromValue walks all dashboard configuration fields that can embed a
// metric label: PromQL targets, legends, transformations, links, and variable
// queries. Limiting the policy to targets[].expr would leave an easy path for
// accidental exposure through Grafana UI metadata.
func stringsFromValue(value any) []string {
	var stringsInValue []string
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case string:
			stringsInValue = append(stringsInValue, typed)
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			for _, item := range typed {
				visit(item)
			}
		}
	}
	visit(value)
	return stringsInValue
}

func assertNoForbiddenMetricLabels(t *testing.T, subject string, values []string) {
	t.Helper()
	for _, value := range values {
		lowerValue := strings.ToLower(value)
		for _, forbidden := range forbiddenDashboardMetricLabels {
			if strings.Contains(lowerValue, forbidden) {
				t.Fatalf("%s uses forbidden high-cardinality label %q", subject, forbidden)
			}
		}
	}
}

func panelsByTitle(t *testing.T, panels []aiDashboardPanel) map[string]aiDashboardPanel {
	t.Helper()
	byTitle := make(map[string]aiDashboardPanel, len(panels))
	ids := make(map[int]struct{}, len(panels))
	for _, panel := range panels {
		if panel.ID <= 0 || strings.TrimSpace(panel.Title) == "" {
			t.Fatal("dashboard panel must have a positive ID and title")
		}
		if _, exists := byTitle[panel.Title]; exists {
			t.Fatalf("dashboard repeats panel title %q", panel.Title)
		}
		if _, exists := ids[panel.ID]; exists {
			t.Fatalf("dashboard repeats panel ID %d", panel.ID)
		}
		byTitle[panel.Title] = panel
		ids[panel.ID] = struct{}{}
	}
	return byTitle
}

func panelExpressions(panel aiDashboardPanel) string {
	expressions := make([]string, 0, len(panel.Targets))
	for _, target := range panel.Targets {
		expressions = append(expressions, target.Expr)
	}
	return strings.Join(expressions, "\n")
}
