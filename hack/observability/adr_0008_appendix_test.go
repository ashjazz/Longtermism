package observability

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestADR0008AcceptedDecisionBaselineIsAppendOnly 保护 2026-07-10 已接受的
// Context/Decision/Alternatives/Consequences/Revisit Conditions。实现附录可以追加事实，
// 但改变这段基线必须由新的 amends/supersedes ADR 显式驱动。
func TestADR0008AcceptedDecisionBaselineIsAppendOnly(t *testing.T) {
	adr := readADR0008(t)
	baseline, _, found := strings.Cut(adr, "\n## Implementation Appendix（T165）")
	if !found {
		t.Fatal("ADR-0008 implementation appendix boundary is missing")
	}
	// 这里只统一不同平台的换行符；Markdown 的空白、缩进与换行本身属于文档结构，
	// 不能像普通内容断言那样压平，否则可能放过对 accepted 决策的实质改写。
	canonicalBaseline := strings.ReplaceAll(baseline, "\r\n", "\n")
	digest := sha256.Sum256([]byte(canonicalBaseline))
	const acceptedBaselineSHA256 = "bf3930ea5cc40b177a476bd04e5abe7da1ceb938a7df50d253db34457a19f9d9"
	if got := fmt.Sprintf("%x", digest); got != acceptedBaselineSHA256 {
		t.Errorf("ADR-0008 accepted baseline changed without a new amends/supersedes ADR: sha256=%s", got)
	}
}

// TestADR0008ImplementationAppendixDefinesVerifiedResultsAndRevisitThresholds
// 固定 T165 的治理边界：implementation note 只能记录已经由仓库证据证明的事实，
// 未来迁移阈值必须明确标成 revisit condition，不能倒写成已完成的生产能力。
func TestADR0008ImplementationAppendixDefinesVerifiedResultsAndRevisitThresholds(t *testing.T) {
	appendix := adr0008ImplementationAppendix(t, readADR0008(t))
	normalized := normalizedText(appendix)
	required := []string{
		"### 实施结果（仅已验证事实）",
		"### 偏差与未验证边界",
		"### Score worker 当前可靠性边界",
		"### Score worker 演进阈值",
		"### ADR 治理边界",
		"本附录不改写上述 accepted 决策",
		"revisit condition 不是已验证结果",
		"架构变化必须新增 ADR",
		"不得静默改写",
		"replica_count > 1",
		"跨主机持久化",
		"生产 SLO",
		"failed_shutdown_timeout",
		"零 shutdown loss window",
		"oldest_queue_age",
		"enqueue_rate",
		"sustainable_send_rate",
		"retry_wait",
		"120s",
		"outbox",
		"外部 worker",
		"queue depth 与 coarse projection telemetry",
		"queue age、稳定吞吐容量和 retry backlog 尚未被当前指标直接证明",
		"Level 2、Level 3 与 Level 4 live E2E 仍待验收",
		"默认 queue capacity 为 64",
		"代码硬上限为 4096",
		"默认 score request timeout 为 10s",
		"总预算上限为 60s",
		"文件上限 8 MiB",
		"1024 条 terminal history",
		"private regular file、进程内 gate、`flock`",
		"临时文件同步后 rename 与目录 fsync",
		"evidence JSONL 与 projection JSON 是两个独立存储",
		"没有 claim、lease、visibility timeout 或 partition ownership",
		"不会由 `LoadPending` 自动重投",
		"只有 lifecycle 的 terminal `Update` 成功时",
		"保留并在重启时恢复先前的 `queued` 快照",
		"真实 Langfuse 是否合并或更新仍待 live E2E 证明",
		"不等于 exactly-once delivery",
		"只有 terminal `Update` 成功时",
		"production lifecycle 尚未把该 callback 接入指标",
		"已经成功 append 并完成 fsync 的本地 evidence",
	}
	for _, fragment := range required {
		if !strings.Contains(normalized, fragment) {
			t.Errorf("ADR-0008 implementation appendix missing %q", fragment)
		}
	}

	unsupportedClaims := []string{
		"多进程安全已验证",
		"score 零丢失已验证",
		"queue age 已监控",
		"真实后端已通过",
		"真实 Langfuse 已合并或更新重放 score",
		"shutdown terminal 已持久化",
	}
	for _, claim := range unsupportedClaims {
		if strings.Contains(normalized, claim) {
			t.Errorf("ADR-0008 contains unsupported implementation claim %q", claim)
		}
	}

	assertADR0008CurrentImplementationFacts(t, appendix)
}

// TestADR0008ImplementationAppendixCitesExistingScoreWorkerEvidence 防止 ADR 用
// 模糊的“测试已覆盖”代替可复验事实，也防止测试重命名后文档继续悬空引用。
func TestADR0008ImplementationAppendixCitesExistingScoreWorkerEvidence(t *testing.T) {
	appendix := adr0008ImplementationAppendix(t, readADR0008(t))
	references := []adrTestReference{
		{"internal/eval/score_projection_store_test.go", "TestScoreProjectionStorePersistsOneCurrentSnapshotPerRun"},
		{"internal/cmd/langfuse_score_lifecycle_test.go", "TestLangfuseScoreLifecyclePersistsInitialSnapshotBeforeAdmission"},
		{"internal/cmd/langfuse_score_lifecycle_test.go", "TestBuildLangfuseScoreLifecycleRecoversPendingBeforeStart"},
		{"internal/observability/langfuse/worker_test.go", "TestScoreWorkerReliablyRecordsRetrySequence"},
		{"internal/observability/langfuse/worker_test.go", "TestScoreWorkerShutdownMarksUndrainedProjectionsWithoutDeletingEvidence"},
		{"internal/observability/smoke/score_failure_runner_test.go", "TestRunScoreWorkerFailureSmokeQueueFullDropsProjectionSafely"},
	}

	repoRoot := observabilityRepoRoot(t)
	for _, reference := range references {
		source, err := os.ReadFile(filepath.Join(repoRoot, reference.path))
		if err != nil {
			t.Fatalf("read implementation evidence %s: %v", reference.path, err)
		}
		label := reference.path + "::" + reference.testName
		if !strings.Contains(appendix, label) {
			t.Errorf("ADR-0008 missing implementation evidence %q", label)
		}
		if !sourceDeclaresGoTest(source, reference.testName) {
			t.Errorf("ADR-0008 references missing executable Go test %q", label)
		}
	}
}

type adrTestReference struct {
	path     string
	testName string
}

func readADR0008(t *testing.T) string {
	t.Helper()
	path := filepath.Join(observabilityRepoRoot(t), "docs", "adr", "0008-real-observability-backends-and-minimal-http-loop.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ADR-0008: %v", err)
	}
	return string(content)
}

func adr0008ImplementationAppendix(t *testing.T, adr string) string {
	t.Helper()
	const heading = "## Implementation Appendix（T165）"
	start := strings.Index(adr, heading)
	if start < 0 {
		t.Fatalf("ADR-0008 missing %q", heading)
	}
	remainder := adr[start:]
	end := strings.Index(remainder, "\n## References（参考）")
	if end < 0 {
		t.Fatal("ADR-0008 implementation appendix is not bounded by References")
	}
	return remainder[:end]
}

func normalizedText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func assertADR0008CurrentImplementationFacts(t *testing.T, appendix string) {
	t.Helper()
	repoRoot := observabilityRepoRoot(t)
	sources := []struct {
		path      string
		fragments []string
	}{
		{"manifest/config/config.yaml", []string{"queue_capacity: 64", `request_timeout: "10s"`}},
		{"internal/cmd/langfuse_score_lifecycle.go", []string{"maxLangfuseScoreQueueCapacity = 4096", "maxLangfuseScoreTimeout = 60 * time.Second"}},
		{"internal/cmd/chat_runtime.go", []string{"shutdownCtx, cancel := context.WithTimeout(context.Background(), maxLangfuseScoreTimeout)"}},
		{"internal/eval/score_projection_store.go", []string{"maximumScoreProjectionStoreBytes = 8 << 20", "maximumTerminalScoreProjections = 1024"}},
	}
	for _, source := range sources {
		content, err := os.ReadFile(filepath.Join(repoRoot, source.path))
		if err != nil {
			t.Fatalf("read current implementation fact source %s: %v", source.path, err)
		}
		normalizedSource := normalizedText(string(content))
		for _, fragment := range source.fragments {
			if !strings.Contains(normalizedSource, fragment) {
				t.Errorf("implementation fact source %s no longer contains %q; recalibrate ADR appendix", source.path, fragment)
			}
		}
	}
	if !strings.Contains(appendix, "64") || !strings.Contains(appendix, "4096") ||
		!strings.Contains(appendix, "10s") || !strings.Contains(appendix, "60s") ||
		!strings.Contains(appendix, "8 MiB") || !strings.Contains(appendix, "1024") {
		t.Error("ADR-0008 appendix does not carry the current bounded score worker/store facts")
	}
}

func sourceDeclaresGoTest(source []byte, testName string) bool {
	file, err := parser.ParseFile(token.NewFileSet(), "evidence_test.go", source, 0)
	if err != nil {
		return false
	}
	testingImports := make(map[string]struct{})
	for _, importSpec := range file.Imports {
		importPath, unquoteErr := strconv.Unquote(importSpec.Path.Value)
		if unquoteErr != nil || importPath != "testing" {
			continue
		}
		localName := "testing"
		if importSpec.Name != nil {
			localName = importSpec.Name.Name
		}
		if localName != "_" && localName != "." {
			testingImports[localName] = struct{}{}
		}
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != testName || function.Type.Params == nil || len(function.Type.Params.List) != 1 ||
			(function.Type.Results != nil && function.Type.Results.NumFields() != 0) {
			continue
		}
		parameter := function.Type.Params.List[0]
		parameterCount := len(parameter.Names)
		if parameterCount == 0 {
			parameterCount = 1
		}
		if parameterCount != 1 {
			continue
		}
		pointer, ok := parameter.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		selector, ok := pointer.X.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "T" {
			continue
		}
		packageName, ok := selector.X.(*ast.Ident)
		if _, isStandardTesting := testingImports[packageName.Name]; ok && isStandardTesting {
			return true
		}
	}
	return false
}

// TestSourceDeclaresGoTestRejectsNonExecutableLookalikes 锁住证据引用解析器自身的边界：
// 注释、同名第三方包、多参数函数和有返回值的函数都不是 go test 会执行的测试。
func TestSourceDeclaresGoTestRejectsNonExecutableLookalikes(t *testing.T) {
	valid := []byte(`package evidence

import stdtesting "testing"

func TestEvidence(t *stdtesting.T) {}
`)
	if !sourceDeclaresGoTest(valid, "TestEvidence") {
		t.Fatal("aliased standard testing import should be accepted")
	}

	invalid := []string{
		`package evidence

import "testing"

// func TestEvidence(t *testing.T) {}
`,
		`package evidence

import testing "example.com/fake/testing"

func TestEvidence(t *testing.T) {}
`,
		`package evidence

import "testing"

func TestEvidence(first, second *testing.T) {}
`,
		`package evidence

import "testing"

func TestEvidence(t *testing.T) error { return nil }
`,
	}
	for index, source := range invalid {
		if sourceDeclaresGoTest([]byte(source), "TestEvidence") {
			t.Errorf("invalid Go test lookalike %d was accepted", index)
		}
	}
}
