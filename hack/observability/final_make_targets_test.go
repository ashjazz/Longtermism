package observability

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestObsReleaseGateRunsSequentiallyAndFailsFast 固定发布门禁的真实执行顺序。
// 使用伪 recursive make，既能证明失败后不再进入付费/破坏性阶段，也不会在测试机
// 误启动 Docker、调用模型或执行 resilience 故障注入。
func TestObsReleaseGateRunsSequentiallyAndFailsFast(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	makefile := mustReadRunbookFile(t, filepath.Join(repoRoot, "Makefile"))
	assertRecipeOnlyAggregate(t, makefile, "obs-release-gate")
	assertPhonyTargets(t, makefile, "obs-status", "obs-release-gate", "obs-signoz-compat-gate")

	wantOrder := []string{
		"verify",
		"obs-coverage",
		"obs-config-check",
		"obs-grafana-e2e",
		"obs-resilience-e2e",
	}
	for failedIndex := -1; failedIndex < len(wantOrder); failedIndex++ {
		name := "success"
		failedTarget := ""
		wantLog := wantOrder
		if failedIndex >= 0 {
			name = "fails_at_" + wantOrder[failedIndex]
			failedTarget = wantOrder[failedIndex]
			wantLog = wantOrder[:failedIndex+1]
		}
		t.Run(name, func(t *testing.T) {
			result := runAggregateMakeTarget(t, repoRoot, "obs-release-gate", failedTarget)
			if failedTarget == "" && result.err != nil {
				t.Fatalf("release gate failed: %v\n%s", result.err, result.output)
			}
			if failedTarget != "" && result.err == nil {
				t.Fatalf("release gate ignored failure from %s", failedTarget)
			}
			assertStringSliceEqual(t, result.calls, wantLog)
		})
	}
}

// TestObsSignozCompatibilityGateIsIndependent 保证备选平台只在显式调用时运行，
// 不借用 Grafana 主线或 resilience，也不会被默认 PR/verify 路径悄悄触发。
func TestObsSignozCompatibilityGateIsIndependent(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	makefile := mustReadRunbookFile(t, filepath.Join(repoRoot, "Makefile"))
	assertRecipeOnlyAggregate(t, makefile, "obs-signoz-compat-gate")

	for _, tt := range []struct {
		name       string
		failed     string
		wantCalls  []string
		wantFailed bool
	}{
		{name: "success", wantCalls: []string{"obs-config-check", "obs-signoz-e2e"}},
		{name: "config failure stops before signoz", failed: "obs-config-check", wantCalls: []string{"obs-config-check"}, wantFailed: true},
		{name: "signoz failure propagates", failed: "obs-signoz-e2e", wantCalls: []string{"obs-config-check", "obs-signoz-e2e"}, wantFailed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := runAggregateMakeTarget(t, repoRoot, "obs-signoz-compat-gate", tt.failed)
			if (result.err != nil) != tt.wantFailed {
				t.Fatalf("compat gate error = %v, wantFailed=%t\n%s", result.err, tt.wantFailed, result.output)
			}
			assertStringSliceEqual(t, result.calls, tt.wantCalls)
		})
	}

	verify := makeTargetBlock(t, makefile, "verify")
	for _, forbidden := range []string{"obs-release-gate", "obs-signoz-compat-gate", "obs-grafana-e2e", "obs-signoz-e2e"} {
		if strings.Contains(verify, forbidden) {
			t.Errorf("verify must remain Level 0 but references %s", forbidden)
		}
	}
}

// TestObsStatusEmitsDiagnosticAllowlist 防止状态命令成为密钥、端点或容器元数据
// 的旁路输出。healthy 只是一条 diagnostic fact，不能伪装成 query/E2E 证据。
func TestObsStatusEmitsDiagnosticAllowlist(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	fakeDir := t.TempDir()
	dockerLog := filepath.Join(fakeDir, "docker.log")
	fakeDocker := filepath.Join(fakeDir, "docker")
	writeExecutable(t, fakeDocker, `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG"
case "$*" in
  *longtermism-signoz*) service=signoz; image='registry.example.invalid/private/signoz:v0.126.0' ;;
  *) service=grafana; image='registry.example.invalid/private/grafana:13.1.0' ;;
esac
printf '%s|%s|%s|%s|%s|%s\n' 'private-container-id' "$service" 'Up 9 seconds (healthy)' "$image" 'authorization=Bearer synthetic-secret-t167' '127.0.0.1:3000->3000/tcp'
`)

	for _, profile := range []string{"grafana", "signoz"} {
		t.Run(profile, func(t *testing.T) {
			if err := os.WriteFile(dockerLog, nil, 0o600); err != nil {
				t.Fatalf("reset docker log: %v", err)
			}
			cmd := exec.Command("make", "--no-print-directory", "OBS_PROFILE="+profile, "obs-status")
			cmd.Dir = repoRoot
			cmd.Env = append(os.Environ(),
				"PATH="+fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_DOCKER_LOG="+dockerLog,
				"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION=Bearer environment-secret-t167",
				"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL=https://user:password@example.invalid/private",
			)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("obs-status failed: %v\n%s", err, output)
			}
			text := string(output)
			for _, required := range []string{
				"profile=" + profile,
				"evidence=diagnostic_only",
				"query_evidence=not_run",
				"service=",
				"state=running",
				"health=healthy",
				"version=",
			} {
				if !strings.Contains(text, required) {
					t.Errorf("status output missing %q: %s", required, text)
				}
			}
			for _, forbidden := range []string{
				"synthetic-secret-t167",
				"environment-secret-t167",
				"user:password",
				"registry.example.invalid",
				"private-container-id",
				"127.0.0.1",
				"passed",
				"supported",
				"ready",
			} {
				if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
					t.Errorf("status output exposed forbidden value %q: %s", forbidden, text)
				}
			}

			invocation := mustReadRunbookFile(t, dockerLog)
			if !strings.Contains(invocation, "ps") || strings.Contains(invocation, "compose") || strings.Contains(invocation, "inspect") {
				t.Errorf("status must use a narrow docker ps query, got %q", invocation)
			}
		})
	}

	if err := os.WriteFile(dockerLog, nil, 0o600); err != nil {
		t.Fatalf("reset docker log: %v", err)
	}
	cmd := exec.Command("make", "--no-print-directory", "OBS_PROFILE=malicious;profile", "obs-status")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "PATH="+fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_DOCKER_LOG="+dockerLog)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("obs-status accepted an unknown profile")
	}
	if strings.Contains(string(output), "malicious") {
		t.Errorf("unknown-profile error echoed rejected input: %s", output)
	}
	if invocation := mustReadRunbookFile(t, dockerLog); invocation != "" {
		t.Errorf("unknown profile reached Docker: %q", invocation)
	}
}

type aggregateMakeResult struct {
	calls  []string
	output string
	err    error
}

func runAggregateMakeTarget(t *testing.T, repoRoot, target, failedTarget string) aggregateMakeResult {
	t.Helper()
	tempDir := t.TempDir()
	callLog := filepath.Join(tempDir, "calls.log")
	fakeMake := filepath.Join(tempDir, "recursive-make")
	writeExecutable(t, fakeMake, `#!/bin/sh
target=''
for argument in "$@"; do
  case "$argument" in
    --*) ;;
    *=*) ;;
    *) target="$argument" ;;
  esac
done
printf '%s\n' "$target" >> "$FAKE_MAKE_LOG"
if [ -n "$FAKE_FAIL_TARGET" ] && [ "$target" = "$FAKE_FAIL_TARGET" ]; then
  exit 17
fi
`)

	cmd := exec.Command("make", "-j4", "--no-print-directory", "MAKE="+fakeMake, target)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "FAKE_MAKE_LOG="+callLog, "FAKE_FAIL_TARGET="+failedTarget)
	output, err := cmd.CombinedOutput()
	calls := []string{}
	if content, readErr := os.ReadFile(callLog); readErr == nil {
		calls = strings.Fields(string(content))
	} else if !os.IsNotExist(readErr) {
		t.Fatalf("read recursive make log: %v", readErr)
	}
	return aggregateMakeResult{calls: calls, output: string(output), err: err}
}

func assertRecipeOnlyAggregate(t *testing.T, makefile, target string) {
	t.Helper()
	line := makeTargetLine(t, makefile, target)
	if strings.TrimSpace(strings.TrimPrefix(line, target+":")) != "" {
		t.Fatalf("%s must be a recipe-only aggregate, got %q", target, line)
	}
	block := makeTargetBlock(t, makefile, target)
	if !strings.Contains(block, "$(MAKE)") {
		t.Fatalf("%s must invoke sequential recursive make recipes", target)
	}
}

func assertPhonyTargets(t *testing.T, makefile string, targets ...string) {
	t.Helper()
	phony := makeTargetLine(t, makefile, ".PHONY")
	for _, target := range targets {
		if !containsField(strings.TrimPrefix(phony, ".PHONY:"), target) {
			t.Errorf("%s is not declared .PHONY", target)
		}
	}
}

func makeTargetLine(t *testing.T, makefile, target string) string {
	t.Helper()
	prefix := target + ":"
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("Makefile missing target %s", target)
	return ""
}

func makeTargetBlock(t *testing.T, makefile, target string) string {
	t.Helper()
	lines := strings.Split(makefile, "\n")
	start := -1
	for index, line := range lines {
		if strings.HasPrefix(line, target+":") {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatalf("Makefile missing target %s", target)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if line != "" && line[0] != '\t' && !strings.HasPrefix(line, "#") && strings.Contains(line, ":") {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func containsField(fields, want string) bool {
	for _, field := range strings.Fields(fields) {
		if field == want {
			return true
		}
	}
	return false
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %v; want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("calls = %v; want %v", got, want)
		}
	}
}
