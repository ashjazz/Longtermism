package failure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"
)

// T111 契约的 GREEN 实现：受控容器操作。
//
// 生产约束：
// 1. 只通过参数化 CommandRunner 执行（argv 逐项传递），绝不 sh -c 拼接，
//    服务名与项目名在触达 runner 之前必须通过安全校验；
// 2. 每次 docker 调用都显式携带 `compose -p <project>`，作用域不越出
//    当前 compose project；
// 3. WithRestore 在 fn 成功/失败/panic 任何退出路径都尝试恢复服务，恢复
//    使用独立于调用方 ctx 的有界上下文（调用方 ctx 可能已超时/取消，
//    恢复不能继承失败），恢复失败记录为 RestoreResidual。

var (
	// ErrDockerControlInvalidService 表示服务名未通过安全校验（shell 元字符、
	// 空串、超长或非法首字符），拒绝执行而不是尝试拼进命令。
	ErrDockerControlInvalidService = errors.New("failure: unsafe service name")

	// ErrDockerControlInvalidProject 表示 compose project 名未通过安全校验。
	ErrDockerControlInvalidProject = errors.New("failure: unsafe project name")

	// ErrDockerControlUnconfigured 表示 command runner 缺失，构造时即拒绝，
	// 避免首次调用时才 panic。
	ErrDockerControlUnconfigured = errors.New("failure: command runner is required")

	// safeDockerScopePattern 是 project/service 标识的安全边界：字母数字开头，
	// 后续只允许字母数字、下划线、点、连字符，最长 64。$、;、空格、反引号
	// 等 shell 元字符一律拒绝。
	safeDockerScopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

	// restoreTimeout 是恢复操作的独立上界：恢复不得继承调用方已取消的 ctx，
	// 也不能永久阻塞（例如 docker daemon 假死时不能让服务停在 paused 状态）。
	restoreTimeout = 30 * time.Second
)

// CommandRunner 是参数化命令执行端口。实现（如 exec.CommandContext 包装或
// 测试 fake）只接收 argv 列表；禁止任何实现走 shell 字符串拼接。
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// RestoreResidual 记录一次失败的服务恢复。它必须被调用方带回 smoke 报告
// （cleanup 段），因为服务可能仍处于 paused 状态——这是运营必须看到的事实。
type RestoreResidual struct {
	Service string
	Err     error
}

// DockerControl 提供限定当前 compose project 的受控容器操作。
type DockerControl struct {
	runner  CommandRunner
	project string
}

// NewDockerControl 构造控制器并在构造时校验 project：非法 project 绝不进入
// 任何执行路径。runner 缺失同样在构造时拒绝。
func NewDockerControl(runner CommandRunner, project string) (*DockerControl, error) {
	if runner == nil {
		return nil, ErrDockerControlUnconfigured
	}
	if !safeDockerScopePattern.MatchString(project) {
		return nil, fmt.Errorf("%w: %q", ErrDockerControlInvalidProject, project)
	}
	return &DockerControl{runner: runner, project: project}, nil
}

func (c *DockerControl) validateService(service string) error {
	if c == nil {
		return ErrDockerControlUnconfigured
	}
	if !safeDockerScopePattern.MatchString(service) {
		return fmt.Errorf("%w: %q", ErrDockerControlInvalidService, service)
	}
	return nil
}

// runCompose 把所有操作收敛到唯一一条 compose 命令构造路径，保证 `-p
// <project>` 作用域不会被任何调用点遗漏。
func (c *DockerControl) runCompose(ctx context.Context, verb, service string) error {
	if _, err := c.runner.Run(ctx, "docker", "compose", "-p", c.project, verb, service); err != nil {
		return fmt.Errorf("docker compose -p %s %s %s: %w", c.project, verb, service, err)
	}
	return nil
}

// Pause 暂停当前 project 内的一个服务。
func (c *DockerControl) Pause(ctx context.Context, service string) error {
	if err := c.validateService(service); err != nil {
		return err
	}
	return c.runCompose(ctx, "pause", service)
}

// Unpause 恢复一个被暂停的服务。
func (c *DockerControl) Unpause(ctx context.Context, service string) error {
	if err := c.validateService(service); err != nil {
		return err
	}
	return c.runCompose(ctx, "unpause", service)
}

// Restart 重启当前 project 内的一个服务。
func (c *DockerControl) Restart(ctx context.Context, service string) error {
	if err := c.validateService(service); err != nil {
		return err
	}
	return c.runCompose(ctx, "restart", service)
}

// WithRestore 执行一次受控故障注入：pause -> fn -> unpause。fn 的任何退出
// 路径（成功、错误、panic）都会触发恢复尝试；恢复使用独立有界 ctx，恢复
// 失败以 RestoreResidual 返回而不是静默吞掉。
//
// pause 本身失败时 fn 不执行、也不尝试恢复：故障未注入成功，没有需要
// 恢复的事实（与 smoke runner 的注入失败语义一致）。
func (c *DockerControl) WithRestore(ctx context.Context, service string, fn func(context.Context) error) (residuals []RestoreResidual, err error) {
	if err := c.validateService(service); err != nil {
		return nil, err
	}
	if err := c.Pause(ctx, service); err != nil {
		return nil, err
	}
	// panic 也是退出路径：defer 中恢复服务后原样重新抛出。恢复失败的
	// 事实无法经返回值传递（panic 优先），至少输出低敏诊断到 stderr——
	// 服务可能仍处于 paused，这个事实不允许无声丢失。
	defer func() {
		if recovered := recover(); recovered != nil {
			if restoreErr := c.unpauseWithFreshContext(service); restoreErr != nil {
				fmt.Fprintf(os.Stderr, "dockercontrol: restore after panic failed for service %q (service may remain paused)\n", service)
			}
			panic(recovered)
		}
	}()
	fnErr := fn(ctx)
	if restoreErr := c.unpauseWithFreshContext(service); restoreErr != nil {
		residuals = append(residuals, RestoreResidual{Service: service, Err: restoreErr})
	}
	return residuals, fnErr
}

// unpauseWithFreshContext 用独立的超时上下文执行恢复：调用方 ctx 可能已经
// 超时/取消（例如 smoke deadline 到期），恢复必须仍然真实发生。
func (c *DockerControl) unpauseWithFreshContext(service string) error {
	restoreCtx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
	defer cancel()
	return c.Unpause(restoreCtx, service)
}
