# 系统可观测性基础

**关联任务**：T072
**关联规格**：`specs/002-dual-plane-observability/spec.md`
**状态**：drafted

## 理论概念

可观测性不是“多打日志”，而是让系统在未知故障发生时，仍然能通过外部输出还原内部状态。传统后端系统通常从 logs、metrics、traces 三类信号开始：

- **Logs**：离散事件记录，回答“发生了什么”。日志适合保存请求开始/结束、配置选择、降级原因、错误分类和关键业务决策。它的优势是上下文丰富，缺点是难以直接表达全局趋势。
- **Metrics**：聚合后的数值指标，回答“系统现在有多健康”。指标适合表达 QPS、错误率、P50/P95/P99 延迟、队列长度、token 消耗和 exporter 失败次数。它的优势是便于告警和看趋势，缺点是单条请求的细节会被聚合掉。
- **Traces**：一次请求经过多个组件的调用链，回答“请求走过哪里、每一步花了多久”。trace 适合分析跨服务、跨 adapter、跨 AI 阶段的延迟和失败传播。它的优势是能还原链路，缺点是如果字段设计混乱，就会变成很多无法关联的孤立 span。

这三类信号不是互相替代的关系。一个生产故障通常需要三者配合：metrics 先发现 SLO 破坏，traces 定位慢在哪个阶段，logs 解释为什么进入了某个失败或降级分支。

**SLO（Service Level Objective）** 是系统对用户体验的可量化承诺，例如“99% 请求在 2 秒内完成”或“5xx 错误率低于 1%”。SLO 的价值不只是告警阈值，而是帮团队判断哪些观测信号值得优先建设。没有 SLO，观测容易变成“什么都记录”；有了 SLO，观测会围绕延迟、错误率、可用性和用户可感知失败来组织。

在 AI Agent 框架里，SLO 还会扩展到 AI 语义层，例如生成首 token 延迟、检索命中率、工具调用失败率、预算耗尽率、降级成功率和评估回归率。但 T072 关注基础设施平面：HTTP/service 入口、配置、provider/exporter 生命周期、平台上报失败保护，以及这些事实如何与 AI 语义平面关联。

## 关键问题

- 这个主题解决什么生产问题？
  它解决“用户请求失败或变慢后，工程师无法判断问题发生在 HTTP 服务、配置、外部平台上报、AI 调用还是评估链路”的问题。

- 它在传统后端观测和 AI Agent 观测中有什么差异？
  传统后端更关注请求、数据库、缓存、外部 API 和服务资源；AI Agent 还需要解释 generation、retrieval、tool、loop、token budget 和 eval evidence。Observability v1 因此采用双平面：基础设施平面负责通用服务事实，AI 语义平面负责 Agent 专属事实。

- 哪些信息必须可见，哪些信息必须隐藏？
  必须可见的是 `request_id`、`service_trace_id`、`span_id`、method、path、status、duration、outcome、failure status 和 exporter/lifecycle 状态。必须隐藏的是请求体、query string 原文、完整 prompt、完整 tool args、密钥、token、个人隐私和外部响应原文。

## 工程实验

本项目的基础设施平面先以离线 smoke 的方式落地，而不是一开始依赖真实 OTel collector 或 Langfuse 平台。这样可以把核心契约变成默认可运行测试：

1. `internal/eval/smoke/infrastructure_span.go` 构造 HTTP/service span 快照，记录 method、path、status、duration、`request_id`、`service_trace_id`、`span_id` 和 outcome。
2. `internal/eval/smoke/infrastructure_export_failure.go` 验证 exporter 或 collector 不可用时，业务结果不能被观测上报失败覆盖；失败状态必须保留为可诊断信息。
3. `internal/cmd/observability_bootstrap.go` 将 enabled/mode/signals 归一到 noop、local 或 collector 装配边界；应用只认识 Collector，默认路径不访问网络。
4. `internal/cmd/observability_lifecycle.go` 管理基础设施 tracer provider/exporter 的初始化与关闭，并记录失败状态和错误消息。
5. `pkg/ai/obs/correlation.go` 提供 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id` 等关联身份，为后续双平面链路打地基。

可运行的验证命令：

```bash
go test ./internal/eval/smoke -run 'TestInfrastructureSpan|TestInfrastructureExportFailure' -count=1
go test ./internal/cmd -run 'TestBuildObservabilityBootstrap|TestObservabilityProviderLifecycle|TestBuildObservabilityResource' -count=1
go test ./pkg/ai/obs -run 'TestCorrelationIdentity|TestBaggage' -count=1
```

观察结果应满足：

- 给定 `request_id` 可以定位基础设施入口记录。
- 基础设施 span 只携带低敏诊断字段。
- 默认配置不访问真实平台。
- exporter 失败不会让业务请求失败。
- 失败状态和错误消息不会被静默吞掉。

## 最佳实践

本项目在基础设施平面采用以下实践：

- **先离线可验证，再接真实平台**：默认测试必须不依赖 collector、Langfuse endpoint 或真实 API key。真实平台 smoke 必须显式 opt-in。
- **配置解析即安全边界**：collector mode 缺 endpoint、协议或超时时必须启动失败；不得静默退回 no-op。
- **上报失败不影响主流程**：observability exporter 是诊断能力，不应成为业务可用性的单点故障。失败必须可记录，但不能覆盖业务结果。
- **基础设施字段低敏化**：HTTP path、method、status、duration、trace/span identity 可以进入普通观测；请求体、query 原文、header token 和外部响应原文不能进入普通观测。
- **字段语义显式**：`request_id` 是请求查询入口，`service_trace_id` 是基础设施 trace 身份，`span_id` 是基础设施 span 身份。不要用一个字符串字段同时表达多个身份。

暂不采用的做法：

- 不把真实 OTel SDK 类型暴露给 `pkg/ai` 核心接口。
- 不让 Langfuse 承担 HTTP、DB、cache、GoFrame middleware 等基础设施 tracing 的全部职责。
- 不为了平台 UI 可读性把原始请求、prompt 或工具参数写入普通 trace。

## 失败模式

基础设施观测平面常见失败方式包括：

- **链路缺身份**：没有稳定 `request_id` 或 `service_trace_id`，导致用户请求和后续 AI trace 无法关联。
- **span 太粗**：只记录“请求失败”，没有 method、path、status、duration、错误分类，无法定位是入口、外部调用还是 exporter 问题。
- **metrics 有告警但 trace 不可查**：错误率上升后无法找到具体请求样例，排障只能靠猜测。
- **logs 有原文泄露**：为了调试方便记录请求体、query、header、prompt 或外部响应，形成隐私风险。
- **exporter 失败拖垮业务**：collector 不可用、网络失败或 shutdown 异常被直接返回给业务层。
- **平台绑定过早**：核心代码依赖某个 SDK 类型，后续切换平台或离线测试变困难。

## 降级路径

基础设施平面的降级策略分为三层：

1. **No-op mode**：观测未启用时不创建 provider 或网络 exporter，保证默认路径安全。
2. **Local sink / in-memory smoke**：开发和 CI 中保留可断言的本地记录，证明字段、顺序、失败状态和隐私边界正确。
3. **Collector exporter failure protection**：Collector 不可用时，业务流程继续返回业务结果，同时记录 `telemetry_export_failed` 等可诊断状态。

降级的边界是：可以降低上报能力，不能伪造业务事实；可以跳过外部平台，不能丢掉本地诊断证据；可以隐藏敏感原文，不能隐藏失败分类。

## 复盘问题

- 这次实现证明了哪个理论概念？
  可观测性三类信号必须围绕用户请求和 SLO 组织；基础设施平面先保证请求入口、配置、生命周期和上报失败可诊断。

- 哪个字段或边界最容易被误用？
  `service_trace_id`、`span_id` 和 `ai_trace_id` 容易混用；baggage 也容易被误当成普通 metadata，实际它是跨进程传播面。

- 如果线上出问题，应该先看哪条记录？
  先用 `request_id` 查基础设施入口 span，确认 method/path/status/duration/outcome；再看是否存在 exporter failure；最后通过关联身份进入 AI 语义记录。

- 后续阶段需要补什么能力？
  需要把基础设施 span、AI observation 和 eval evidence 在学习资产中继续串起来，并在平台接入阶段验证真实 OTel collector/Langfuse opt-in smoke。

## 关联任务 / 测试

- 任务：T023-T033、T065-T071、T072
- 测试：
  - `internal/eval/smoke/infrastructure_span_test.go`
  - `internal/eval/smoke/infrastructure_export_failure_test.go`
  - `internal/cmd/observability_bootstrap_test.go`
  - `internal/cmd/observability_lifecycle_test.go`
  - `internal/cmd/observability_resource_test.go`
  - `pkg/ai/obs/baggage_policy_test.go`
- ADR：
  - `docs/adr/0006-observability-adapter-boundary.md`
  - `docs/adr/0007-dual-plane-observability-evaluation-v1.md`
- Journal：
  - `docs/journal/0007-observability-privacy-boundary.md`
