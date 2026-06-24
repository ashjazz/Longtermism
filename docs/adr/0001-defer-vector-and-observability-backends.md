# ADR-0001：暂缓向量库与可观测平台最终选型

**日期**：2026-06-22
**状态**：deferred
**决策者**：JazzAsh、Codex

## Context（背景）

项目正在从 AI Agent 框架地基开始建设，P0 的首要目标是打通 `prompt -> llm -> obs -> eval -> local gate` 的最小工程闭环。向量数据库候选包括 pgvector 与 Milvus，可观测和评估平台候选包括本地日志、OpenTelemetry 与 LangFuse。Milvus 和 LangFuse 都具备较强的可用性、可扩展性和开源属性，但当前阶段如果直接绑定具体后端，会让核心抽象被基础设施形态过早牵引。

## Decision（决策）

暂缓 pgvector/Milvus 与日志/OTEL/LangFuse 的最终选型。P0 先固化 `vectordb.Store`、`obs.Tracer`、`eval.Dataset`、`eval.Runner` 等核心契约，并通过 fake、in-memory、本地日志和 JSON 数据集完成默认离线验证；真实后端后续只能以 adapter 或 app-layer integration 方式接入。

## Alternatives Considered（备选方案）

### 方案 1：立即采用 Milvus + LangFuse

- **优点**：更贴近生产级 RAG 与 LLMOps 场景，便于深入建设向量检索和评估观测能力。
- **缺点**：引入额外部署、运维、网络、鉴权和数据模型复杂度，P0 本地门禁会变慢且更不稳定。
- **未采用原因**：P0 的关键风险不是后端能力不足，而是核心抽象、评估、trace 和默认门禁尚未稳定。

### 方案 2：立即采用 pgvector + 本地日志

- **优点**：部署简单，能复用 PostgreSQL，适合早期单体应用和低运维成本场景。
- **缺点**：可能不足以覆盖后续高规模向量检索、索引调优和 LLMOps 平台化要求。
- **未采用原因**：它适合作为强候选 adapter，但不应在当前阶段成为核心契约默认形态。

### 方案 3：只保留抽象和本地 fake/in-memory 实现

- **优点**：默认测试稳定、低成本、可重复，能优先验证核心能力边界。
- **缺点**：真实后端协议、性能和运维问题会被推迟暴露。
- **采用原因**：最符合 P0 的目标；真实服务 smoke 可在抽象稳定后作为显式 opt-in 补充。

## Consequences（影响）

### 正面影响

- 核心框架不会被单一向量库或观测平台的数据模型锁定。
- 默认测试和 eval smoke 不依赖真实外部服务，更适合本地学习、CI 和回归。
- 后续可以在同一契约下比较 pgvector、Milvus、本地内存向量库、日志 tracer、OTEL adapter 和 LangFuse adapter。

### 负面影响

- P0 阶段不会直接证明 Milvus、pgvector、LangFuse 或 OTEL 的真实集成质量。
- 后续引入真实后端时仍需补集成测试、部署文档、迁移策略和运行成本评估。

### 风险

- **风险**：抽象设计过薄，后续真实 adapter 需要反复改契约。
  **缓解**：为 `vectordb.Store`、`obs.Tracer`、`eval.Dataset/Runner` 建立契约测试，并在引入真实 adapter 前补 ADR。
- **风险**：长期停留在 fake/in-memory 实现，错过真实生产约束。
  **缓解**：在完成 P0 最小闭环后，按 ROADMAP 推进 RAG 与可观测平台 adapter 的真实 smoke。

## Revisit Conditions（重新审视条件）

满足以下任一条件时重新打开本决策：

- P0 最小闭环已完成，且 `prompt -> llm -> obs -> eval -> local gate` 默认门禁稳定。
- RAG 阶段需要真实向量索引能力，并已明确数据规模、过滤条件、召回指标和部署约束。
- 可观测阶段需要跨会话 trace、在线评估、人工标注或 LLM-as-Judge 数据闭环。
- 出现明确的生产部署目标，需要在运维复杂度、成本、性能和平台能力之间做最终取舍。
