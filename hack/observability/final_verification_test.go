package observability

import (
	"bufio"
	"path/filepath"
	"strings"
	"testing"
)

// TestFinalVerificationRecordsEveryRequiredGate 固定 T169 的审查账本：失败或未运行
// 也是事实，必须连同低敏原因记录，不能通过删除行或模糊写成“基本通过”来收口。
func TestFinalVerificationRecordsEveryRequiredGate(t *testing.T) {
	document := readFinalVerification(t)
	required := []string{
		"# 003 Final Verification",
		"## 审查结论",
		"## 命令结果",
		"## 安全审查",
		"## Live evidence",
		"## 剩余风险",
		"## 完成判定",
		"`PASS`",
		"`FAIL`",
		"`NOT_RUN`",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("final verification missing section or result vocabulary %q", fragment)
		}
	}

	commands := []string{
		"git diff --check",
		"make verify",
		"go test -race ./...",
		"make obs-coverage",
		"tracked-worktree secret scan",
		"make obs-config-check",
		"make obs-release-gate",
		"make obs-signoz-compat-gate",
	}
	for _, command := range commands {
		row := finalVerificationRow(document, command)
		if row == "" {
			t.Errorf("final verification missing command row %q", command)
			continue
		}
		if !containsOne(row, "`PASS`", "`FAIL`", "`NOT_RUN`") {
			t.Errorf("final verification row %q has no closed result", command)
		}
		if strings.Count(row, "|") < 4 {
			t.Errorf("final verification row %q lacks low-sensitive evidence/reason column", command)
		}
	}
}

// TestFinalVerificationKeepsLiveClaimsFailClosed 防止本地资产、历史报告或容器状态
// 被提升成当前 schema-v3 的 release/SigNoz 证据。
func TestFinalVerificationKeepsLiveClaimsFailClosed(t *testing.T) {
	document := readFinalVerification(t)
	required := []string{
		"schema v3",
		"historical schema v2",
		"diagnostic_only",
		"真实模型 API 可能计费",
		"当前 live checklist 未闭合",
		"没有新的受审 passed report",
		"不能声明 Grafana release/resilience 或 SigNoz live support",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("final verification missing fail-closed live boundary %q", fragment)
		}
	}
}

// TestFinalVerificationSecurityReviewIsScopedAndLowSensitive 固定安全审查范围与输出
// 边界：只记录规则、计数和结论，不复制命中的 credential、endpoint 或本地绝对路径。
func TestFinalVerificationSecurityReviewIsScopedAndLowSensitive(t *testing.T) {
	document := readFinalVerification(t)
	required := []string{
		"current tracked worktree",
		"git history scanner",
		"dependency vulnerability scanner",
		"`.env.local`",
		"0600",
		"loopback-only",
		"no-proxy/no-redirect",
		"constant-time",
		"路径 containment",
		"stable low-sensitive error",
		"synthetic fixture",
		"T035",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("final verification missing security boundary %q", fragment)
		}
	}

	for _, forbidden := range []string{
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION=",
		"LANGFUSE_SECRET_KEY=",
		"OPENAI_API_KEY=",
		"Authorization: Bearer",
		"/Users/",
		"http://127.0.0.1:",
	} {
		if strings.Contains(document, forbidden) {
			t.Errorf("final verification leaks forbidden local or credential detail %q", forbidden)
		}
	}
}

// TestFinalVerificationDoesNotHideRemainingTaskOrGateFailure 让任务勾选只代表
// “审查已执行并记录”，不代表 feature 或 release 已通过。
func TestFinalVerificationDoesNotHideRemainingTaskOrGateFailure(t *testing.T) {
	document := readFinalVerification(t)
	required := []string{
		"T169 完成只表示审查已执行并记录",
		"003 整体仍为 `in-progress`",
		"T035",
		"release decision",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("final verification missing completion boundary %q", fragment)
		}
	}
}

func readFinalVerification(t *testing.T) string {
	t.Helper()
	path := filepath.Join(
		observabilityRepoRoot(t),
		"specs",
		"003-real-observability-backends",
		"checklists",
		"final-verification.md",
	)
	return mustReadRunbookFile(t, path)
}

func finalVerificationRow(document, command string) string {
	scanner := bufio.NewScanner(strings.NewReader(document))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "|") && strings.Contains(line, command) {
			return line
		}
	}
	return ""
}

func containsOne(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
