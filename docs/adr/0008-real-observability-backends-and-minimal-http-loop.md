# ADR-0008：真实可观测后端接入与最小 HTTP 观测闭环

**日期**：2026-07-10
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

ADR-0007 已确定双平面观测的领域边界，但尚未决定真实后端、Collector 拓扑、最小 HTTP 闭环和验收方式。项目需要在不让业务代码绑定具体平台的前提下，真实验证基础设施事实与 AI 语义事实能够被关联、查询、评估和恢复。

这项决策同时受三个约束：默认开发门禁必须离线且无付费依赖；真实后端必须具备生产化的队列、失败归因和隐私边界；框架仍应以最小 HTTP 闭环逐步生长，而不是提前实现完整 Agent 功能面。

## Decision（决策）

应用内采用 GoFrame 基础设施自动埋点、OpenTelemetry API/SDK 标准遥测层与 `pkg/ai/obs.Trace` AI 语义事实源的组合；新实现不引入 `opentracing-go`。应用只通过 OTLP 连接 OTel Collector，默认 gRPC，允许 HTTP/protobuf override；业务代码不得直连 Prometheus、Loki、Tempo、Grafana、SigNoz 或 Langfuse trace ingestion。

Collector 采用 `ingress -> forward connectors -> infra/AI downstream pipelines` 的方案 C。AI usecase 在 root/bridge span 设置 `longtermism.observability.plane="ai"`，AI semantic spans 同样携带该标记；纯基础设施请求不进入 AI pipeline。Tempo、Loki 与 Langfuse OTLP exporters 分别拥有 queue/retry/timeout 和 `file_storage` persistent queue；Prometheus 通过 scrape Collector exporter 获取指标，不属于 push queue。

基础设施观测主线采用 `Prometheus + Loki + Tempo + Grafana`，并提供 `observability-grafana` 本地 profile、provisioned dashboards 和 alert rules。`SigNoz` 作为实施优先级较后的备选基础设施 profile；它只替换 logs/metrics/traces 后端，Langfuse 始终保留为 AI trace/generation/score 后端。Langfuse score 由应用内有界异步 adapter 投影，本地 eval evidence 仍是事实源。

首个真实业务闭环是非流式 `POST /api/v1/chat`，接入服务端配置的 OpenAI-compatible GPT-5.5；另提供受 smoke 开关保护的 `GET /api/v1/observability/infra-smoke` 来验证纯基础设施路由。chat 始终返回 `request_id` 和 opaque `ai_trace_id`，仅 debug 返回有界低敏 `eval_summary`。

验证分层：`obs-smoke-offline` 保护默认离线契约；新增 `obs-platform-smoke` 以本地受控 sender 验证最小平台接入 payload、显式启用和隐私边界，不连接真实后端；`obs-grafana-e2e`、`obs-resilience-e2e` 与 `obs-signoz-e2e` 才验证真实后端接收、查询和恢复。真实平台验证必须采用唯一 marker、限定时间窗口并产生机器可读报告。

## Alternatives Considered（备选方案）

### 方案 1：应用直接对接各后端或平台 SDK

- **优点**：初期可快速看到各平台数据，平台特性调用直接。
- **缺点**：业务代码被 backend endpoint、SDK 类型和多条失败路径污染，切换后端与测试成本高。
- **未采用原因**：违反 `pkg/ai/obs` 作为事实源、Collector 作为出口边界的分层原则。

### 方案 2：只使用 SigNoz 作为统一后端

- **优点**：本地部署与 UI 更一体化，三信号接入路径较短。
- **缺点**：无法替代 Langfuse 的 generation、score 与后续评估工作流，也减少了对经典组件职责边界的学习。
- **未采用原因**：SigNoz 保留为受支持备选；Grafana 经典栈更适合作为当前主线，Langfuse 仍独立负责 AI 平面。

### 方案 3：单一 Collector pipeline 向所有后端导出

- **优点**：配置数量少，最容易启动。
- **缺点**：纯基础设施 span 会误入 Langfuse，两个平面的字段过滤、采样、队列和失败归因无法独立。
- **未采用原因**：选择 ingress 加两个 downstream pipelines，显式表达 infra/AI 路由边界。

### 方案 4：只保留默认离线 smoke 或只运行完整真实 E2E

- **优点**：前者稳定、后者最接近生产。
- **缺点**：前者不能验证平台接入契约，后者慢且受 Docker、凭据和付费服务影响。
- **未采用原因**：新增轻量 `obs-platform-smoke` 填补两者之间的验证层，但明确它不替代真实 E2E。

## Consequences（影响）

### 正面影响

- 应用核心保持对后端和 Langfuse tracing SDK 无感，平台替换集中在 Collector 与 adapter。
- 一次 chat 可以关联 HTTP、日志、指标、AI trace、generation、eval evidence 与 score；纯基础设施端点可反证不会误路由到 AI 平面。
- 本地轻量 smoke 可在无 Docker 和无凭据环境快速保护平台接入契约，真实 E2E 则保留严格的后端查询证明。
- queue、告警、retention、故障归因和清理要求在首次真实接入前已经成为可验收约束。

### 负面影响

- 需要维护两套 compose profile、Collector 配置、dashboard/checklist 和更细的失败证据。
- Collector fan-out 与 persistent queue 增加部署、磁盘容量和恢复测试复杂度。
- `obs-platform-smoke` 与真实 E2E 的边界必须持续清楚，否则容易出现本地 sender 通过却误判真实后端可用。

### 风险与缓解

- **高基数与隐私泄露**：request/run id 不进入 metrics labels；payload policy、baggage allowlist、应用内过滤与 Collector 二次过滤共同约束，生产禁止 `content_raw`。
- **观测故障影响业务**：exporter、score worker 和 queue 失败必须被单独指标记录，但不得改写 chat 或 infra-smoke 的业务结果。
- **积压或数据丢失**：Tempo/Loki/Langfuse 使用 persistent queue；验收覆盖 backend pause、Collector restart、queue 满、磁盘不可写和 shutdown timeout。语义是 at-least-once，不承诺 exactly-once。
- **真实平台门禁不稳定或产生费用**：默认 PR 只跑离线门禁；真实 E2E 在首个阶段验收及相关配置变更、release candidate 中显式运行。

## Revisit Conditions（重新审视条件）

- GoFrame contrib 无法表达必要的 OTel resource、采样、fan-out 或测试替身配置。
- Langfuse OTLP 不能稳定表达所需 generation 或 score 关联，或官方 Go SDK 成熟且不会污染核心契约。
- 单进程 score worker 的丢失窗口、吞吐或恢复需求要求演进为 outbox/外部 worker。
- 多服务部署、吞吐或隔离要求使单 Collector 或本地 persistent queue 不再适用。
- 需要支持流式 chat、tool/MCP、RAG、sub-agent 或上下文压缩时，新增行为不能在现有 trace/eval 语义内清晰表达。
- SigNoz 支持成熟度、Grafana stack 运维成本或后端能力变化改变主线与备选的取舍。

## References（参考）

- ADR-0006：可观测平台 Adapter 边界。
- ADR-0007：双平面观测与评估体系 v1。
- [真实可观测后端接入决策工作台](../observability/08-real-backend-decision-workbench.md)。
- [真实后端规格质量清单](../../specs/003-real-observability-backends/checklists/requirements.md)。
- [真实后端实施验收清单](../../specs/003-real-observability-backends/checklists/real-backend-acceptance.md)。
