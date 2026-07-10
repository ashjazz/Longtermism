# ADR 索引

本文档用于索引项目中的架构决策记录（Architecture Decision Record, ADR）。ADR 记录的是对项目结构、核心抽象、后端选型、质量门禁和生产化取舍有长期影响的判断。

当前 spec-kit 规格：[`specs/003-real-observability-backends/spec.md`](../../specs/003-real-observability-backends/spec.md)

## 状态说明

- `proposed`：已提出但尚未接受，仍需要验证、讨论或补充证据。
- `accepted`：已接受并作为后续实现与审查依据。
- `deferred`：有意暂缓决策，当前通过抽象、adapter 或最小实现保持可替换性。
- `superseded`：已被后续 ADR 取代，保留用于追溯历史原因。

## 当前索引

| ADR | 标题 | 状态 | 说明 |
| --- | --- | --- | --- |
| [0001](0001-defer-vector-and-observability-backends.md) | 暂缓向量库与可观测平台最终选型 | deferred | P0 先固化抽象边界，pgvector/Milvus 与日志/OTEL/LangFuse 后续作为 adapter 评估。 |
| [0002](0002-p0-error-classification.md) | P0 模型调用错误分类策略 | accepted | 429/5xx/timeout 进入 ErrUpstream 路径，4xx 快速失败且不触发重试/熔断。 |
| [0003](0003-p0-local-validation-without-live-services.md) | P0 默认离线验证策略 | accepted | 默认门禁使用 fake/in-memory/本地数据；真实服务 smoke 必须显式 opt-in。 |
| [0004](0004-lightweight-harness-first.md) | 优先自建 lightweight Agent Harness | accepted | 核心 Agent Harness 自建，第三方框架只能作为 adapter 或 app-layer integration。 |
| [0005](0005-vector-store-adapter-boundary.md) | 向量库 Adapter 边界 | accepted | `vectordb.Store` 是 RAG 上层唯一依赖，pgvector、Milvus、memory fake 都只能作为 adapter 实现同一契约。 |
| [0006](0006-observability-adapter-boundary.md) | 可观测平台 Adapter 边界 | accepted | `obs.Tracer` 是 AI 内核唯一依赖，LangFuse、OTEL、本地日志都只能作为 adapter 映射同一 Trace 契约。 |
| [0007](0007-dual-plane-observability-evaluation-v1.md) | 双平面观测与评估体系 v1 | accepted | OpenTelemetry 负责基础设施链路与上下文传播，Langfuse 负责 AI 语义观测与后续评估，二者通过 `obs.Trace` 和关联层串联。 |
| [0008](0008-real-observability-backends-and-minimal-http-loop.md) | 真实可观测后端接入与最小 HTTP 观测闭环 | accepted | 应用统一接入 Collector；Grafana 栈为基础设施主线、SigNoz 为备选、Langfuse 为 AI 平面，并通过分层 smoke/E2E 验收。 |

## 维护规则

新增或修改 ADR 时，必须同步更新本索引，至少包含 ADR 编号、标题、状态和一句话说明。状态变化也应保留历史语义，避免只改结论而丢失当时的工程背景。

## 索引审查记录

- 2026-06-30：T090 收口审查确认当前新增 ADR `0001`-`0006` 均已出现在索引中；状态覆盖 `deferred` 与 `accepted`，暂无 `proposed` 或 `superseded` 条目。
- 2026-07-03：新增 ADR `0007`，记录 Observability v1 双平面观测与评估体系决策。
- 2026-07-10：新增 ADR `0008`，记录真实可观测后端、最小 HTTP API 与分层验证契约。
