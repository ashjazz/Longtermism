# Quickstart：P0 最小 AI 工程闭环验证

## 目标

本指南用于验证 P0 是否已经打通最小闭环：

```text
提示词资产 -> 模型交互 -> 追踪记录 -> 评估报告 -> 本地门禁
```

默认验证不应强制依赖真实外部模型服务或 API key。

## 前置条件

- 已阅读 `specs/001-agent-framework-spec/spec.md`。
- 已阅读 `specs/001-agent-framework-spec/plan.md`。
- 已阅读 `docs/ROADMAP.md` 的 P0 冷启动执行计划。
- 本地 Go 环境可运行项目测试命令。

## 验证 1：规格和计划完整性

运行：

```bash
find specs/001-agent-framework-spec -maxdepth 2 -type f | sort
```

期望看到：

```text
specs/001-agent-framework-spec/checklists/requirements.md
specs/001-agent-framework-spec/contracts/core-framework-contract.md
specs/001-agent-framework-spec/contracts/evaluation-observability-contract.md
specs/001-agent-framework-spec/contracts/p0-provider-contract.md
specs/001-agent-framework-spec/data-model.md
specs/001-agent-framework-spec/plan.md
specs/001-agent-framework-spec/quickstart.md
specs/001-agent-framework-spec/research.md
specs/001-agent-framework-spec/spec.md
```

## 验证 2：当前代码测试基线

运行：

```bash
go test ./...
```

期望结果：

- 所有当前测试通过。
- 若存在失败，应先判断是否与当前计划无关，再记录到 journal 或后续 tasks。

## 验证 3：P0-A Provider 契约测试

在 P0-A 实现后运行：

```bash
go test ./pkg/ai/llm/...
```

期望覆盖：

- 正常响应。
- 工具调用解析。
- 429、5xx、timeout 错误分类。
- 400、401 错误分类。
- 流式输出。
- 上下文取消。
- 缺少必需字段时快速失败。

## 验证 4：P0-B Prompt 资产

在 P0-B 实现后运行：

```bash
go test ./pkg/ai/prompt/...
```

期望覆盖：

- 文件模板加载。
- 缺失变量失败。
- 渲染 hash 稳定。
- 路径穿越被拒绝。

## 验证 5：P0-C Trace

在 P0-C 实现后运行：

```bash
go test ./pkg/ai/obs/...
```

期望覆盖：

- trace 字段完整。
- 不记录原始敏感内容。
- 成功、失败、降级状态可表达。

## 验证 6：P0-D Eval Runner

在 P0-D 实现后运行：

```bash
go test ./pkg/ai/eval/...
```

期望覆盖：

- JSON dataset 可加载。
- 确定性 metric 可评分。
- runner 输出 report。
- 指标失败可定位。

## 验证 7：P0-E 本地门禁

在 P0-E 实现后运行：

```bash
go test ./...
go test -race ./pkg/ai/...
go vet ./...
```

如果提供 Makefile，运行：

```bash
make test
make test-race
make vet
make eval-smoke
```

期望结果：

- 默认命令不要求真实 API key。
- `eval-smoke` 输出稳定报告。
- 任何失败都能定位到测试、评估或静态检查。

## 可选真实服务 smoke

真实模型服务验证只能作为可选步骤。它应满足：

- 需要显式环境变量。
- 不进入默认本地门禁。
- 失败不会阻塞离线测试。
- 输出必须标记为 smoke 而不是确定性评估。
