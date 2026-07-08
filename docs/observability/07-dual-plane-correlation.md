# 双平面关联

**关联任务**：T078
**关联规格**：`specs/002-dual-plane-observability/spec.md`
**状态**：drafted

## 理论概念

双平面观测的核心问题是：一次用户请求既有传统服务事实，也有 AI 语义事实，还可能产生评估证据。三者如果各自记录、各自查询，就会形成三套孤立数据。

Observability v1 将它们拆成三个层次：

- **基础设施平面**：HTTP/service/db/cache/external call 等传统服务 span。它回答“请求从服务角度发生了什么”，核心身份是 `request_id`、`service_trace_id`、`span_id`。
- **AI 语义平面**：generation、retriever、tool、agent、evaluator 等 AI observation。它回答“Agent 从语义角度做了什么”，核心身份是 `ai_trace_id`、`observation_id`，并通过 `parent_span_id` 指回基础设施 span。
- **评估证据层**：dataset/sample/metric/score 和 regression status。它回答“这次输出是否通过评估”，核心身份是 `eval_run_id + sample_id`，并通过 `request_id + ai_trace_id` 回链到请求和 AI 阶段。

关联层的职责不是记录更多业务内容，而是维护稳定身份和 parent/link 规则。它应该让工程师能从任意入口进入同一条事实链：

- 从 `request_id` 找服务入口、AI 阶段和 eval evidence。
- 从 `ai_trace_id` 回到 `service_trace_id/span_id`。
- 从 `eval_run_id + sample_id` 回到请求和 AI trace。

## 关键问题

- 这个主题解决什么生产问题？
  它解决“HTTP trace、AI trace 和 eval report 都存在，但无法证明它们属于同一次请求，也无法从失败样例回到具体请求链路”的问题。

- 它在传统后端观测和 AI Agent 观测中有什么差异？
  传统后端通常只需要 trace/span 的父子关系；AI Agent 还需要把不一定同步发生的 generation、retriever、tool、agent step 和离线/在线 eval evidence 关联起来。

- 哪些信息必须可见，哪些信息必须隐藏？
  必须可见的是 `request_id`、`service_trace_id`、`span_id`、`parent_span_id`、`ai_trace_id`、`observation_id`、`eval_run_id`、`sample_id` 和 outcome/failure status。必须隐藏的是请求体、query 原文、完整 prompt、完整 tool args、密钥、token 和外部响应原文。

## 工程实验

本项目用三组实现证明双平面关联：

1. `pkg/ai/obs/dual_plane_link.go` 定义最小 parent/link 规则：
   - `DualPlaneHTTPParentLink` 表达基础设施父 span。
   - `DualPlaneAIChildLink` 表达 AI observation 对基础 span 的子关联。
   - `DualPlaneEvalLink` 表达 eval evidence 到请求、基础 span 和 AI trace 的回链。
   - 缺少关键身份时 fail fast，不自动编造链路。
2. `pkg/ai/obs/chain_recorder.go` 定义 `RequestObservationChain`，按 `request_id` 组织 service stages、AI observations、eval evidence、stage refs 和最终 outcome。
3. `internal/eval/smoke/observability_chain.go` 构造默认离线完整链路，覆盖 success、upstream failure、retrieval miss、tool error、loop detected、budget exceeded 和 degraded 七类结果。
4. `pkg/ai/obs/baggage_policy.go` 只允许低敏关联身份进入 baggage，支持跨服务传播，但不传播敏感 payload。

可运行的验证命令：

```bash
go test ./pkg/ai/obs -run 'TestBuildDualPlaneLinks|TestRequestObservationChainRecorder|TestBaggageFieldsFromCorrelationIdentity' -count=1
go test ./internal/eval/smoke -run TestObservabilityChainSmoke -count=1
```

观察结果应满足：

- `BuildDualPlaneLinks` 在缺少 request/service/span/AI/eval/sample 身份时失败。
- AI observation 的 `ParentSpanID` 指向基础设施 root/current span。
- eval evidence 包含 `EvalRunID`、`SampleID`、`RequestID` 和 `AITraceID`。
- `RequestObservationChain` 能按 `request_id` 回查完整事实链。
- baggage 只传播 allowlist 中的低敏关联字段。

## 最佳实践

本项目在双平面关联上采用以下实践：

- **request_id 作为查询入口**：它不是平台 trace id，而是用户请求级事实链入口。
- **service_trace_id/span_id 保持基础设施语义**：它们用于定位 HTTP/service 等传统 span，不应被 AI trace id 覆盖。
- **ai_trace_id 保持 AI 语义语义**：它用于串联 generation、retriever、tool、agent、evaluator，不应取代 service trace。
- **parent_span_id 显式指回基础 span**：AI observation 不靠平台自动推断父子关系，而是携带明确父 span 身份。
- **eval evidence 通过 link 回到请求**：score 必须能定位 sample 和 trace，否则无法成为回归证据。
- **baggage 只传身份，不传内容**：跨服务传播只允许低敏关联字段。
- **缺关键身份 fail fast**：缺身份时拒绝构造完整 link，不生成假链路。

暂不采用的做法：

- 不把 OTel trace id、业务 request id 和 AI trace id 合并成一个字段。
- 不让 Langfuse observation id 成为核心唯一身份。
- 不把完整请求、prompt 或 tool args 放进 link/baggage 来“方便回查”。

## 失败模式

双平面关联常见失败方式包括：

- **孤立基础 span**：HTTP/service span 有记录，但没有 AI trace link，无法解释 AI 阶段。
- **孤立 AI observation**：AI 语义记录有 `ai_trace_id`，但缺 `request_id` 或 `parent_span_id`，无法回到用户请求。
- **孤立 eval evidence**：评估有 sample/score，但缺 `request_id` 或 `ai_trace_id`，无法定位产生输出的 trace。
- **身份字段混淆**：把 `service_trace_id`、`span_id`、`ai_trace_id`、`observation_id` 混用，导致查询入口不稳定。
- **错误父子关系**：AI observation 挂到错误 parent span，排障时看起来像另一次请求的问题。
- **baggage 过宽**：为了跨服务回查，把敏感原文放进 baggage，造成传播面泄露。
- **最终 outcome 缺解释**：链路状态为 failure/terminated/degraded，但没有 outcome explanation，无法复盘。

## 降级路径

双平面关联的降级策略是：

1. **非关键阶段缺失**：例如没有 tool observation，但 request、AI trace 和 eval evidence 仍可回查，可以记录部分链路。
2. **关键身份缺失**：缺 `request_id`、`service_trace_id`、`span_id` 或 `ai_trace_id` 时，不构造完整 link。
3. **eval link 缺失**：报告 missing sample/field，让 eval smoke 或 CI 暴露问题。
4. **baggage 拒绝传播**：本地可以继续记录低敏 chain，但跨服务传播不携带该字段。
5. **真实平台不可用**：保留本地 `RequestObservationChain` 或 smoke 结果，平台上报失败不影响业务。

降级不能做的事是自动编造 trace id、把未知 observation 挂到 root span，或者把敏感原文加入 baggage 来补偿链路缺失。

## 复盘问题

- 这次实现证明了哪个理论概念？
  双平面不是两套独立观测，而是基础设施事实、AI 语义事实和 eval 证据之间的可回查关联。

- 哪个字段或边界最容易被误用？
  `trace_id` 这个概念最容易混淆。项目中应始终区分 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id` 和 `observation_id`。

- 如果线上出问题，应该先看哪条记录？
  先用 `request_id` 查 `RequestObservationChain`，确认 service stage、AI observations、eval evidence 和 outcome；如果是 eval 失败，再用 `eval_run_id + sample_id` 回链。

- 后续阶段需要补什么能力？
  需要在真实 OTel/Langfuse opt-in smoke 中验证这些 parent/link 字段映射到平台后仍然可查，并补充跨服务 baggage 传播集成验证。

## 关联任务 / 测试

- 任务：T034-T047、T078
- 测试：
  - `pkg/ai/obs/dual_plane_link_test.go`
  - `pkg/ai/obs/chain_recorder_test.go`
  - `pkg/ai/obs/baggage_policy_test.go`
  - `internal/eval/smoke/observability_chain_test.go`
  - `internal/eval/smoke/eval_trace_link_test.go`
- ADR：
  - `docs/adr/0007-dual-plane-observability-evaluation-v1.md`
- Journal：
  - `docs/journal/0005-observation-type-defaulting.md`
  - `docs/journal/0007-observability-privacy-boundary.md`
