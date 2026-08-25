package observability

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectStatusDocsDefineEvidenceVocabulary 固定 T168 的状态语义，避免把“仓库里
// 已有资产”自动提升为“真实环境已验证”。四种状态都必须给出明确证据边界。
func TestProjectStatusDocsDefineEvidenceVocabulary(t *testing.T) {
	for name, document := range readProjectStatusDocs(t) {
		t.Run(name, func(t *testing.T) {
			required := []string{
				"003 实施与验证状态",
				"`generated`",
				"`planned`",
				"`in-progress`",
				"`verified`",
				"资产已经进入仓库",
				"尚未取得所需证据",
				"只有与声明范围匹配的可复验证据",
			}
			for _, fragment := range required {
				if !strings.Contains(document, fragment) {
					t.Errorf("project status document missing evidence vocabulary %q", fragment)
				}
			}
		})
	}
}

// TestProjectStatusDocsPreserve002LearningAssetHistory 防止 003 的真实后端工作
// 倒写 002 的学习资产归属。01-07 的 drafted 状态也不能被 003 的代码进度改成 validated。
func TestProjectStatusDocsPreserve002LearningAssetHistory(t *testing.T) {
	for name, document := range readProjectStatusDocs(t) {
		t.Run(name, func(t *testing.T) {
			required := []string{
				"docs/observability/01-07",
				"specs/002-dual-plane-observability",
				"T072-T078",
				"drafted",
				"不改写为 003 产物",
			}
			for _, fragment := range required {
				if !strings.Contains(document, fragment) {
					t.Errorf("project status document missing 002 history boundary %q", fragment)
				}
			}
		})
	}
}

// TestProjectStatusDocsExposeOnlyRealVerificationEntrypoints 保护操作者入口：status
// 只是诊断，主线和 SigNoz live gate 都是显式 opt-in，不能混入默认离线 PR 门禁。
func TestProjectStatusDocsExposeOnlyRealVerificationEntrypoints(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	documents := readProjectStatusDocs(t)
	makefile := mustReadRunbookFile(t, filepath.Join(repoRoot, "Makefile"))
	targets := makeTargets(makefile)

	requiredTargets := []string{
		"verify",
		"obs-config-check",
		"obs-status",
		"obs-release-gate",
		"obs-signoz-compat-gate",
	}
	for _, target := range requiredTargets {
		if _, ok := targets[target]; !ok {
			t.Fatalf("project status documents require missing Make target %q", target)
		}
	}

	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			required := []string{
				"make verify",
				"make obs-config-check",
				"make obs-status",
				"make obs-release-gate",
				"make obs-signoz-compat-gate",
				"diagnostic_only",
				"显式 opt-in",
				"真实模型 API 可能计费",
				"不进入默认 PR 门禁",
			}
			for _, fragment := range required {
				if !strings.Contains(document, fragment) {
					t.Errorf("project status document missing verification boundary %q", fragment)
				}
			}
		})
	}
}

// TestProjectStatusDocsDoNotClaimUnverifiedLiveSupport 固定当前证据事实：003 仍在
// 收口，历史 schema-v2 infra 报告不能替代当前 v3 release/SigNoz/resilience 证据。
func TestProjectStatusDocsDoNotClaimUnverifiedLiveSupport(t *testing.T) {
	for name, document := range readProjectStatusDocs(t) {
		t.Run(name, func(t *testing.T) {
			required := []string{
				"003 整体",
				"`in-progress`",
				"schema v2",
				"不能关闭当前 schema v3 live acceptance",
				"Grafana release/resilience",
				"SigNoz",
				"尚无当前 schema v3 passed report",
				"T169",
			}
			for _, fragment := range required {
				if !strings.Contains(document, fragment) {
					t.Errorf("project status document missing current evidence fact %q", fragment)
				}
			}

			unsupportedClaims := []string{
				"003 已完成",
				"真实后端已完成",
				"Grafana release 已通过",
				"resilience 已通过",
				"SigNoz 已验证",
				"SigNoz 已完成",
			}
			for _, claim := range unsupportedClaims {
				if strings.Contains(document, claim) {
					t.Errorf("project status document contains unsupported live claim %q", claim)
				}
			}
		})
	}
}

func readProjectStatusDocs(t *testing.T) map[string]string {
	t.Helper()
	repoRoot := observabilityRepoRoot(t)
	return map[string]string{
		"README":  mustReadRunbookFile(t, filepath.Join(repoRoot, "README.md")),
		"ROADMAP": mustReadRunbookFile(t, filepath.Join(repoRoot, "docs", "ROADMAP.md")),
	}
}
