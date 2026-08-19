package failure

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// T111 受控容器操作契约测试（RED 先行，T121 实现 docker_control.go 使其 GREEN）。
//
// 覆盖的生产风险：故障注入本身失控。生产环境注入 pause/restart 时最容易出
// 三类事故——命令把 shell 元字符拼进未校验参数（注入执行）、作用域越界
// （误停其它 compose project 的服务）、恢复路径被业务错误或 ctx 取消跳过
// （服务被永久留在 paused 状态）。因此契约要求：
//
// 1. 只通过参数化 CommandRunner 执行，argv 逐项传递，禁止 sh -c 拼接；
// 2. project/service 在触达 runner 之前必须通过安全校验；
// 3. 每个 docker 调用都显式携带 `-p <project>`，作用域不得越出当前项目；
// 4. ctx 取消必须传播给 runner，不吞掉超时；
// 5. WithRestore 在 fn 成功/失败/panic 任何退出路径都尝试恢复，恢复失败
//    记录为 RestoreResidual，供 smoke 报告保留（T121 的 residual resources）。

// fakeRunner 记录每次调用的 argv 并返回可编排的输出/错误；当 fn 为 nil 时
// 成功返回。blocking 语义由调用方在 fn 中实现（等待 ctx.Done 再返回）。
type fakeRunner struct {
	fn    func(ctx context.Context, name string, args ...string) ([]byte, error)
	calls []fakeRunnerCall
}

type fakeRunnerCall struct {
	name string
	args []string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeRunnerCall{name: name, args: slices.Clone(args)})
	if f.fn != nil {
		return f.fn(ctx, name, args...)
	}
	return nil, nil
}

func (f *fakeRunner) lastArgs() []string {
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1].args
}

func newTestControl(t *testing.T, runner *fakeRunner) *DockerControl {
	t.Helper()
	control, err := NewDockerControl(runner, "longtermism-obs")
	if err != nil {
		t.Fatalf("NewDockerControl = err %v, want nil", err)
	}
	return control
}

func wantComposeArgs(verb, service string) []string {
	return []string{"compose", "-p", "longtermism-obs", verb, service}
}

func TestPauseSendsScopedComposeCommand(t *testing.T) {
	runner := &fakeRunner{}
	control := newTestControl(t, runner)

	if err := control.Pause(context.Background(), "otel-collector"); err != nil {
		t.Fatalf("Pause() = err %v, want nil", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner 调用次数 = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "docker" {
		t.Errorf("命令名 = %q, want docker", call.name)
	}
	if !slices.Equal(call.args, wantComposeArgs("pause", "otel-collector")) {
		t.Errorf("argv = %v, want %v", call.args, wantComposeArgs("pause", "otel-collector"))
	}
}

func TestUnpauseSendsScopedComposeCommand(t *testing.T) {
	runner := &fakeRunner{}
	control := newTestControl(t, runner)

	if err := control.Unpause(context.Background(), "langfuse-worker"); err != nil {
		t.Fatalf("Unpause() = err %v, want nil", err)
	}
	if !slices.Equal(runner.lastArgs(), wantComposeArgs("unpause", "langfuse-worker")) {
		t.Errorf("argv = %v, want %v", runner.lastArgs(), wantComposeArgs("unpause", "langfuse-worker"))
	}
}

func TestRestartSendsScopedComposeCommand(t *testing.T) {
	runner := &fakeRunner{}
	control := newTestControl(t, runner)

	if err := control.Restart(context.Background(), "otel-collector"); err != nil {
		t.Fatalf("Restart() = err %v, want nil", err)
	}
	if !slices.Equal(runner.lastArgs(), wantComposeArgs("restart", "otel-collector")) {
		t.Errorf("argv = %v, want %v", runner.lastArgs(), wantComposeArgs("restart", "otel-collector"))
	}
}

// 项目作用域不变量：每个 docker 调用都必须显式携带 `-p <project>`，
// 作用域不得越出当前 compose project（T118 安全 reset 同理要求项目隔离）。
func TestEveryCommandCarriesProjectScope(t *testing.T) {
	runner := &fakeRunner{}
	control := newTestControl(t, runner)
	ctx := context.Background()

	for _, op := range []struct {
		verb    string
		invoke  func() error
		service string
	}{
		{"pause", func() error { return control.Pause(ctx, "otel-collector") }, "otel-collector"},
		{"unpause", func() error { return control.Unpause(ctx, "otel-collector") }, "otel-collector"},
		{"restart", func() error { return control.Restart(ctx, "langfuse-web") }, "langfuse-web"},
	} {
		if err := op.invoke(); err != nil {
			t.Fatalf("%s() = err %v, want nil", op.verb, err)
		}
	}
	for i, call := range runner.calls {
		if len(call.args) < 4 || call.args[0] != "compose" || call.args[1] != "-p" || call.args[2] != "longtermism-obs" {
			t.Errorf("第 %d 次调用缺少项目作用域: %v", i, call.args)
		}
	}
}

// 未校验的 shell 参数是注入执行漏洞：service 名在触达 runner 之前必须被
// 拒绝，且 runner 一次都不得被调用。
func TestServiceNameValidationRejectsShellMetacharacters(t *testing.T) {
	invalid := []string{
		"",
		"tempo;touch /tmp/pwned",
		"langfuse web",
		"$(whoami)",
		"`id`",
		"../escape",
		"-f",
		"web&",
		"svc|nc",
		strings.Repeat("a", 65),
	}
	valid := []string{
		"otel-collector",
		"langfuse-web",
		"langfuse_worker.v2",
	}
	for _, service := range invalid {
		runner := &fakeRunner{}
		control := newTestControl(t, runner)
		if err := control.Pause(context.Background(), service); err == nil {
			t.Errorf("Pause(%q) = nil, want 校验错误", service)
		}
		if len(runner.calls) != 0 {
			t.Errorf("Pause(%q) 触发了 runner 调用：未校验参数被拼接执行", service)
		}
	}
	for _, service := range valid {
		runner := &fakeRunner{}
		control := newTestControl(t, runner)
		if err := control.Pause(context.Background(), service); err != nil {
			t.Errorf("Pause(%q) = err %v, want nil", service, err)
		}
		if len(runner.calls) != 1 {
			t.Errorf("Pause(%q) runner 调用次数 = %d, want 1", service, len(runner.calls))
		}
	}
}

// project 名同样不得包含 shell 元字符；非法 project 在构造时拒绝，
// 而不是等第一个命令执行时才失败。
func TestProjectNameValidationRejectsInvalidProject(t *testing.T) {
	runner := &fakeRunner{}
	for _, project := range []string{"", "obs; rm -rf /", "a b", "$(id)", strings.Repeat("x", 65)} {
		if control, err := NewDockerControl(runner, project); err == nil {
			t.Errorf("NewDockerControl(project=%q) = nil error, want 校验错误", project)
			_ = control
		}
	}
}

// ctx 取消（含超时）必须原样传播给调用方，不能被吞掉后留下一个
// 实际未生效的故障注入。
func TestContextCancellationPropagated(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	control := newTestControl(t, runner)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := control.Pause(ctx, "otel-collector"); !errors.Is(err, context.Canceled) {
		t.Errorf("Pause() = %v, want context.Canceled", err)
	}

	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelTimeout()
	if err := control.Restart(timeoutCtx, "otel-collector"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Restart() = %v, want context.DeadlineExceeded", err)
	}
}

// WithRestore 的编排顺序：pause -> fn -> unpause，fn 收到的 ctx 必须与调用方一致。
func TestWithRestoreRunsFnBetweenPauseAndUnpause(t *testing.T) {
	runner := &fakeRunner{}
	control := newTestControl(t, runner)
	ctx := context.Background()

	var fnGotCtx context.Context
	residuals, err := control.WithRestore(ctx, "otel-collector", func(fnCtx context.Context) error {
		fnGotCtx = fnCtx
		return nil
	})
	if err != nil {
		t.Fatalf("WithRestore() = err %v, want nil", err)
	}
	if len(residuals) != 0 {
		t.Errorf("residuals = %v, want 空", residuals)
	}
	if fnGotCtx != ctx {
		t.Error("fn 收到的 ctx 与调用方不一致")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner 调用次数 = %d, want 2 (pause + unpause)", len(runner.calls))
	}
	if !slices.Equal(runner.calls[0].args, wantComposeArgs("pause", "otel-collector")) {
		t.Errorf("第 1 次 argv = %v", runner.calls[0].args)
	}
	if !slices.Equal(runner.calls[1].args, wantComposeArgs("unpause", "otel-collector")) {
		t.Errorf("第 2 次 argv = %v", runner.calls[1].args)
	}
}

// fn 失败时业务错误原样返回，但服务必须被恢复——观测故障注入不得把
// 服务留在 paused 状态。
func TestWithRestoreRestoresWhenFnFails(t *testing.T) {
	runner := &fakeRunner{}
	control := newTestControl(t, runner)
	fnErr := errors.New("smoke check failed")

	residuals, err := control.WithRestore(context.Background(), "otel-collector", func(context.Context) error {
		return fnErr
	})
	if !errors.Is(err, fnErr) {
		t.Errorf("WithRestore() = %v, want fn 的错误原样返回", err)
	}
	if len(residuals) != 0 {
		t.Errorf("residuals = %v, want 空（恢复成功）", residuals)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner 调用次数 = %d, want 2：fn 失败后仍必须 unpause", len(runner.calls))
	}
	if !slices.Equal(runner.calls[1].args, wantComposeArgs("unpause", "otel-collector")) {
		t.Errorf("恢复 argv = %v", runner.calls[1].args)
	}
}

// 恢复失败必须记录 RestoreResidual（service + 原因），供 smoke 报告保留；
// 不得静默吞掉——服务可能仍处于 paused，这是运营必须看到的事实。
func TestWithRestoreRecordsResidualWhenRestoreFails(t *testing.T) {
	restoreErr := errors.New("compose: container gone")
	runner := &fakeRunner{fn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[3] == "unpause" {
			return nil, restoreErr
		}
		return nil, nil
	}}
	control := newTestControl(t, runner)

	residuals, err := control.WithRestore(context.Background(), "otel-collector", func(context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithRestore() = err %v, want nil（fn 成功时 err 保持 nil）", err)
	}
	if len(residuals) != 1 {
		t.Fatalf("residuals 长度 = %d, want 1", len(residuals))
	}
	if residuals[0].Service != "otel-collector" {
		t.Errorf("residual.Service = %q, want otel-collector", residuals[0].Service)
	}
	if !errors.Is(residuals[0].Err, restoreErr) {
		t.Errorf("residual.Err = %v, want %v", residuals[0].Err, restoreErr)
	}
}

// fn 执行期间 ctx 被取消（例如 smoke 超时）时，恢复不得继承已取消的
// ctx——否则 unpause 永远不会真正执行。恢复必须使用独立的活跃上下文。
// 探针在恢复调用发生那一刻采样 ctx 存活状态：返回后的 ctx 状态属于实现
// 内部的资源清理（defer cancel），不是契约语义。
func TestWithRestoreUsesLiveContextWhenCallerCtxCanceled(t *testing.T) {
	restoreCtxLive := false
	restoreCtxSeen := false
	runner := &fakeRunner{fn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[3] == "unpause" {
			restoreCtxSeen = true
			restoreCtxLive = ctx.Err() == nil
		}
		return nil, nil
	}}
	control := newTestControl(t, runner)
	ctx, cancel := context.WithCancel(context.Background())

	_, err := control.WithRestore(ctx, "otel-collector", func(context.Context) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithRestore() = %v, want context.Canceled", err)
	}
	if !restoreCtxSeen {
		t.Fatal("unpause 未被调用")
	}
	if !restoreCtxLive {
		t.Error("恢复调用发生时 ctx 已失效：恢复必须独立于调用方 ctx")
	}
}

// panic 也是退出路径：恢复必须发生，panic 随后原样传播。
func TestWithRestoreRestoresAfterPanic(t *testing.T) {
	runner := &fakeRunner{}
	control := newTestControl(t, runner)
	panicValue := "smoke assertion blew up"

	defer func() {
		if recovered := recover(); recovered != panicValue {
			t.Errorf("recover = %v, want %v", recovered, panicValue)
		}
		if len(runner.calls) != 2 {
			t.Errorf("runner 调用次数 = %d, want 2：panic 后仍必须 unpause", len(runner.calls))
			return
		}
		if !slices.Equal(runner.calls[1].args, wantComposeArgs("unpause", "otel-collector")) {
			t.Errorf("恢复 argv = %v", runner.calls[1].args)
		}
	}()

	_, _ = control.WithRestore(context.Background(), "otel-collector", func(context.Context) error {
		panic(panicValue)
	})
	t.Fatal("WithRestore 未重新抛出 panic")
}

// pause 本身失败说明故障未注入成功：fn 不得执行（否则业务在未注入
// 状态下运行），也无需恢复。
func TestWithRestoreSkipsFnWhenPauseFails(t *testing.T) {
	pauseErr := errors.New("compose: no such service")
	runner := &fakeRunner{fn: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[3] == "pause" {
			return nil, pauseErr
		}
		return nil, nil
	}}
	control := newTestControl(t, runner)
	fnCalled := false

	_, err := control.WithRestore(context.Background(), "otel-collector", func(context.Context) error {
		fnCalled = true
		return nil
	})
	if !errors.Is(err, pauseErr) {
		t.Errorf("WithRestore() = %v, want pause 错误", err)
	}
	if fnCalled {
		t.Error("fn 在 pause 失败后被执行")
	}
	if len(runner.calls) != 1 {
		t.Errorf("runner 调用次数 = %d, want 1（无恢复尝试）", len(runner.calls))
	}
}
