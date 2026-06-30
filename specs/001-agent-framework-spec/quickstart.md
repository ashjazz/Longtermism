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
- 已阅读 `specs/001-agent-framework-spec/tasks.md`，并确认当前第一个未完成任务。
- 已阅读 `docs/ROADMAP.md` 的 P0 冷启动执行计划。
- 已阅读 `AGENTS.md` 的项目完成定义、学习型代码注释约定和常用命令。
- 本地 Go 环境可运行项目测试命令。

## 新会话恢复步骤

当上下文被新消息淹没，或由新的 AI 会话接手时，按以下顺序恢复项目状态：

1. 阅读 `AGENTS.md`，确认项目定位、技术栈、完成定义和当前 spec-kit 入口。
2. 阅读 `docs/ROADMAP.md` 的“当前 spec-kit 入口”和 P0 阶段说明，理解为什么先做最小 AI 工程闭环。
3. 阅读 `specs/001-agent-framework-spec/spec.md` 和 `plan.md`，确认本版本范围与技术计划。
4. 阅读 `specs/001-agent-framework-spec/tasks.md`，找到第一个未勾选的任务，作为下一步执行入口。
5. 阅读本文件，确认该任务所属 P0 子阶段需要运行哪些验证命令。
6. 如任务涉及架构边界，先查 `docs/adr/README.md`；如任务涉及学习记录，先查 `docs/journal/README.md`。
7. 执行任务后，更新 `tasks.md` 状态，并按任务要求同步 ADR、journal、ROADMAP 或 quickstart。

恢复完成后，执行者应能用一句话说明：

```text
当前要做的任务是 Txxx，它属于 P0-x，完成后必须通过哪些本地验证，并同步哪些文档。
```

## 任务执行顺序

当前版本采用 spec-kit 的任务顺序推进。除非 `tasks.md` 明确标记 `[P]` 可并行，默认按任务编号顺序执行。

```text
Phase 1 Setup：T001-T008
  建立 ADR、journal、忽略规则、Makefile、prompt/eval 文档和 spec-kit 导航。

Phase 2 Foundational：T009-T016
  建立共享测试工具、错误分类 ADR、离线验证 ADR 和本 quickstart 接力手册。

Phase 3 US1：T017-T022
  强化学习路径治理，让新会话可以从静态文档判断下一步。

Phase 4 US2 / P0 最小闭环：T023-T055
  P0-A llm provider -> P0-B prompt -> P0-C obs -> P0-D eval -> P0-E local gate。

Phase 5+：
  在 P0 最小闭环稳定后，再推进 RAG、Agent、resilience、ratelimit、cache 和后端可替换边界。
```

P0 子阶段的执行依赖如下：

```text
P0-A 模型供应商 Adapter
  -> P0-B Prompt as Code
  -> P0-C 本地 Trace
  -> P0-D Eval Runner
  -> P0-E 本地门禁与闭环
```

这条顺序不是为了追求串行，而是为了保证每一步都有可验证的前置能力：模型调用需要 prompt，prompt 与模型调用需要 trace，trace 与输出需要 eval，最后由本地门禁统一证明。

## P0 验证清单

每完成一个 P0 子阶段，先运行该阶段的局部验证，再运行仍然可用的全局验证。P0 默认验证必须遵守 ADR-0003：不得强制依赖真实 API key、真实向量库、真实可观测平台或实时外部服务。

| 阶段 | 任务范围 | 必跑验证 | 完成信号 |
|------|----------|----------|----------|
| Foundation | T009-T016 | `go test ./pkg/ai/...`、相关包 `go test -race` | fake provider、trace recorder、static dataset、错误分类与离线验证策略可被后续任务复用。 |
| P0-A | T023-T034B + T034 | `go test ./pkg/ai/llm/...` | OpenAI-compatible adapter 的正常响应、非流式 tool call、流式 tool call、错误映射、stream、cancel 均通过 mock/httptest 验证。 |
| P0-B | T035-T039 | `go test ./pkg/ai/prompt/...` | prompt 模板可加载、缺失变量失败、hash 稳定、路径穿越被拒绝。 |
| P0-C | T040-T043 | `go test ./pkg/ai/obs/...` | trace 字段完整，普通输出不泄露原始 query、完整 prompt 或 tool 参数。 |
| P0-D | T044-T050 | `go test ./pkg/ai/eval/...` | JSON dataset、确定性 metrics、runner/report 和错误样例处理可回归。 |
| P0-E | T051-T055 | `make test`、`make test-race`、`make vet`、`make eval-smoke` | 本地形成 `prompt -> llm -> obs -> eval` 闭环，默认不需要真实 API key。 |

如果某个阶段的包尚未存在，说明对应任务尚未执行；不要为了让命令“看起来完整”提前创建空实现。

## 验证 1：规格和计划完整性

运行：

```bash
find specs/001-agent-framework-spec -maxdepth 2 -type f ! -name '.DS_Store' | sort
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
specs/001-agent-framework-spec/tasks.md
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
- 流式工具调用分片聚合与结构化输出。
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
make test
make test-race
make vet
make eval-smoke
```

当前最终验证输出示例来自 2026-06-30 的真实本地命令：

```bash
$ make eval-smoke
go run ./cmd/eval-smoke
{
  "datasetVersion": "p0-smoke-local",
  "sampleCount": 3,
  "scores": {
    "context_hit": 1,
    "exact_match": 1
  }
}
```

当前 `Makefile` 中的命令定义为：

```makefile
test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

eval-smoke:
	go run ./cmd/eval-smoke
```

`make eval-smoke` 默认读取 `internal/eval/golden/p0_smoke.json` 和
`resource/prompt/p0_smoke/v1.tmpl`，通过 `internal/eval/smoke` 组合层完成：

```text
golden dataset -> prompt 渲染 -> fake LLM -> trace recorder -> eval runner -> JSON report
```

期望输出形态：

```text
go run ./cmd/eval-smoke
{
  "datasetVersion": "p0-smoke-local",
  "sampleCount": 3,
  "scores": {
    "context_hit": 1,
    "exact_match": 1
  }
}
```

期望结果：

- 默认命令不要求真实 API key。
- `eval-smoke` 输出稳定 JSON 报告，`sampleCount` 与 golden case 数量一致。
- `context_hit` 和 `exact_match` 当前应为 `1`，证明 fake LLM 输出、上下文回传和 eval runner 都已串通。
- 每条样例都会产生 trace 记录；普通 trace 只保存 query hash、prompt hash、token、模型和状态等摘要字段，不保存原始 query 或完整 prompt。
- 任何失败都能定位到测试、评估或静态检查。

## 验证 8：后端可替换契约

US5 完成后运行：

```bash
go test ./pkg/ai/llm -run TestProviderAdaptersAreReplaceable -count=1
go test ./pkg/ai/vectordb -run TestMemoryStoreContract -count=1
go test ./pkg/ai/obs -run TestLoggerTracerContract -count=1
go test ./pkg/ai/eval -run TestJSONDatasetContract -count=1
go test ./pkg/ai/cache -run TestMemoryFallbackCacheContract -count=1
```

期望结果：

- fake provider 与 OpenAI-compatible adapter 对上层暴露一致的 Chat、tool call、stream、错误分类和取消语义。
- memory vector store、logger tracer、JSON dataset、memory fallback cache 均通过同一套契约测试。
- 后续 pgvector、Milvus、LangFuse、OTEL、Redis 或评估平台 adapter 接入时，必须复用这些契约测试，而不是只写 adapter 私有测试。

## 可选真实服务 smoke

真实模型服务验证只能作为可选步骤。它应满足：

- 需要显式环境变量。
- 不进入默认本地门禁。
- 失败不会阻塞离线测试。
- 输出必须标记为 smoke 而不是确定性评估。
