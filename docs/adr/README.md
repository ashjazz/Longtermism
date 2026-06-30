# ADR 索引

本文档用于索引项目中的架构决策记录（Architecture Decision Record, ADR）。ADR 记录的是对项目结构、核心抽象、后端选型、质量门禁和生产化取舍有长期影响的判断。

当前 spec-kit 实施计划：[`specs/001-agent-framework-spec/plan.md`](../../specs/001-agent-framework-spec/plan.md)

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

## 维护规则

新增或修改 ADR 时，必须同步更新本索引，至少包含 ADR 编号、标题、状态和一句话说明。状态变化也应保留历史语义，避免只改结论而丢失当时的工程背景。

## 索引审查记录

- 2026-06-30：T090 收口审查确认当前新增 ADR `0001`-`0006` 均已出现在索引中；状态覆盖 `deferred` 与 `accepted`，暂无 `proposed` 或 `superseded` 条目。
