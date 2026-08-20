package observability

// T140：SigNoz 备选 profile 的 dashboard/checklist 发布资产 RED 契约测试。
// 实现前两个目标文件都不存在，本测试必须在加载阶段失败；
// T145 落地 deploy/observability/signoz/dashboard.json、T146 落地
// specs/003-real-observability-backends/checklists/signoz.md 后转 GREEN。
//
// 契约定位（与 Grafana 主线 dashboard 测试的差异）：
//   1. 不要求复刻 Grafana JSON——SigNoz 用自己的 dashboard 结构与查询语言，
//      测试只假设最小结构：顶层 panels 数组，每个 panel 有 title 与
//      queries[].query 字符串；布局、图表类型、变量全部留给实现者。
//   2. 但面板必须回答与主线等价的运营问题（T145 门控）：
//      request / error / latency / export failure / token+cost / eval correlation，
//      且指标名必须命中主线已验证的同源低基数事实，不允许虚构另一套指标。
//   3. AI/score 到 Langfuse 的跳转说明必须保留：SigNoz 只承接基础设施三信号，
//      AI trace 与 score 投影的证据面仍在 Langfuse，dashboard 上不能悄悄删掉这条边界。
//   4. 高基数标签禁区与主线完全一致（forbiddenDashboardMetricLabels）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const signozDashboardRelativePath = "../../deploy/observability/signoz/dashboard.json"
const signozChecklistRelativePath = "../../specs/003-real-observability-backends/checklists/signoz.md"

type signozDashboardDocument struct {
	Title  string            `json:"title"`
	Panels []signozDashboard `json:"panels"`
	Raw    map[string]any    `json:"-"`
}

type signozDashboard struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Queries     []signozDashQuery `json:"queries"`
}

type signozDashQuery struct {
	Query string `json:"query"`
}

// TestSignozDashboardContract 固定备选 profile 的首屏运营问题。
// 每个面板的查询必须引用主线同源指标名；export failure 面板必须保留
// per-exporter component 归因（分出口失败证据是 SC-003/SC-004 的基础）。
func TestSignozDashboardContract(t *testing.T) {
	dashboard := loadSignozDashboard(t)
	if strings.TrimSpace(dashboard.Title) == "" || len(dashboard.Panels) == 0 {
		t.Fatal("signoz dashboard must have a title and panels")
	}
	panels := signozPanelsByTitle(t, dashboard.Panels)

	tests := []struct {
		name        string
		title       string
		fragments   []string
		description string
	}{
		{
			name:      "request rate answers the serving-plane question",
			title:     "HTTP Request Rate",
			fragments: []string{"longtermism_http_server_request_count_total"},
		},
		{
			name:      "error rate separates failures",
			title:     "HTTP Error Rate",
			fragments: []string{"http_response_status_class", "5xx"},
		},
		{
			name:      "latency keeps the p95 question",
			title:     "HTTP Latency p95",
			fragments: []string{"longtermism_http_server_request_duration", "0.95"},
		},
		{
			name:  "export failure keeps component attribution",
			title: "Collector Export Failures",
			// 分出口归因：失败证据必须能区分 otlp/signoz 与 otlphttp/langfuse
			// 两个 exporter，否则备选 profile 里 AI 与 infra 的投递故障会互相掩盖。
			fragments: []string{"otelcol_exporter_send_failed", "otlp/signoz", "otlphttp/langfuse"},
		},
		{
			name:      "token and cost stay on the same low-cardinality facts",
			title:     "LLM Tokens and Cost",
			fragments: []string{"longtermism_llm_tokens_token_total", "longtermism_llm_cost_total"},
		},
		{
			name:      "evaluation results keep the correlation question",
			title:     "Evaluation Results",
			fragments: []string{"longtermism_eval_result_total"},
		},
		{
			name:      "score projection keeps its separate surface",
			title:     "Langfuse Score Projection",
			fragments: []string{"longtermism_score_projection_total"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel, ok := panels[tt.title]
			if !ok {
				t.Fatalf("dashboard panels %v, want a panel titled %q", signozPanelTitles(dashboard.Panels), tt.title)
			}
			query := strings.Join(signozPanelQueries(panel), "\n")
			if strings.TrimSpace(query) == "" {
				t.Fatalf("panel %q has no queries", tt.title)
			}
			for _, fragment := range tt.fragments {
				if !strings.Contains(query, fragment) {
					t.Fatalf("panel %q query = %q, want fragment %q", tt.title, query, fragment)
				}
			}
			// 高基数禁区：request/trace/run identity 只属于 span/log/report，
			// 不允许进入备选 profile 的指标标签（与主线同一条边界）。
			assertNoForbiddenMetricLabels(t, "signoz panel "+tt.title, []string{query})
		})
	}

	t.Run("keeps an explicit langfuse handoff note for AI and score evidence", func(t *testing.T) {
		// SigNoz 不承接 AI 平面：dashboard 必须显式告诉排障者 AI trace 与
		// score 投影的证据在 Langfuse，避免"看不到 AI 数据"被误判为 profile 损坏。
		for _, panel := range dashboard.Panels {
			if strings.Contains(strings.ToLower(panel.Title+" "+panel.Description), "langfuse") {
				return
			}
		}
		t.Fatalf("dashboard has no panel title/description mentioning langfuse; panels = %v", signozPanelTitles(dashboard.Panels))
	})
}

// TestSignozCompatibilityChecklistContract 固定备选 profile 的独立兼容性清单。
// T146 逐项要求 SigNoz 三信号与 Langfuse trace/score 的查询证据，
// 并明确该清单的优先级低于 Grafana 主线——备选方案不能被误读为同等维护承诺。
func TestSignozCompatibilityChecklistContract(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate checklist contract source")
	}
	body, err := os.ReadFile(filepath.Clean(filepath.Join(filepath.Dir(sourcePath), signozChecklistRelativePath)))
	if err != nil {
		t.Fatalf("read signoz compatibility checklist: %v", err)
	}
	checklist := string(body)
	lowered := strings.ToLower(checklist)

	for _, required := range []struct {
		fragment string
		reason   string
	}{
		{"signoz", "the checklist must name the alternate backend"},
		{"logs", "log-signal query evidence is a per-item requirement"},
		{"metrics", "metric-signal query evidence is a per-item requirement"},
		{"traces", "trace-signal query evidence is a per-item requirement"},
		{"langfuse", "AI trace/score evidence must stay in the Langfuse plane"},
		{"score", "score projection evidence must be checked explicitly"},
		{"查询", "acceptance items must demand real query evidence, not container health"},
		{"grafana", "the checklist must state its priority relative to the mainline"},
		{"备选", "the profile must be identified as the alternate, not a co-equal mainline"},
	} {
		if !strings.Contains(lowered, strings.ToLower(required.fragment)) {
			t.Fatalf("checklist missing %q: %s", required.fragment, required.reason)
		}
	}

	// 清单必须逐项可勾选：没有 checkbox 的"清单"无法在验收中留下证据状态。
	openItems := strings.Count(checklist, "- [ ]")
	if openItems < 5 {
		t.Fatalf("checklist open items = %d, want at least 5 per-signal/per-plane acceptance items", openItems)
	}
}

func loadSignozDashboard(t *testing.T) signozDashboardDocument {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate signoz dashboard contract source")
	}
	body, err := os.ReadFile(filepath.Clean(filepath.Join(filepath.Dir(sourcePath), signozDashboardRelativePath)))
	if err != nil {
		t.Fatalf("read signoz dashboard: %v", err)
	}
	var document signozDashboardDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("signoz dashboard is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(body, &document.Raw); err != nil {
		t.Fatalf("signoz dashboard is not a JSON object: %v", err)
	}
	if len(document.Panels) == 0 {
		t.Fatal("signoz dashboard has no panels array")
	}
	return document
}

func signozPanelsByTitle(t *testing.T, panels []signozDashboard) map[string]signozDashboard {
	t.Helper()
	byTitle := make(map[string]signozDashboard, len(panels))
	for _, panel := range panels {
		if strings.TrimSpace(panel.Title) == "" {
			t.Fatalf("dashboard panel %#v has no title", panel)
		}
		if _, duplicate := byTitle[panel.Title]; duplicate {
			t.Fatalf("dashboard panel title %q is not unique", panel.Title)
		}
		byTitle[panel.Title] = panel
	}
	return byTitle
}

func signozPanelQueries(panel signozDashboard) []string {
	queries := make([]string, 0, len(panel.Queries))
	for _, query := range panel.Queries {
		queries = append(queries, query.Query)
	}
	return queries
}

func signozPanelTitles(panels []signozDashboard) []string {
	titles := make([]string, 0, len(panels))
	for _, panel := range panels {
		titles = append(titles, panel.Title)
	}
	return titles
}
