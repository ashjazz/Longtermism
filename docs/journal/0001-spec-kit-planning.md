# Spec-kit 规划沉淀：为什么先做 P0 最小闭环

**日期**：2026-06-23
**关联任务**：T020
**关联模块**：specs/001-agent-framework-spec、docs/ROADMAP.md、docs/adr、docs/journal
**状态**：已复盘

## 发生了什么

本轮将项目从较粗的 `docs/ROADMAP.md` 和学习手册 `准备清单.md`，标准化为 spec-kit 规格体系：

- `spec.md`：定义项目使命、用户故事、功能需求、成功标准和范围边界。
- `plan.md`：把规格落成技术路线，明确 P0 到 P5 的阶段计划、约束、风险和项目结构。
- `tasks.md`：把计划拆成可逐项执行的任务，要求每个任务具备路径、质量门控和可验证结果。
- `quickstart.md`：为新会话提供恢复上下文和 P0 验证清单。
- `docs/adr/`：记录架构决策，包括暂缓 pgvector/Milvus、LangFuse/OTEL 最终选型，以及 P0 错误分类和离线验证策略。
- `docs/journal/`：用于沉淀真实失败、修复和工程判断。

这次规划的核心结果是：项目不先追求完整 RAG、完整 Agent 或完整后端集成，而是先建设 P0 最小 AI 工程闭环。

## 根因

如果一开始直接做 RAG、Agent Harness、缓存、向量库或可观测平台接入，很容易出现三个问题：

1. **能力无法证明有效**：没有 eval runner 和 golden case 时，只能凭 demo 体验判断“好像能用”，无法识别回归。
2. **故障无法诊断**：没有 trace 和错误分类时，上游超时、429、5xx、4xx 参数错误、prompt 版本变化都会混在一起。
3. **选型过早锁死**：如果 P0 就绑定 Milvus、pgvector、LangFuse 或某个供应商 SDK，后续很难保持核心框架可替换。

`准备清单.md` 里的重要信号是“30% 模型化 + 70% 工程化”。工程化不是先堆能力，而是先建立能证明能力、诊断失败、控制边界的地基。

## 修复

本轮通过 spec-kit 把建设顺序收敛为以下路径：

```text
规格统一
  -> 技术计划
  -> 任务拆解
  -> 共享测试工具与错误分类
  -> P0 最小闭环
  -> RAG / Agent / 容错 / 限流 / 缓存 / 后端适配
```

P0 最小闭环被定义为：

```text
prompt -> llm -> obs -> eval -> local gate
```

为什么这个闭环优先：

- `prompt` 让输入可版本化、可复现、可追踪。
- `llm` 让模型调用、tool call、流式输出、usage 和错误分类成为统一契约。
- `obs` 让每次关键交互有 trace，可以看见模型、prompt hash、token、latency、状态和失败原因。
- `eval` 让能力不是靠感觉验收，而是通过样例、指标和报告回归。
- `local gate` 让默认验证不依赖真实 API key、真实向量库或实时平台，保证日常开发稳定。

同时，本轮补充了任务状态维护规则：任务完成后必须按性质同步 `ROADMAP.md`、ADR、journal、quickstart、spec、plan 或 tasks，避免文档和实现漂移。

## 学到什么

生产级 AI Agent 框架的第一性问题不是“先接哪个模型”或“先选哪个向量库”，而是：

1. **能不能稳定复现一次能力运行。**
2. **能不能看见它为什么成功或失败。**
3. **能不能用评估证明它比之前更好或没有退化。**
4. **能不能在外部服务不可用时继续做本地验证。**

因此，P0 最小闭环不是“小 demo”，而是后续所有能力的工程地基。RAG、Agent、缓存、限流、熔断和平台观测都应该长在这个闭环之上。

另一个重要经验是：暂缓选型本身也是一种架构决策。Milvus、pgvector、LangFuse、OTEL 都可以是优秀候选，但在核心接口尚未稳定时，先写 adapter 边界和 ADR，比直接引入真实服务更稳。

## 后续预防

- 增加或调整测试：后续代码任务继续遵循 RED -> GREEN -> REFACTOR；默认测试优先 fake、in-memory、httptest。
- 增加或调整评估 case：P0-D 必须补 JSON dataset、确定性 metrics、runner/report；P0-E 必须让 `make eval-smoke` 默认离线可运行。
- 增加或调整 trace 字段：P0-C 必须验证 trace 字段完整，并确保普通日志不记录原始 query、完整 prompt 或 tool 参数。
- 增加或调整降级路径：P0 阶段先定义错误分类和离线验证边界；P1 后再接断路器、failover、exact/stale cache 和限流。
- 需要补充的 ADR / ROADMAP / tasks：
  - T022：补 ADR-0004，固化 lightweight harness first 原则。
  - T023-T055：按 P0-A 到 P0-E 执行最小闭环。
  - Final Phase：在 P0 完成后更新 ROADMAP、quickstart 和 retrospective journal。
