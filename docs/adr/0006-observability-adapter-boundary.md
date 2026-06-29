# ADR-0006：可观测平台 Adapter 边界

**日期**：2026-06-29
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

P0 已经建立 `pkg/ai/obs.Tracer` 抽象、本地日志型 `Logger` 实现和 Tracer 契约测试。项目后续会评估 LangFuse、OpenTelemetry 和本地日志等观测后端，但 AI Agent 框架核心不能被任何一个平台的 run/span/event 模型绑定。当前需要明确：核心可观测契约只描述业务侧需要长期稳定追踪的 AI trace 字段，平台 SDK、采样、上报、批处理、标注和 UI 能力都属于 adapter 或装配层。

## Decision（决策）

`pkg/ai/obs.Tracer` 是 AI 内核唯一依赖的可观测边界，核心契约只暴露 `Record(ctx, Trace)` 与稳定 `Trace` 字段。LangFuse、OpenTelemetry 和本地日志都不得进入 `pkg/ai` 核心契约；它们只能作为 adapter 把 `Trace` 映射到各自后端，并必须复用 `RunTracerContract` 证明关键字段、记录顺序、隐私边界和防御性拷贝语义不变。

普通 trace 只记录 hash、长度、模型、token、latency、cost、retrieval summary、outcome 和 feedback 等诊断字段，不记录原始 query、完整 prompt 或 tool 参数。若后续需要敏感原文留存，必须走独立的加密审计链路，而不是扩展普通 Tracer 契约。

## Boundary（边界）

### 核心契约负责

- 定义业务侧稳定 AI trace 字段：trace id、租户/用户/会话、feature、prompt 版本/hash、token、latency、成本、检索摘要、结果状态和反馈。
- 保持 `Record(ctx, Trace)` 的非阻塞心智模型：观测失败不得让 AI 请求主流程失败。
- 保持普通 trace 的隐私边界：不从 `context.Context` 读取或序列化任意敏感值，不记录原始 query、完整 prompt 或 tool args。
- 提供契约测试，让本地日志、LangFuse adapter、OTEL adapter 在同一套用户可见语义下替换。

### Adapter 负责

- 把 `Trace` 映射到具体平台：JSON Lines、LangFuse trace/run/generation、OTEL span/event/attribute 或其它后端。
- 处理平台 SDK 初始化、认证、批量上报、采样、缓冲、flush、重试、限流和降级。
- 在平台字段能力不足时做可解释映射，不得要求核心 `Trace` 改成平台专属结构。
- 将上报失败转化为内部告警或本地降级，不得把平台不可用扩散为用户请求失败。

### 上层不得依赖

- LangFuse 的 trace/run/generation id、dataset、score、prompt registry 或 SDK 类型。
- OpenTelemetry 的 span、attribute key、resource、exporter、sampler 或 collector 配置。
- 本地日志的 JSON Lines 字段顺序、writer、锁实现或输出目标。

## Alternatives Considered（备选方案）

### 方案 1：直接采用 LangFuse 作为核心可观测模型

- **优点**：LLMOps 能力强，天然支持 prompt、trace、dataset、score 和人工评估闭环，适合后续评估平台化。
- **缺点**：核心代码会被 LangFuse 的 trace/run/generation 数据模型牵引，默认本地测试也更容易依赖外部服务或平台 SDK。
- **未采用原因**：LangFuse 是重要候选 adapter，但不能成为 `pkg/ai/obs` 的核心类型来源；否则未来接入 OTEL 或本地离线门禁会被平台模型反向约束。

### 方案 2：直接采用 OpenTelemetry 作为核心可观测模型

- **优点**：标准化程度高，适合接入基础设施 tracing、metrics、logs 与 collector 生态。
- **缺点**：OTEL 的 span/attribute 语义偏通用基础设施观测，不能天然表达 prompt 版本、token 成本、retrieval score、eval feedback 等 AI 专属字段的业务含义。
- **未采用原因**：OTEL 适合作为生产 tracing adapter，但核心契约仍应用 AI 语义建模，再由 adapter 映射到 span/event/attribute。

### 方案 3：只保留本地日志作为长期实现

- **优点**：实现简单、默认离线、可测试、便于 P0 smoke 和教学复盘。
- **缺点**：缺少跨服务链路、平台检索、在线标注、反馈闭环、告警和仪表盘能力。
- **未采用原因**：本地日志只适合作为 P0 默认实现和降级 sink，不能替代后续 LangFuse/OTEL 等真实观测后端。

### 方案 4：稳定 Tracer 契约，LangFuse/OTEL/本地日志都作为 adapter

- **优点**：核心 AI trace 字段稳定，默认门禁保持离线；后续可按场景同时接入 LLMOps 平台和基础设施 tracing。
- **缺点**：adapter 需要维护字段映射，部分平台高级能力不能直接穿透到核心。
- **采用原因**：最符合 ADR-0001 的暂缓平台选型策略，也能保持 ADR-0004 要求的 prompt、token、tool 参数、cost、trace 和降级行为可见。

## Implementation Notes（实施约束）

- 新增 LangFuse 或 OTEL adapter 时，必须新增对应的 `TestXxxTracerContract`，复用 `RunTracerContract`。
- Adapter 可以维护平台专属配置和初始化代码，但不能要求 `pkg/ai` 或 `pkg/ai/agent` 引入平台 SDK 类型。
- 普通 trace schema 不得新增 `query`、`raw_query`、`prompt`、`prompt_content`、`tool_args`、`tool_arguments` 等可承载敏感原文的字段。
- 上报失败默认不影响主流程；生产 adapter 可以记录内部错误指标或降级到本地日志 sink。
- 平台上的人工评分、LLM-as-Judge、dataset 关联等能力应由 eval/app-layer adapter 显式接入，不能隐式改变 `Tracer.Record` 语义。

## Consequences（影响）

### 正面影响

- AI 内核只依赖稳定 `Trace` 语义，不被 LangFuse、OTEL 或本地日志实现锁定。
- 可观测字段可以被本地测试、生产平台和后续评估报告复用。
- 普通 trace 隐私边界明确，降低 prompt、tool 参数、用户输入和认证信息进入日志平台的风险。

### 负面影响

- LangFuse 和 OTEL 的高级能力需要 adapter 额外映射，短期不会直接出现在核心接口中。
- 同时维护 LLMOps 平台和基础设施 tracing 时，需要处理 trace id、span id、run id 的关联策略。

### 风险

- **风险**：核心 `Trace` 字段过多，逐渐变成平台无关的大而全 DTO。
  **缓解**：只收敛跨后端都需要的 AI 诊断字段；平台特有字段留在 adapter。
- **风险**：平台 adapter 为了方便排障偷偷记录原始 prompt 或 tool args。
  **缓解**：契约测试与 privacy test 禁止普通 trace 输出敏感字段；敏感原文留存必须另写 ADR 和加密审计设计。
- **风险**：观测上报失败被静默忽略后难以及时发现。
  **缓解**：核心允许不影响主流程，但生产 adapter 必须补充内部错误指标、告警或本地 fallback sink。

## Revisit Conditions（重新审视条件）

满足以下任一条件时重新审视本决策：

- 首个 LangFuse 或 OTEL adapter 落地后，发现现有 `Trace` 无法表达必要 AI 诊断语义。
- 需要把 eval dataset、人工标注、LLM-as-Judge 分数与在线 trace 做强关联。
- 进入生产部署阶段，需要统一 trace id、request id、span id、LangFuse run id 与日志平台字段。
- 合规或审计要求保存原始 prompt、tool 参数或用户输入，需要设计独立加密审计链路。
