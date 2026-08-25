package observability

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	assertDefaultAndLevelZeroDependencyGraph(t, makefile)
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
printf '%s\n' 'docker-stderr=synthetic-secret-t167' >&2
if [ "${FAKE_DOCKER_EXIT:-0}" != 0 ]; then exit "$FAKE_DOCKER_EXIT"; fi
if [ "${FAKE_DOCKER_EMPTY:-0}" = 1 ]; then exit 0; fi
case "$*" in
  *longtermism-signoz*) service=signoz; image='registry.example.invalid/private/signoz:v0.126.0' ;;
  *) service=grafana; image='registry.example.invalid/private/grafana:13.1.0' ;;
esac
printf '%s|%s|%s|%s|%s|%s\n' "$service" 'Up 9 seconds (healthy)' "$image" 'authorization=Bearer synthetic-secret-t167' '127.0.0.1:3000->3000/tcp' 'private-container-id'
printf '%s|%s|%s\n' 'sk-live-service-secret-t167' 'Up 1 second (healthy)' 'repo:sk-live-version-secret-t167'
`)

	for _, profile := range []string{"grafana", "signoz"} {
		t.Run(profile, func(t *testing.T) {
			if err := os.WriteFile(dockerLog, nil, 0o600); err != nil {
				t.Fatalf("reset docker log: %v", err)
			}
			makePath := mustLookPath(t, "make")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, makePath, "--no-print-directory", "obs-status")
			cmd.Dir = repoRoot
			cmd.Env = []string{
				"PATH=" + fakeDir + string(os.PathListSeparator) + systemToolPath(),
				"HOME=" + fakeDir,
				"TMPDIR=" + fakeDir,
				"OBS_PROFILE=" + profile,
				"FAKE_DOCKER_LOG=" + dockerLog,
				"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION=Bearer environment-secret-t167",
				"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL=https://user:password@example.invalid/private",
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("obs-status failed: %v\n%s%s", err, output, stderr.String())
			}
			text := string(output)
			allOutput := text + stderr.String()
			service := profile
			version := "13.1.0"
			if profile == "signoz" {
				version = "v0.126.0"
			}
			wantOutput := "profile=" + profile + " evidence=diagnostic_only query_evidence=not_run\n" +
				"service=" + service + " state=running health=healthy version=" + version + "\n" +
				"service=unknown state=running health=healthy version=unknown\n"
			if text != wantOutput {
				t.Errorf("status output = %q; want exact allowlist projection %q", text, wantOutput)
			}
			for _, forbidden := range []string{
				"synthetic-secret-t167",
				"environment-secret-t167",
				"user:password",
				"registry.example.invalid",
				"private-container-id",
				"sk-live-service-secret-t167",
				"sk-live-version-secret-t167",
				"127.0.0.1",
				"passed",
				"supported",
				"ready",
			} {
				if strings.Contains(strings.ToLower(allOutput), strings.ToLower(forbidden)) {
					t.Errorf("status output exposed forbidden value %q: %s", forbidden, allOutput)
				}
			}

			invocation := strings.TrimSpace(mustReadRunbookFile(t, dockerLog))
			project := "longtermism-observability"
			if profile == "signoz" {
				project = "longtermism-signoz"
			}
			wantInvocation := `ps --all --filter label=com.docker.compose.project=` + project + ` --format {{.Label "com.docker.compose.service"}}|{{.Status}}|{{.Image}}`
			if invocation != wantInvocation {
				t.Errorf("status Docker query = %q; want %q", invocation, wantInvocation)
			}
		})
	}

	if err := os.WriteFile(dockerLog, nil, 0o600); err != nil {
		t.Fatalf("reset docker log: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mustLookPath(t, "make"), "--no-print-directory", "obs-status")
	cmd.Dir = repoRoot
	cmd.Env = []string{"PATH=" + fakeDir + string(os.PathListSeparator) + systemToolPath(), "HOME=" + fakeDir, "TMPDIR=" + fakeDir, "OBS_PROFILE=malicious;profile", "FAKE_DOCKER_LOG=" + dockerLog}
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

	if err := os.WriteFile(dockerLog, nil, 0o600); err != nil {
		t.Fatalf("reset docker log: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd = exec.CommandContext(ctx, mustLookPath(t, "make"), "--no-print-directory", "obs-status")
	cmd.Dir = repoRoot
	cmd.Env = []string{"PATH=" + fakeDir + string(os.PathListSeparator) + systemToolPath(), "HOME=" + fakeDir, "TMPDIR=" + fakeDir, "OBSERVABILITY_COMPOSE_PROJECT=bad;project", "FAKE_DOCKER_LOG=" + dockerLog}
	output, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatal("obs-status accepted an unsafe compose project")
	}
	if strings.Contains(string(output), "bad;project") {
		t.Errorf("project validation echoed rejected input: %s", output)
	}
	if invocation := mustReadRunbookFile(t, dockerLog); invocation != "" {
		t.Errorf("unsafe project reached Docker: %q", invocation)
	}

	t.Run("empty inventory", func(t *testing.T) {
		result := runStatusMake(t, repoRoot, fakeDir, dockerLog, "FAKE_DOCKER_EMPTY=1")
		if result.err != nil {
			t.Fatalf("empty inventory failed: %v\n%s", result.err, result.output)
		}
		want := "profile=grafana evidence=diagnostic_only query_evidence=not_run\nservice=none state=absent health=unknown version=unknown\n"
		if result.output != want {
			t.Errorf("empty inventory output = %q; want %q", result.output, want)
		}
	})
	t.Run("docker failure", func(t *testing.T) {
		result := runStatusMake(t, repoRoot, fakeDir, dockerLog, "FAKE_DOCKER_EXIT=19")
		if result.err == nil {
			t.Fatal("status ignored Docker failure")
		}
		if !strings.Contains(result.output, "obs-status: docker inventory query failed") || strings.Contains(result.output, "synthetic-secret-t167") {
			t.Errorf("Docker failure was not stable and redacted: %s", result.output)
		}
	})
}

// TestLiveE2EPreflightRejectsUnsafeInputsBeforeDocker guards the boundary added by T167:
// malformed project/env-file and incomplete live refs must fail before any profile lifecycle starts.
func TestLiveE2EPreflightRejectsUnsafeInputsBeforeDocker(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	tempDir := t.TempDir()
	dockerLog := filepath.Join(tempDir, "docker.log")
	writeExecutable(t, filepath.Join(tempDir, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_DOCKER_LOG\"\n")
	writeExecutable(t, filepath.Join(tempDir, "go"), "#!/bin/sh\nprintf 'go %s\\n' \"$*\" >> \"$FAKE_DOCKER_LOG\"\nexit 91\n")
	writeExecutable(t, filepath.Join(tempDir, "curl"), "#!/bin/sh\nprintf 'curl %s\\n' \"$*\" >> \"$FAKE_DOCKER_LOG\"\nexit 91\n")
	fakeRecursiveMake := filepath.Join(tempDir, "recursive-make")
	writeExecutable(t, fakeRecursiveMake, "#!/bin/sh\nprintf 'make %s\\n' \"$*\" >> \"$FAKE_DOCKER_LOG\"\nexit 91\n")
	baseEnv := []string{"PATH=" + tempDir + string(os.PathListSeparator) + systemToolPath(), "HOME=" + tempDir, "TMPDIR=" + tempDir, "FAKE_DOCKER_LOG=" + dockerLog}

	tests := []struct {
		name   string
		target string
		args   []string
		env    []string
	}{
		{name: "grafana missing refs", target: "obs-grafana-e2e"},
		{name: "signoz missing refs", target: "obs-signoz-e2e"},
		{name: "resilience missing refs", target: "obs-resilience-e2e"},
		{name: "command-line unsafe project", target: "obs-grafana-up", args: []string{"OBSERVABILITY_COMPOSE_PROJECT=bad;project"}},
		{name: "environment unsafe project", target: "obs-grafana-up", env: []string{"OBSERVABILITY_COMPOSE_PROJECT=bad;project"}},
		{
			name:   "external endpoint",
			target: "obs-grafana-e2e",
			env: replaceEnvironmentValue(
				syntheticGrafanaLiveEnvironment(tempDir),
				"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL=https://example.invalid/private",
			),
		},
		{
			name:   "relative artifact path",
			target: "obs-grafana-e2e",
			env: replaceEnvironmentValue(
				syntheticGrafanaLiveEnvironment(tempDir),
				"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT=relative/private",
			),
		},
		{
			name:   "resilience project mismatch",
			target: "obs-resilience-e2e",
			env: replaceEnvironmentValue(
				syntheticResilienceLiveEnvironment(tempDir),
				"LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT=another-project",
			),
		},
	}
	makeExpansionMarker := filepath.Join(tempDir, "make-expansion-marker")
	tests = append(tests, struct {
		name   string
		target string
		args   []string
		env    []string
	}{
		name:   "command-line make function syntax remains inert",
		target: "obs-grafana-up",
		args:   []string{"OBSERVABILITY_COMPOSE_PROJECT=$(shell touch " + makeExpansionMarker + ")"},
	})
	derivedMarkers := []string{}
	for _, variable := range []string{
		"SAFE_OBSERVABILITY_COMPOSE_PROJECT",
		"SAFE_OBSERVABILITY_SIGNOZ_COMPOSE_PROJECT",
		"SAFE_OBSERVABILITY_LOCAL_ENV_FILE",
		"SAFE_OBS_PROFILE",
		"OBSERVABILITY_LOCAL_ENV_OPTION",
		"OBS_LANGFUSE_COMPOSE",
		"OBS_GRAFANA_COMPOSE",
		"OBS_SIGNOZ_COMPOSE",
	} {
		marker := filepath.Join(tempDir, "marker-"+strings.ToLower(variable))
		derivedMarkers = append(derivedMarkers, marker)
		tests = append(tests, struct {
			name   string
			target string
			args   []string
			env    []string
		}{
			name:   "derived override remains inert " + variable,
			target: "obs-grafana-e2e",
			args:   []string{variable + "=$(shell touch " + marker + ")"},
		})
	}
	for name, endpoint := range map[string]string{
		"path":      "http://127.0.0.1:9090/private",
		"ipv6":      "http://[::1]:9090",
		"port zero": "http://127.0.0.1:0",
		"port high": "http://127.0.0.1:65536",
		"userinfo":  "http://user@127.0.0.1:9090",
		"query":     "http://127.0.0.1:9090?secret=value",
		"fragment":  "http://127.0.0.1:9090#private",
	} {
		tests = append(tests, struct {
			name   string
			target string
			args   []string
			env    []string
		}{
			name:   "invalid endpoint " + name,
			target: "obs-grafana-e2e",
			env: replaceEnvironmentValue(
				syntheticGrafanaLiveEnvironment(tempDir),
				"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL="+endpoint,
			),
		})
	}
	tests = append(tests,
		struct {
			name   string
			target string
			args   []string
			env    []string
		}{name: "invalid privacy digest", target: "obs-grafana-e2e", env: replaceEnvironmentValue(syntheticGrafanaLiveEnvironment(tempDir), "LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST=sha256:not-a-digest")},
		struct {
			name   string
			target string
			args   []string
			env    []string
		}{name: "invalid privacy component", target: "obs-grafana-e2e", env: replaceEnvironmentValue(syntheticGrafanaLiveEnvironment(tempDir), "LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY=wrong-component")},
	)
	tests = append(tests, struct {
		name   string
		target string
		args   []string
		env    []string
	}{
		name:   "environment make function syntax remains inert",
		target: "obs-grafana-up",
		env:    []string{"OBSERVABILITY_COMPOSE_PROJECT=$(shell touch " + makeExpansionMarker + ")"},
	})
	outsideEnv := filepath.Join(tempDir, "outside.env")
	if err := os.WriteFile(outsideEnv, []byte("SAFE=value\n"), 0o600); err != nil {
		t.Fatalf("write outside env fixture: %v", err)
	}
	tests = append(tests, struct {
		name   string
		target string
		args   []string
		env    []string
	}{name: "command-line env file outside repository", target: "obs-grafana-up", args: []string{"OBSERVABILITY_LOCAL_ENV_FILE=" + outsideEnv}})
	tests = append(tests, struct {
		name   string
		target string
		args   []string
		env    []string
	}{name: "environment env file outside repository", target: "obs-grafana-up", env: []string{"OBSERVABILITY_LOCAL_ENV_FILE=" + outsideEnv}})
	tests = append(tests, struct {
		name   string
		target string
		args   []string
		env    []string
	}{name: "repo env file has insecure mode", target: "obs-grafana-up", env: []string{"OBSERVABILITY_LOCAL_ENV_FILE=deploy/observability/.env.local.example"}})
	symlinkRoot := filepath.Join(tempDir, "symlink-root")
	if err := os.Symlink(tempDir, symlinkRoot); err != nil {
		t.Fatalf("create artifact symlink fixture: %v", err)
	}
	tests = append(tests, struct {
		name   string
		target string
		args   []string
		env    []string
	}{
		name:   "artifact parent symlink",
		target: "obs-grafana-e2e",
		env: replaceEnvironmentValue(
			syntheticGrafanaLiveEnvironment(tempDir),
			"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH="+filepath.Join(symlinkRoot, "evidence.jsonl"),
		),
	})
	wrongType := filepath.Join(tempDir, "projection-directory")
	if err := os.Mkdir(wrongType, 0o700); err != nil {
		t.Fatalf("create artifact type fixture: %v", err)
	}
	tests = append(tests, struct {
		name   string
		target string
		args   []string
		env    []string
	}{
		name:   "artifact wrong type",
		target: "obs-grafana-e2e",
		env: replaceEnvironmentValue(
			syntheticGrafanaLiveEnvironment(tempDir),
			"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH="+wrongType,
		),
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(dockerLog, nil, 0o600); err != nil {
				t.Fatalf("reset Docker log: %v", err)
			}
			arguments := append([]string{"--no-print-directory", "MAKE=" + fakeRecursiveMake}, tt.args...)
			arguments = append(arguments, tt.target)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, mustLookPath(t, "make"), arguments...)
			cmd.Dir = repoRoot
			cmd.Env = append(append([]string{}, baseEnv...), tt.env...)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("unsafe live input unexpectedly passed: %s", output)
			}
			if ctx.Err() != nil {
				t.Fatalf("live preflight timed out: %v", ctx.Err())
			}
			if invocation := mustReadRunbookFile(t, dockerLog); invocation != "" {
				t.Errorf("preflight reached Docker: %q", invocation)
			}
			for _, forbidden := range append(tt.args, "SAFE=value") {
				if forbidden != "" && strings.Contains(string(output), forbidden) {
					t.Errorf("preflight echoed rejected value %q: %s", forbidden, output)
				}
			}
			for _, entry := range tt.env {
				_, value, _ := strings.Cut(entry, "=")
				if value != "" && strings.Contains(string(output), value) {
					t.Errorf("preflight echoed live reference value: %s", output)
				}
			}
		})
	}
	if _, err := os.Stat(makeExpansionMarker); !os.IsNotExist(err) {
		t.Fatalf("untrusted Make value executed before preflight: %v", err)
	}
	for _, marker := range derivedMarkers {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("derived Make override executed before preflight: %s", marker)
		}
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mustLookPath(t, "make"), "-j4", "--no-print-directory", "MAKE="+fakeMake, target)
	cmd.Dir = repoRoot
	cmd.Env = []string{"PATH=" + systemToolPath(), "HOME=" + tempDir, "TMPDIR=" + tempDir, "FAKE_MAKE_LOG=" + callLog, "FAKE_FAIL_TARGET=" + failedTarget}
	output, err := cmd.CombinedOutput()
	calls := []string{}
	if content, readErr := os.ReadFile(callLog); readErr == nil {
		calls = strings.Fields(string(content))
	} else if !os.IsNotExist(readErr) {
		t.Fatalf("read recursive make log: %v", readErr)
	}
	return aggregateMakeResult{calls: calls, output: string(output), err: err}
}

func runStatusMake(t *testing.T, repoRoot, fakeDir, dockerLog string, extraEnv ...string) aggregateMakeResult {
	t.Helper()
	if err := os.WriteFile(dockerLog, nil, 0o600); err != nil {
		t.Fatalf("reset Docker log: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mustLookPath(t, "make"), "--no-print-directory", "obs-status")
	cmd.Dir = repoRoot
	cmd.Env = append([]string{"PATH=" + fakeDir + string(os.PathListSeparator) + systemToolPath(), "HOME=" + fakeDir, "TMPDIR=" + fakeDir, "FAKE_DOCKER_LOG=" + dockerLog}, extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		output = append(output, stderr.Bytes()...)
	}
	return aggregateMakeResult{output: string(output), err: err}
}

func assertDefaultAndLevelZeroDependencyGraph(t *testing.T, makefile string) {
	t.Helper()
	dependencies := map[string][]string{}
	defaultTarget := ""
	for _, line := range strings.Split(makefile, "\n") {
		if line == "" || line[0] == '\t' || strings.HasPrefix(line, "#") || strings.Contains(line, "=") {
			continue
		}
		name, rest, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !found || name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		if defaultTarget == "" && !strings.HasPrefix(name, ".") {
			defaultTarget = name
		}
		for _, dependency := range strings.Fields(strings.Split(rest, "|")[0]) {
			if !strings.Contains(dependency, "$") {
				dependencies[name] = append(dependencies[name], dependency)
			}
		}
	}
	if strings.Contains(makefile, ".DEFAULT_GOAL") {
		t.Fatal("Makefile must not override the first-target Level 0 default goal")
	}
	if defaultTarget != "verify" {
		t.Fatalf("default Make target = %q; want verify", defaultTarget)
	}
	if line := makeTargetLine(t, makefile, "verify"); line != "verify: vet test" {
		t.Fatalf("verify declaration = %q; want exact Level 0 dependencies", line)
	}
	for target, wantRecipe := range map[string]string{"test": "go test ./...", "vet": "go vet ./..."} {
		if line := makeTargetLine(t, makefile, target); line != target+":" {
			t.Fatalf("%s must not have dependencies: %q", target, line)
		}
		recipe := ""
		for _, line := range strings.Split(makeTargetBlock(t, makefile, target), "\n") {
			if strings.HasPrefix(line, "\t") {
				recipe += strings.TrimSpace(line) + "\n"
			}
		}
		if recipe != wantRecipe+"\n" {
			t.Fatalf("%s recipe = %q; want %q", target, recipe, wantRecipe)
		}
	}
	for _, start := range []string{"verify", "test", "vet"} {
		seen := map[string]bool{}
		var visit func(string)
		visit = func(target string) {
			if seen[target] {
				return
			}
			seen[target] = true
			for _, dependency := range dependencies[target] {
				visit(dependency)
			}
		}
		visit(start)
		for _, forbidden := range []string{"obs-release-gate", "obs-signoz-compat-gate", "obs-grafana-e2e", "obs-signoz-e2e", "obs-resilience-e2e"} {
			if seen[forbidden] {
				t.Errorf("Level 0 target %s reaches live target %s", start, forbidden)
			}
		}
	}
}

func mustLookPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	return path
}

func systemToolPath() string {
	return "/usr/bin:/bin:/usr/sbin:/sbin"
}

func syntheticGrafanaLiveEnvironment(root string) []string {
	return []string{
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL=http://127.0.0.1:9090",
		"LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL=http://127.0.0.1:3100",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL=http://127.0.0.1:3200",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL=http://127.0.0.1:3001",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL=synthetic-credential",
		"LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL=http://127.0.0.1:8000",
		"LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL=synthetic-credential",
		"LONGTERMISM_SMOKE_APP_BASE_URL=http://127.0.0.1:8000",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION=synthetic-authorization",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT=" + filepath.Join(root, "manifests"),
		"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH=" + filepath.Join(root, "evidence.jsonl"),
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH=" + filepath.Join(root, "projection.json"),
		"LONGTERMISM_SMOKE_PRIVACY_ARTIFACT_ROOT=" + filepath.Join(root, "privacy"),
		"LONGTERMISM_SMOKE_COLLECTOR_RUNTIME_CONFIG_DIGEST=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"LONGTERMISM_SMOKE_COLLECTOR_COMPONENT_IDENTITY=otlphttp/loki",
		"LONGTERMISM_SMOKE_EXPORT_ADMISSION_CORRELATION=synthetic-correlation",
	}
}

func syntheticResilienceLiveEnvironment(root string) []string {
	return []string{
		"LONGTERMISM_SMOKE_APP_BASE_URL=http://127.0.0.1:8000",
		"LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL=http://127.0.0.1:9090",
		"LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL=http://127.0.0.1:3200",
		"LONGTERMISM_SMOKE_RESILIENCE_COMPOSE_PROJECT=longtermism-observability",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL=http://127.0.0.1:3001",
		"LONGTERMISM_SMOKE_CHAT_AUTHORIZATION=synthetic-authorization",
		"LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL=synthetic-credential",
		"LONGTERMISM_SMOKE_CHAT_MANIFEST_ROOT=" + filepath.Join(root, "manifests"),
		"LONGTERMISM_SMOKE_SCORE_EVIDENCE_PATH=" + filepath.Join(root, "evidence.jsonl"),
		"LONGTERMISM_SMOKE_SCORE_PROJECTION_PATH=" + filepath.Join(root, "projection.json"),
	}
}

func replaceEnvironmentValue(environment []string, replacement string) []string {
	name, _, _ := strings.Cut(replacement, "=")
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		entryName, _, _ := strings.Cut(entry, "=")
		if entryName != name {
			result = append(result, entry)
		}
	}
	return append(result, replacement)
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
	recipe := ""
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "\t") {
			recipe += line + "\n"
		}
	}
	for _, forbidden := range []string{"docker ", "go ", "curl ", "bash ", "obs-status", "obs-reset", " &"} {
		if strings.Contains(recipe, forbidden) {
			t.Fatalf("%s aggregate directly invokes forbidden operation %q", target, forbidden)
		}
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
