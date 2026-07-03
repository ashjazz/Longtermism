# 实施计划：双平面观测与评估体系 v1

**规格目录**：`specs/002-dual-plane-observability`
**规格文件**：`specs/002-dual-plane-observability/spec.md`
**计划文件**：`specs/002-dual-plane-observability/plan.md`
**分支**：`002-dual-plane-observability`
**日期**：2026-07-03

## 概要

本计划把 ADR-0007 和当前规格转化为可执行的技术建设路线。目标是在不污染 `pkg/ai` 核心抽象的前提下，建立 Observability v1 的最小生产观测切片：一次请求可以关联基础服务链路、AI 语义 trace、LLM generation、检索、工具/外部工具服务、Agent step、失败/降级状态和评估证据。

计划同时把“边做边学”作为一等产出。每个主要切片都要配套学习目标、概念笔记、工程实验、最佳实践总结和复盘问题，使本阶段不仅交付观测能力，也系统学习系统可观测性、分布式追踪、AI Agent 可观测性、评估体系和平台接入取舍。

## 技术上下文

**项目类型**：Go 后端应用 + 可独立测试的 AI Agent 框架内核。
**主要语言/运行环境**：Go 1.25；GoFrame v2 应用层；`pkg/ai` 作为框架内核保持与应用层和平台 SDK 解耦。
**当前状态**：已有 `pkg/ai/obs.Trace`、`obs.Tracer`、日志型 tracer、tracer contract、privacy test、failure trace helper、eval runner、基础 RAG、Agent executor、resilience、ratelimit 和 fallback cache。缺口是：请求级关联身份、OTel 初始化与上下文传播、AI trace 到 span/observation 的字段映射、Langfuse/OTLP 真实 smoke、eval-to-trace 回链和系统化学习资产。
**关键依赖**：GoFrame contrib trace、OpenTelemetry Go、Langfuse OTLP/OpenTelemetry 接入、本地日志/内存 recorder、现有 eval runner。真实平台依赖必须 opt-in，不进入默认门禁。
**存储/外部服务**：v1 不新增持久化存储；默认离线验证使用 in-memory/logger sink。真实 OTel collector 或 Langfuse endpoint 只用于显式 smoke。原始 prompt/query/tool args 留存不在 v1 范围内。
**测试策略**：测试先行。默认覆盖 contract/privacy/unit/smoke；所有默认测试不得依赖真实外部平台、API key 或付费服务。真实平台 smoke 单独命令和环境变量控制。
**约束**：核心 AI 内核不得依赖 OTel/Langfuse SDK 类型；普通观测记录不得泄露原始敏感内容；上报失败不得影响主流程；字段映射必须可回归；每个主要切片必须有学习目标和学习资产。
**未知项**：无未解决澄清项。平台细节通过 adapter 和 opt-in smoke 验证，不阻塞 v1 设计。

## 宪章检查

### 初始检查

- **评估先行**：本计划将 eval-to-trace 回链、默认离线 smoke 和 contract/privacy 测试列为核心验收，满足。
- **核心抽象独立**：`pkg/ai/obs.Trace` 与 `obs.Tracer` 仍是核心边界；OTel/Langfuse 仅作为 adapter 或应用层装配，满足。
- **生产失效可见**：计划覆盖 success/failure/terminated/degraded、上报失败降级、循环/预算/检索/tool 错误等状态，满足。
- **轻量 Harness 优先**：不引入第三方 Agent 编排框架；只为现有 lightweight harness 增加观测证据，满足。
- **学习可沉淀**：每个主要切片要求学习目标、概念笔记、工程实验、最佳实践和复盘问题，满足。

### 设计后复查

- `research.md` 已收敛 OTel、GoFrame tracing、Langfuse OTLP、关联身份、隐私边界、离线验证和学习资产形态等决策。
- `data-model.md` 已定义请求观测链路、基础链路记录、AI 语义记录、关联身份、安全摘要、评估证据和学习资产。
- `contracts/observability-v1-contract.md` 已定义字段、隐私、关联、失败、eval 和学习资产契约。
- `quickstart.md` 已定义默认离线验证和真实平台 opt-in smoke 的期望路径。
- 未发现违反宪章的设计项；真实外部平台仍不进入默认门禁。

## 项目结构

### 文档产物

```text
specs/002-dual-plane-observability/
  spec.md
  plan.md
  research.md
  data-model.md
  quickstart.md
  contracts/
    observability-v1-contract.md
  checklists/
    requirements.md
```

### 代码区域

```text
pkg/ai/obs/              AI trace、tracer contract、OTel/Langfuse adapter、关联字段与隐私测试
pkg/ai/eval/             eval report 与 trace 关联、评估证据字段
pkg/ai/agent/            Agent step/tool 调用观测点
pkg/ai/rag/              retrieval summary 观测点
pkg/ai/resilience/       failure/degraded outcome 与上报失败降级
internal/cmd/            应用启动时观测初始化和 shutdown
internal/eval/smoke/     默认离线观测 smoke 与 eval-to-trace 验证
cmd/eval-smoke/          本地命令入口扩展
docs/journal/            学习记录、实验复盘和失败案例
docs/adr/                需要新增或修订的架构决策
docs/observability/      观测理论、最佳实践和学习资产
manifest/config/         opt-in 平台配置示例
```

## 阶段计划

### Phase 0：调研与决策

调研产物见 `research.md`。本阶段必须回答：

1. 如何在 GoFrame 应用层接入基础链路观测，同时保持 `pkg/ai` 核心解耦。
2. `obs.Trace` 如何映射为 OTel span/event/attribute 和 Langfuse observation。
3. 请求级关联身份如何贯穿服务入口、AI trace、评估报告和真实平台记录。
4. 哪些字段允许传播，哪些只能本地记录，哪些必须禁止进入普通 trace。
5. 默认离线验证与真实平台 opt-in smoke 如何分离。
6. 学习资产如何组织，确保理论学习、工程实验、最佳实践和复盘沉淀可追溯。

### Phase 1：设计与契约

设计产物包括：

- `data-model.md`：定义请求观测链路、基础链路记录、AI 语义记录、关联身份、安全摘要、评估证据和学习资产。
- `contracts/observability-v1-contract.md`：定义关联、字段、隐私、失败、eval 和学习资产契约。
- `quickstart.md`：定义离线验证、隐私验证、eval-to-trace 回链验证、上报失败验证和真实平台 opt-in smoke。
- `AGENTS.md`：更新当前 spec-kit 指针，确保后续会话恢复到本规格路线。

### Phase 2：任务拆分预告

后续 `/speckit-tasks` 应按垂直切片拆分：

1. **关联身份与上下文传播**：测试先行定义 request/session/ai trace 关联语义，再实现 helper 与 context 装配。
2. **基础链路平面装配**：接入 GoFrame 应用启动、配置、shutdown 和本地 exporter/fake sink，默认测试不访问真实平台。
3. **AI 语义映射契约**：扩展 tracer contract，定义 generation/retriever/tool/agent/evaluator 类型映射和隐私测试。
4. **OTel adapter**：实现 `obs.Trace` 到 OTel span/event/attribute 的 adapter，验证字段、顺序、隐私和上报失败降级。
5. **Langfuse/OTLP smoke**：建立 opt-in 命令和配置示例，验证真实平台能看到最小生产观测切片。
6. **eval-to-trace 回链**：让 eval report 记录 sample/run/metric 与 AI trace 关联，默认离线 smoke 覆盖至少 7 类 outcome。
7. **学习资产**：为每个主要切片新增理论笔记、工程实验记录、最佳实践总结和复盘问题。
8. **文档收口**：必要时补 ADR、quickstart、journal 和 ROADMAP 同步。

## 风险与缓解

- **风险：平台 SDK 类型污染核心包**。缓解：OTel/Langfuse 只在 adapter 或应用层装配；核心 contract 继续围绕 `obs.Trace`。
- **风险：普通 trace 泄露敏感内容**。缓解：隐私 contract 测试覆盖 query、prompt、tool args、token 和 PII；原文留存另行 ADR。
- **风险：真实平台不可用导致默认门禁不稳定**。缓解：默认测试只用 fake/in-memory/logger sink；真实平台 smoke 显式 opt-in。
- **风险：trace id、request id、session id、observation id 关系混乱**。缓解：先实现关联身份契约，再实现平台 adapter。
- **风险：学习资产流于形式**。缓解：每个主要切片必须有“理论概念 -> 工程实验 -> 最佳实践 -> 复盘问题”链路，并纳入任务验收。
- **风险：v1 范围膨胀到完整 LLMOps**。缓解：prompt registry、annotation queue、user feedback、judge calibration、CI experiment gate 均标记为后续阶段。
