# 实施计划：生产级 AI Agent 框架

**规格目录**：`specs/001-agent-framework-spec`  
**规格文件**：`specs/001-agent-framework-spec/spec.md`  
**计划文件**：`specs/001-agent-framework-spec/plan.md`  
**分支**：`001-agent-framework-spec`  
**日期**：2026-06-22

## 概要

本计划把项目级规格、`docs/ROADMAP.md` 和 `准备清单.md` 转化为可执行的技术建设路线。项目目标不是先做一个完整终端应用，而是先建立一个可运行、可观测、可评估、可降级的 AI Agent 框架地基，再逐步扩展 RAG、Agent Harness、缓存、容错、限流、性能和工程化门禁。

计划重点是将 P0 从“接口骨架”推进到“最小 AI 工程闭环”：提示词资产准备、模型交互、追踪记录、评估报告和本地质量门禁。后续 P1-P5 继续按 ROADMAP 推进，但所有能力都必须继承同一条完成标准：测试先行、评估可回归、故障可诊断、降级可说明、决策可追溯。

## 技术上下文

**项目类型**：Go 后端应用 + 可独立测试的 AI 框架内核。  
**主要语言/运行环境**：Go 1.24+；应用层使用 GoFrame，AI 内核位于 `pkg/ai` 并保持与应用框架解耦。  
**当前状态**：GoFrame 应用骨架与 `pkg/ai` 核心接口骨架已存在；`llm`、`prompt`、`obs`、`eval`、`rag`、`agent`、`vectordb`、`cache`、`resilience`、`ratelimit` 目前主要是契约层，真实实现和本地门禁尚未完成。  
**关键依赖**：P0 默认不强制依赖实时外部服务；真实模型供应商、向量数据库、可观测平台和评估平台都通过 adapter 接入。  
**存储/外部服务**：向量数据库候选包括 pgvector 与 Milvus，暂缓最终决策；可观测/评估平台候选包括本地日志/OTEL 与 LangFuse，暂缓最终决策。  
**测试策略**：P0 使用 fake、in-memory 和 httptest 优先，默认测试不依赖真实 API key；真实服务 smoke test 后续用环境变量或独立命令控制。  
**约束**：核心抽象不得被单一供应商 API 绑定；普通 trace 不保存原始敏感内容；4xx 参数/认证错误不得进入可重试上游错误路径；AI 能力不得只凭 demo 标记完成。  
**未知项**：无未解决澄清项。后端选型是有意暂缓的架构决策，不阻塞 P0。

## 宪章检查

### 初始检查

- **评估先行**：通过 P0-D 和 P0-E 把 evaluation runner、golden case 和本地门禁提前，满足。
- **核心抽象独立**：计划以 `pkg/ai` 接口为核心，真实后端通过 adapter 接入，满足。
- **生产失效可见**：P0-A 明确错误分类，P0-C 建立 trace，P1 后续接 resilience，满足。
- **轻量 Harness 优先**：P3 规划为自建 native tool calling executor，不引入第三方编排框架作为核心，满足。
- **学习可沉淀**：计划要求 ADR 和 journal，满足。

### 设计后复查

- `research.md` 已将 P0 后端选型、错误边界、评估策略和可观测策略决策化。
- `data-model.md` 已提炼项目规格中的核心数据对象与验证规则。
- `contracts/` 已定义核心框架契约、P0 provider 契约和评估/观测契约。
- `quickstart.md` 已定义可运行的验证路径，并保持默认验证不依赖实时外部服务。
- 未发现违反宪章的设计项。

## 项目结构

### 文档产物

```text
specs/001-agent-framework-spec/
  spec.md
  plan.md
  research.md
  data-model.md
  quickstart.md
  contracts/
    core-framework-contract.md
    p0-provider-contract.md
    evaluation-observability-contract.md
```

### 代码区域

```text
pkg/ai/llm/          P0-A 模型抽象与首个 OpenAI-compatible provider
pkg/ai/prompt/       P0-B 文件模板 registry 与渲染 hash
pkg/ai/obs/          P0-C 日志型 Tracer 与 trace 记录
pkg/ai/eval/         P0-D Dataset、Metric、Runner、Report
internal/eval/       P0-D/P0-E 本地 golden case 与 eval smoke 入口
resource/prompt/     P0-B 本地 prompt 模板
docs/adr/            重大选型和暂缓决策
docs/journal/        真实开发问题、修复和学习记录
```

## 阶段计划

### Phase 0：调研与决策

本阶段已完成并记录在 `research.md`。核心决策如下：

- P0 的真实模型 adapter 采用 OpenAI-compatible 形态，但核心接口不绑定任一供应商。
- 默认测试不依赖真实外部服务，真实服务验证作为可选 smoke。
- pgvector/Milvus 与 LangFuse/OTEL 均暂缓最终选择，先保持抽象隔离。
- 评估系统先做确定性 runner 与小型 golden case，再扩展 LLM-as-Judge 和平台同步。

### Phase 1：设计与契约

本阶段已完成以下设计产物：

- `data-model.md`：定义项目规格、能力阶段、AI 能力、评估样例、追踪记录、提示词资产、模型供应商、降级策略、决策记录等核心实体。
- `contracts/core-framework-contract.md`：定义所有 AI 能力必须遵守的横向完成契约。
- `contracts/p0-provider-contract.md`：定义 P0-A OpenAI-compatible provider 的输入、输出、错误、流式和测试契约。
- `contracts/evaluation-observability-contract.md`：定义 P0-D/P0-C 的评估报告和 trace 契约。
- `quickstart.md`：定义后续验证 P0 最小闭环的本地操作路径。

### Phase 2：任务拆分预告

后续 `/speckit-tasks` 应按以下垂直切片拆分任务：

1. P0-A RED：为 provider 正常响应、非流式 tool call、流式 tool call、错误映射、流式文本、ctx cancel 编写失败测试。
2. P0-A GREEN：实现 OpenAI-compatible provider 最小版本；普通 streaming 与 streaming tool call 可分两步完成，后者必须在公共 provider 契约接入前完成。
3. P0-B：实现文件模板 registry、渲染、hash、路径安全测试。
4. P0-C：实现日志型 Tracer 和敏感内容边界测试。
5. P0-D：实现 JSON dataset、确定性 metrics、runner、report。
6. P0-E：补本地门禁命令和 eval smoke。
7. 文档收口：补 ADR、journal、更新 ROADMAP 进度。

## 风险与缓解

- **风险：过早绑定供应商协议**。缓解：所有外部服务只通过 adapter 进入核心抽象，独有字段不得泄漏到通用契约。
- **风险：P0 范围膨胀**。缓解：P0 只打通最小闭环，不做 failover、复杂路由、语义缓存和完整 RAG。
- **风险：测试依赖真实服务导致开发变慢**。缓解：默认测试使用 fake/httptest，真实服务仅作为可选 smoke。
- **风险：可观测性记录泄露敏感内容**。缓解：普通 trace 只记录 hash、长度、类别、模型、用量和状态，不保存原始 prompt/tool 参数。
- **风险：评估流于形式**。缓解：每项能力至少包含可重复 case，质量门禁必须能报告分数和回归。
- **风险：文档与实现分叉**。缓解：完成每个子阶段时同步更新 ROADMAP、ADR 或 journal，并让 AGENTS 指向当前计划。
