package observability

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestQuickstartRunbookCoversFinalOperationalSurface 固定 T164 的文档边界：
// runbook 必须覆盖完整操作面，并明确区分离线验证与尚待真实环境证明的结论。
// 文档中出现命令从来不等于已经取得后端证据。
func TestQuickstartRunbookCoversFinalOperationalSurface(t *testing.T) {
	quickstart := readQuickstartRunbook(t)
	required := []string{
		"## 2. 证据状态与通过规则",
		"## 3. 环境准备",
		"## 4. Level 0",
		"## 5. Level 1",
		"## 6. Level 2",
		"## 7. Level 3",
		"## 8. Level 4",
		"## 9. 故障注入与恢复",
		"## 10. 诊断",
		"## 11. Cleanup 与安全 reset",
		"## 12. 凭据轮换",
		"## 13. 门禁频率",
		"文档出现命令不代表命令已在真实环境执行",
		"Level 2、Level 3 与 Level 4 的真实 E2E 状态仍为待验收",
		"schema-valid report",
		"real-backend acceptance checklist",
	}
	for _, fragment := range required {
		if !strings.Contains(quickstart, fragment) {
			t.Errorf("quickstart missing final runbook contract %q", fragment)
		}
	}

	unsupportedClaims := []string{"Level 2 已通过", "Level 3 已通过", "Level 4 已通过", "SigNoz 已验证", "resilience 已通过"}
	for _, claim := range unsupportedClaims {
		if strings.Contains(quickstart, claim) {
			t.Errorf("quickstart contains unsupported live-evidence claim %q", claim)
		}
	}
}

// TestQuickstartRunbookDefinesGateFrequencyDependenciesAndCost 防止高成本 live
// gate 被塞回每个 PR，也防止发布流程漏掉显式的外部依赖与费用说明。
func TestQuickstartRunbookDefinesGateFrequencyDependenciesAndCost(t *testing.T) {
	quickstart := readQuickstartRunbook(t)
	tests := []struct {
		name      string
		rowPrefix string
		required  []string
	}{
		{
			name:      "pull request",
			rowPrefix: "| PR |",
			required:  []string{"make verify", "make obs-contract", "make obs-smoke-offline", "make obs-platform-smoke", "make obs-config-check", "无 Docker", "零外部 API 费用"},
		},
		{
			name:      "observability configuration change",
			rowPrefix: "| 观测配置变更 |",
			required:  []string{"make obs-config-check", "make obs-grafana-e2e", "Docker", "真实模型 API 可能计费"},
		},
		{
			name:      "stage milestone",
			rowPrefix: "| 阶段里程碑 |",
			required:  []string{"make obs-grafana-e2e", "make obs-resilience-e2e", "Docker", "真实模型 API 可能计费"},
		},
		{
			name:      "release candidate",
			rowPrefix: "| Release candidate |",
			required:  []string{"make verify", "make obs-coverage", "make obs-config-check", "make obs-grafana-e2e", "make obs-resilience-e2e", "make obs-signoz-e2e", "真实模型 API 可能计费"},
		},
		{
			name:      "scheduled canary",
			rowPrefix: "| Scheduled canary |",
			required:  []string{"make obs-grafana-e2e", "make obs-signoz-e2e", "非合并门禁", "真实模型 API 可能计费"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := markdownTableRow(quickstart, tt.rowPrefix)
			if row == "" {
				t.Fatalf("quickstart missing frequency row %q", tt.rowPrefix)
			}
			for _, fragment := range tt.required {
				if !strings.Contains(row, fragment) {
					t.Errorf("frequency row %q missing %q", tt.rowPrefix, fragment)
				}
			}
		})
	}
}

// TestQuickstartRunbookClosesCredentialAndCleanupOwnership 固定最容易误操作的
// 所有权边界：smoke 只能清理自己创建的临时资产，调用方长期凭据永不由 smoke 撤销。
func TestQuickstartRunbookClosesCredentialAndCleanupOwnership(t *testing.T) {
	quickstart := readQuickstartRunbook(t)
	required := []string{
		"smoke 自建短期凭据",
		"撤销或删除",
		"外部注入的长期凭据",
		"不得由 smoke 撤销",
		"临时文件和数据",
		"cleanup.temporary_credentials",
		"cleanup.temporary_data",
		"cleanup.residual_resources",
		"`cleanup.status=not_required` 仅在两项临时资产均为 `not_created` 且 residual resources 为空时合法",
		"实际执行过 cleanup 时必须为 `completed`",
		"`cleanup.status=failed`、任一临时资产为 `failed` 或仍有 residual resources 时，本次运行必须判定失败",
		"先创建并验证新凭据",
		"被 Git 忽略的 `.env.local` 或 secret manager",
		"确认新凭据通过对应 smoke 后，才由凭据所有者撤销旧值",
		"LANGFUSE_ENCRYPTION_KEY",
		"不得在已有 volume 上无迁移计划直接轮换",
		"`reset.sh` 当前只适合做只读 inventory preview",
		"尚未声明脚本要求的 `longtermism.observability=true` label",
		"direct Langfuse curl target 不经过正式 query adapter 的 endpoint 安全边界",
	}
	for _, fragment := range required {
		if !strings.Contains(quickstart, fragment) {
			t.Errorf("quickstart missing credential/cleanup contract %q", fragment)
		}
	}
}

// TestQuickstartRunbookDocumentsOnlyExistingRepositoryCommands 解析全文的 Make
// 命令和 bash 代码块中的脚本入口，逐项与 Makefile/文件系统核对。外部工具是否安装
// 属于运行前置，不在 CI 中用 exec.LookPath 猜测；这里只阻止文档引用尚未实现的入口。
func TestQuickstartRunbookDocumentsOnlyExistingRepositoryCommands(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	quickstart := readQuickstartRunbook(t)
	makefile := mustReadRunbookFile(t, filepath.Join(repoRoot, "Makefile"))
	targets := makeTargets(makefile)

	commands := documentedRepositoryCommands(quickstart)
	for _, target := range commands.makeTargets {
		if target == "obs-reset" {
			t.Error("quickstart recommends obs-reset before destructive path and volume-label hardening exists")
		}
		if target == "obs-direct-langfuse-smoke" {
			t.Error("quickstart recommends a direct credential curl that bypasses the production query adapter endpoint boundary")
		}
		if _, exists := targets[target]; !exists {
			t.Errorf("quickstart references missing Make target %q", target)
		}
	}
	for _, script := range commands.bashScripts {
		if _, err := os.Stat(filepath.Join(repoRoot, strings.TrimPrefix(script, "./"))); err != nil {
			t.Errorf("quickstart references missing repository script %q", script)
		}
	}
	for _, entrypoint := range commands.goEntrypoints {
		if _, err := os.Stat(filepath.Join(repoRoot, strings.TrimPrefix(entrypoint, "./"))); err != nil {
			t.Errorf("quickstart references missing Go command %q", entrypoint)
		}
	}

	if strings.Contains(quickstart, "make obs-stack-health OBS_PROFILE=signoz") {
		t.Error("quickstart documents an impossible SigNoz health command; the target accepts only grafana")
	}
	if strings.Contains(quickstart, "--confirm") || strings.Contains(quickstart, "--run-root") {
		t.Error("quickstart recommends destructive reset arguments before reset path and volume-label hardening exists")
	}

	for _, profile := range unsupportedHealthProfiles(quickstart) {
		t.Errorf("quickstart documents unsupported obs-stack-health profile %q", profile)
	}
}

// TestDocumentedRepositoryCommandsCoversMarkdownForms 用合成文档固定解析器自身的边界，
// 避免反引号或 fenced block 让不存在的仓库入口绕过 quickstart 契约。
func TestDocumentedRepositoryCommandsCoversMarkdownForms(t *testing.T) {
	document := "inline `make inline-target` and `bash ./hack/inline.sh` and `go run .`\n" +
		"```bash\nmake fenced-target\nbash hack/fenced.sh\ngo run ./cmd/example\n```\n"

	commands := documentedRepositoryCommands(document)
	assertStringSet(t, commands.makeTargets, "inline-target", "fenced-target")
	assertStringSet(t, commands.bashScripts, "./hack/inline.sh", "hack/fenced.sh")
	assertStringSet(t, commands.goEntrypoints, ".", "./cmd/example")
	assertStringSet(t, unsupportedHealthProfiles(
		"`OBS_PROFILE=signoz make\tobs-stack-health`\n`make  obs-stack-health OBS_PROFILE=bogus`",
	), "signoz", "bogus")
}

func observabilityRepoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve quickstart test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readQuickstartRunbook(t *testing.T) string {
	t.Helper()
	return mustReadRunbookFile(t, filepath.Join(observabilityRepoRoot(t), "specs", "003-real-observability-backends", "quickstart.md"))
}

func mustReadRunbookFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return string(content)
}

func markdownTableRow(document, prefix string) string {
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func makeTargets(makefile string) map[string]struct{} {
	targetPattern := regexp.MustCompile(`(?m)^([A-Za-z0-9_.%-]+):`)
	targets := make(map[string]struct{})
	for _, match := range targetPattern.FindAllStringSubmatch(makefile, -1) {
		targets[match[1]] = struct{}{}
	}
	return targets
}

type repositoryCommands struct {
	makeTargets   []string
	bashScripts   []string
	goEntrypoints []string
}

func documentedRepositoryCommands(document string) repositoryCommands {
	makeCommand := regexp.MustCompile(`\bmake\s+([A-Za-z0-9_.-]+)`)
	bashScript := regexp.MustCompile(`\bbash\s+((?:\./|hack/)[A-Za-z0-9_./-]+\.sh)`)
	goCommand := regexp.MustCompile(`\bgo\s+run\s+(\./[A-Za-z0-9_./-]+|\.)`)

	return repositoryCommands{
		makeTargets:   regexpCaptureGroup(document, makeCommand),
		bashScripts:   regexpCaptureGroup(document, bashScript),
		goEntrypoints: regexpCaptureGroup(document, goCommand),
	}
}

func regexpCaptureGroup(document string, pattern *regexp.Regexp) []string {
	matches := pattern.FindAllStringSubmatch(document, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		values = append(values, match[1])
	}
	return values
}

func unsupportedHealthProfiles(document string) []string {
	profileAssignment := regexp.MustCompile(`\bOBS_PROFILE=([A-Za-z0-9_.-]+)`)
	healthCommand := regexp.MustCompile(`\bmake\s+obs-stack-health\b`)
	profiles := make([]string, 0)
	for _, line := range strings.Split(document, "\n") {
		if !healthCommand.MatchString(line) {
			continue
		}
		for _, match := range profileAssignment.FindAllStringSubmatch(line, -1) {
			if match[1] != "grafana" {
				profiles = append(profiles, match[1])
			}
		}
	}
	return profiles
}

func assertStringSet(t *testing.T, got []string, want ...string) {
	t.Helper()
	gotSet := make(map[string]struct{}, len(got))
	for _, value := range got {
		gotSet[value] = struct{}{}
	}
	for _, value := range want {
		if _, exists := gotSet[value]; !exists {
			t.Errorf("command parser missed %q in %v", value, got)
		}
	}
}
