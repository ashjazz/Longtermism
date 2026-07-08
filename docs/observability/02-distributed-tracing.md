# 分布式追踪

**关联任务**：T073
**关联规格**：`specs/002-dual-plane-observability/spec.md`
**状态**：drafted

## 理论概念

分布式追踪关注一次请求在多个组件、进程或服务之间的流动。它的核心不是“每个函数都打点”，而是让跨边界调用仍然能被组织成一条可解释的因果链。

- **Trace**：一次端到端请求或任务的整体链路。它回答“这次用户请求从入口到完成经过了哪些阶段”。在基础设施平面里，trace 通常由 HTTP/server middleware 或上游网关注入；在 AI 语义平面里，`ai_trace_id` 表示一次 AI 行为链路。
- **Span**：trace 中的一个阶段或操作，例如 HTTP server request、数据库查询、检索、LLM generation、tool 调用。span 需要有开始/结束时间、状态和父子关系。
- **Context**：在调用链中传递取消、超时、trace/span 身份和本地关联值的载体。Go 里使用 `context.Context`，但它不应该变成任意业务字段或敏感 payload 的容器。
- **Baggage**：随上下文跨进程传播的 key/value。它比普通 span attribute 更危险，因为它会被下游服务、代理、collector 或日志系统继续携带。baggage 只能放低敏、稳定、必要的关联字段。

分布式追踪有两个容易混淆的边界：

1. **span attribute 不等于 baggage**。attribute 通常只属于当前 span；baggage 会继续传播给下游。
2. **context 不等于万能参数包**。context 可以携带请求生命周期和少量显式关联身份，但不应携带原始 query、prompt、tool args、API key 或外部响应。

本项目的双平面设计把传统基础设施 trace 和 AI 语义 trace 分开建模，再用显式身份关联它们。这样既能复用 OTel/GoFrame 的基础设施能力，又不会把 AI 领域语义压扁成通用 span 字段。

## 关键问题

- 这个主题解决什么生产问题？
  它解决“HTTP 请求、AI generation、retriever、tool、eval evidence 各自有记录，但无法证明它们属于同一次用户请求”的问题。

- 它在传统后端观测和 AI Agent 观测中有什么差异？
  传统后端追踪通常围绕 service span、RPC、DB、cache 和外部 API；AI Agent 还需要关联语义 observation、Agent step、评估证据和 prompt/model 身份。AI 阶段不一定天然在同一个基础设施调用栈中，因此必须显式维护 `ai_trace_id` 和 observation id。

- 哪些信息必须可见，哪些信息必须隐藏？
  必须可见的是 `request_id`、`service_trace_id`、`span_id`、`parent_span_id`、`ai_trace_id`、`observation_id`、`eval_run_id` 和 `sample_id`。必须隐藏的是原始 query、完整 prompt、完整 tool args、认证信息、密钥和外部响应原文，尤其不能进入 baggage。

## 工程实验

本项目用三个小对象承载分布式追踪的最小可验证模型：

1. `pkg/ai/obs/correlation.go` 定义 `CorrelationIdentity`。它集中保存 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id`、`session_id` 和 `eval_run_id`，避免各层从 context 或平台 payload 里临时猜字段。
2. `pkg/ai/obs/baggage_policy.go` 定义允许进入 baggage 的低敏字段。它采用 key allowlist 和 value 敏感扫描两层保护，确保跨进程传播面不携带敏感原文。
3. `pkg/ai/obs/dual_plane_link.go` 定义基础设施父 span、AI 子 observation 和 eval evidence 的关联快照。缺少关键身份时 fail fast，而不是生成看似完整但不可验证的链路。

可运行的验证命令：

```bash
go test ./pkg/ai/obs -run 'TestCorrelationIdentity|TestBaggageFieldsFromCorrelationIdentity|TestBuildDualPlaneLinks' -count=1
go test ./pkg/ai/obs -run 'TestRequestObservationChainRecorder|TestMapTraceToSpanSnapshot' -count=1
```

观察结果应满足：

- `CorrelationIdentity` 只能由显式 option 组装，不从任意 context 内容推断敏感字段。
- baggage 只包含 allowlist 中的低敏关联字段。
- 缺少 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id`、`eval_run_id` 等关键身份时，双平面 link 构建失败。
- AI observation 通过 `parent_span_id` 指回基础设施 span，而 eval evidence 通过 `request_id + ai_trace_id` 回链到请求和 AI 阶段。

## 最佳实践

本项目在分布式追踪上采用以下实践：

- **事实身份显式传入**：adapter 不从 context、payload 或平台返回值中猜 `trace_id`、`span_id` 或 `ai_trace_id`。
- **基础设施 trace 与 AI trace 分离**：`service_trace_id` 归基础设施平面，`ai_trace_id` 归 AI 语义平面；二者通过关联层闭环，而不是互相覆盖。
- **baggage 用 allowlist**：只有 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id`、`session_id`、`eval_run_id` 这类低敏身份字段可以传播。
- **缺字段 fail fast**：如果关键身份缺失，构造 link 或 chain 应返回错误，不应自动编造 ID。
- **span attribute 记录摘要**：普通 span attribute 只记录状态、类型、hash、长度、数量和错误分类；原文留存必须另行设计加密审计链路。

暂不采用的做法：

- 不把 OTel SDK span/context 类型暴露给 `pkg/ai` 核心接口。
- 不让 Langfuse observation id 取代本项目自己的 `ai_trace_id` 和 `request_id`。
- 不通过 baggage 传播 prompt、query、tool args、用户隐私或外部响应原文。

## 失败模式

分布式追踪常见失败方式包括：

- **trace 断裂**：HTTP span 有记录，但 AI observation 没有 `parent_span_id` 或 `service_trace_id`，导致无法从请求跳到 AI 阶段。
- **身份混淆**：把 OTel `trace_id`、业务 `request_id` 和 AI `trace_id` 当成同一个字段，后续平台或 eval 报告无法稳定回查。
- **baggage 污染**：为了方便排障，把 query、prompt、tool args 或 token 放入 baggage，造成跨服务泄露。
- **context 滥用**：把 context 当作通用 map，在下游 mapper 中序列化未知值，导致敏感信息进入普通观测。
- **孤立 eval evidence**：评估样例有分数，但缺少 `request_id` 或 `ai_trace_id`，无法定位产生输出的请求和 AI 阶段。
- **平台 id 反向污染核心**：让某个外部平台的 trace/observation id 成为核心模型唯一身份，导致替换平台或离线测试困难。

## 降级路径

分布式追踪的降级目标是“链路不完整时不要伪造完整链路”：

1. **缺少非关键身份**：例如没有 `session_id`，可以继续记录 request/span/AI trace，但不能声称具备 session 聚合能力。
2. **缺少关键身份**：例如缺少 `request_id`、`service_trace_id`、`span_id` 或 `ai_trace_id`，应 fail fast 或丢弃关联记录，并保留可诊断错误。
3. **baggage 构造失败**：如果字段不在 allowlist 或 value 像敏感内容，应拒绝传播；本地 span 仍可记录低敏失败分类。
4. **平台上报失败**：真实 collector 或 Langfuse 不可用时，保留本地 chain/smoke 证据，业务请求不应被观测失败覆盖。

降级不应该做的事是自动生成看似有效的 `ai_trace_id`、把未知 span 挂到错误父级，或者把敏感原文塞进 baggage 来“方便排障”。

## 复盘问题

- 这次实现证明了哪个理论概念？
  分布式追踪的关键在于稳定身份和传播边界，而不是单纯记录更多 span。双平面体系必须把基础设施父 span、AI observation 和 eval evidence 显式关联。

- 哪个字段或边界最容易被误用？
  `trace_id` 这个词最容易泛化误用。项目中应区分 `service_trace_id`、`ai_trace_id` 和平台 observation id；baggage 也最容易被误当成普通 metadata。

- 如果线上出问题，应该先看哪条记录？
  先用 `request_id` 找基础设施入口 span，再检查 `service_trace_id/span_id` 是否能指向 AI observation 的 `parent_span_id`，最后用 `ai_trace_id` 和 `eval_run_id/sample_id` 回查评估证据。

- 后续阶段需要补什么能力？
  需要在真实 OTel/Langfuse opt-in smoke 中验证这些身份映射到平台后仍能回查，并补充跨进程传播场景下 baggage allowlist 的集成测试。

## 关联任务 / 测试

- 任务：T007-T014、T034-T043、T073
- 测试：
  - `pkg/ai/obs/correlation_test.go`
  - `pkg/ai/obs/baggage_policy_test.go`
  - `pkg/ai/obs/baggage_privacy_test.go`
  - `pkg/ai/obs/dual_plane_link_test.go`
  - `pkg/ai/obs/chain_recorder_test.go`
  - `pkg/ai/obs/otel_mapper_test.go`
- ADR：
  - `docs/adr/0006-observability-adapter-boundary.md`
  - `docs/adr/0007-dual-plane-observability-evaluation-v1.md`
- Journal：
  - `docs/journal/0005-observation-type-defaulting.md`
  - `docs/journal/0007-observability-privacy-boundary.md`
