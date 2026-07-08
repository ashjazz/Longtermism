# AI Agent 可观测性

**关联任务**：T074
**关联规格**：`specs/002-dual-plane-observability/spec.md`
**状态**：drafted

## 理论概念

AI Agent 可观测性不是把一次 LLM 请求当成一个黑盒延迟数字，而是把 Agent 的语义阶段拆成可诊断、可评估、可回放的 observation。Observability v1 约定的稳定类型包括：

- **generation**：模型生成阶段。重点观察 model、prompt identity、input/output/reasoning/cache tokens、TTFT、total latency、cost、finish/outcome 和 provider 状态。
- **retriever**：RAG 检索阶段。重点观察 query hash/length、chunks retrieved、top scores、retrieval latency、retrieval miss 和 query rewrite hash。
- **tool**：工具调用阶段。重点观察 tool name、tool call id、status、latency、错误分类和参数摘要/hash。普通观测不记录完整 tool args。
- **agent**：Agent 编排阶段。重点观察 step index、termination reason、loop detected、budget exceeded、max steps、step timeout 和最终 outcome。
- **evaluator**：评估阶段。重点观察 dataset/sample/metric/score 以及它与请求和 AI trace 的回链关系。

AI Agent 的核心诊断问题通常不是“模型有没有返回”，而是“为什么这个请求走到了这个结果”：检索没命中、工具失败、重复调用同一个工具、token budget 提前耗尽、provider 报错、降级路径生效，都会导致完全不同的修复方向。

## 关键问题

- 这个主题解决什么生产问题？
  它解决“用户只看到答案错误、超时或失败，但工程师不知道失败发生在 generation、retriever、tool、agent loop 还是 eval 回归”的问题。

- 它在传统后端观测和 AI Agent 观测中有什么差异？
  传统后端通常关注接口、DB、cache 和外部 API；AI Agent 还要解释语义阶段、token/cost、prompt/model 身份、工具轨迹、循环终止和评估证据。仅有 HTTP 200 或 500 无法说明 Agent 是否“答得好”。

- 哪些信息必须可见，哪些信息必须隐藏？
  必须可见的是 `observation_type`、`ai_trace_id`、`request_id`、`parent_span_id`、model、prompt version/hash、token usage、latency、cost、retrieval count/top score、tool name/call id、step index、termination reason、loop/budget 状态和 outcome。必须隐藏的是原始 query、完整 prompt、完整 tool args、用户隐私、密钥、token 和外部响应原文。

## 工程实验

本项目已经把 AI 语义观测拆到多个小切片中，每个切片都可以离线验证：

1. `pkg/ai/obs/observation_type.go` 固定 `generation/retriever/tool/agent/evaluator` 词表，缺失或未知类型会被 mapper/contract 测试暴露。
2. `pkg/ai/obs/obs.go` 和 `trace_helpers.go` 定义 AI trace 字段和不可变 helper，统一承载 token、latency、cost、retrieval、safe summary、agent step、loop/budget 和 outcome。
3. `pkg/ai/rag/retriever.go` 在检索完成后记录 `retriever` observation：命中数量、top scores、latency、retrieval miss 和 query hash/length。
4. `pkg/ai/agent/executor.go` 在 native tool calling loop 中记录 `agent` observation：step index、tool call id、tool name、termination reason、loop detected 和 budget exceeded。
5. `internal/eval/smoke/observability_chain.go` 组合 success、upstream failure、retrieval miss、tool error、loop detected、budget exceeded 和 degraded 七类场景，验证完整请求链路里 AI observation 能与基础设施 span 和 eval evidence 关联。

可运行的验证命令：

```bash
go test ./pkg/ai/obs -run 'TestValidateObservationType|TestMapTraceToSpanSnapshot|TestLoggerTracerContract' -count=1
go test ./pkg/ai/rag -run TestBasicRetrieverRecordsRetrieval -count=1
go test ./pkg/ai/agent -run TestExecutorRecords -count=1
go test ./internal/eval/smoke -run TestObservabilityChainSmoke -count=1
```

观察结果应满足：

- 每条 AI 语义记录都有明确 `observation_type`，不能由 adapter 猜默认值。
- retriever observation 能区分命中和 retrieval miss。
- Agent observation 能记录工具步骤、循环检测和预算耗尽。
- token、latency、cost 等诊断字段进入普通 trace，但原始 prompt/query/tool args 不进入普通 trace。
- 完整 chain smoke 能在一次请求中串起基础设施阶段、AI observation 和 eval evidence。

## 最佳实践

本项目在 AI Agent 可观测性上采用以下实践：

- **observation type 显式化**：generation、retriever、tool、agent、evaluator 是不同语义阶段，不能为了测试兼容给缺失类型默认成 agent。
- **低敏摘要优先**：query/prompt/tool 参数用 hash、length、count、score、status、error class 表达，不记录原文。
- **token/cost 一等公民**：input/output/reasoning/cache tokens 和 cost 不是附属字段，它们直接影响模型路由、预算控制和性能评估。
- **loop 和 budget 是终止事实**：`loop_detected`、`budget_exceeded`、`termination_reason` 应作为稳定诊断字段，而不是只写在错误字符串里。
- **Agent step 记录工具轨迹，不记录工具参数原文**：tool name、tool call id、状态和摘要足以支持大部分排障；完整参数需要另行审计链路。
- **观测失败保护主流程**：Tracer/exporter 失败不能让业务请求失败，失败状态应进入诊断字段。

暂不采用的做法：

- 不解析旧式 ReAct 文本 action 来推断工具调用；Agent harness 只消费 provider 返回的结构化 tool call。
- 不把 Langfuse 或 OTel SDK 类型放进 `pkg/ai` 核心 trace。
- 不用单个“大 trace 字符串”替代结构化字段；否则 token、cost、loop、budget 和 eval 都无法稳定评估。

## 失败模式

AI Agent 可观测性常见失败方式包括：

- **generation 黑盒化**：只知道模型调用失败，不知道模型、prompt version、tokens、TTFT、latency 和 provider 状态。
- **retriever miss 不可见**：答案差但 trace 里没有 chunks retrieved、top score 或 retrieval miss，无法判断是检索问题还是生成问题。
- **tool error 被吞掉**：工具调用失败只表现为最终回答差，缺少 tool name/call id/status/error class。
- **loop 只表现为超时**：重复 tool call 没有被记录为 `loop_detected`，排障时只看到请求耗时长。
- **budget 超限没有上下文**：token budget exceeded 只返回终止结果，缺少当前 step、usage 和 termination reason。
- **成本无法归因**：没有按请求记录 token/cost，无法判断某个 feature、tenant 或 prompt 版本是否异常消耗。
- **语义阶段和基础 span 断裂**：AI observation 缺少 `request_id/service_trace_id/span_id`，导致无法从 HTTP 请求跳到 AI 诊断。

## 降级路径

AI Agent 可观测性的降级策略应遵循“保留事实，不伪造细节”：

1. **缺少 tracer**：业务可以继续运行，但不会产生 AI 语义记录；测试应覆盖有 tracer 的路径。
2. **缺少关联身份**：retriever/agent 可以跳过观测写入，不应编造 `ai_trace_id` 或 `request_id`。
3. **provider 失败**：业务返回 provider error；已有阶段应尽量记录 failure outcome 和稳定错误分类。
4. **retrieval miss**：记录 `retrieval_miss`，让上层决定停止、降级或继续生成。
5. **loop/budget 终止**：返回受控终止结果，并记录 `loop_detected` 或 `budget_exceeded`，避免表现成未知超时。
6. **exporter 失败**：通过 `RecordWithExportFailureProtection` 保护主流程，记录 telemetry export failure，不覆盖业务事实。

降级不能做的事是把未知结果默认为 success、把缺失 observation type 默认为 agent、或者为了排障把原始 prompt/tool args 写入普通 trace。

## 复盘问题

- 这次实现证明了哪个理论概念？
  AI Agent 的观测对象是语义阶段和决策轨迹，不只是一次 HTTP 请求或一次模型调用。generation、retriever、tool、agent 和 evaluator 必须能分开定位。

- 哪个字段或边界最容易被误用？
  `ObservationType` 和 `TerminationReason` 最容易被当成展示字段，实际它们是 eval、mapper、平台 adapter 和回归分析都会依赖的事实字段。

- 如果线上出问题，应该先看哪条记录？
  先从 `request_id` 找完整 chain，再按 `observation_type` 定位失败阶段：retrieval miss 看 retriever，tool error 看 tool/agent step，loop/budget 看 agent，答案质量下降看 evaluator evidence。

- 后续阶段需要补什么能力？
  需要补齐真实 generation provider wrapper 的 observation、tool observation 的独立类型记录、cost 计算 adapter，以及 Langfuse/OTel 平台上的 observation 展示 smoke。

## 关联任务 / 测试

- 任务：T011-T016、T036、T040-T041、T048-T060、T074
- 测试：
  - `pkg/ai/obs/observation_type_test.go`
  - `pkg/ai/obs/otel_mapper_test.go`
  - `pkg/ai/obs/tracer_contract_test.go`
  - `pkg/ai/rag/retriever_observation_test.go`
  - `pkg/ai/agent/executor_observation_test.go`
  - `internal/eval/smoke/observability_chain_test.go`
  - `internal/eval/smoke/agent_golden_test.go`
- ADR：
  - `docs/adr/0006-observability-adapter-boundary.md`
  - `docs/adr/0007-dual-plane-observability-evaluation-v1.md`
- Journal：
  - `docs/journal/0005-observation-type-defaulting.md`
  - `docs/journal/0006-dataset-identity-domain-modeling.md`
