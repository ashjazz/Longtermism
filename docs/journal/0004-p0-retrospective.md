# P0 阶段复盘：从地基到可替换边界

**日期**：2026-06-30
**关联任务**：T001-T094
**关联模块**：pkg/ai/llm、pkg/ai/prompt、pkg/ai/obs、pkg/ai/eval、pkg/ai/rag、pkg/ai/agent、pkg/ai/resilience、pkg/ai/ratelimit、pkg/ai/cache、pkg/ai/vectordb
**状态**：已复盘

## 发生了什么

P0 从最初的项目骨架和 spec-kit 文档，推进到一个可以本地验证的 AI Agent 框架地基。当前已经形成几条可复用的工程路径：

```text
prompt -> llm -> obs -> eval -> local gate
```

以及后续替换真实后端时必须遵守的契约路径：

```text
fake/in-memory/local -> provider/vector store/tracer/dataset/cache adapter -> 同一套 contract tests
```

这意味着后续接入 pgvector、Milvus、LangFuse、OTEL、Redis 或更多 LLM provider 时，默认不需要重写 RAG、Agent、eval 或 resilience 上层能力；只要新的 adapter 通过对应契约测试，就能证明用户可见语义没有漂移。

P0 最终门禁已通过：

```bash
make test
make test-race
make vet
make eval-smoke
```

`make eval-smoke` 的真实输出为：

```text
go run ./cmd/eval-smoke
{
  "datasetVersion": "p0-smoke-local",
  "sampleCount": 3,
  "scores": {
    "context_hit": 1,
    "exact_match": 1
  }
}
```

## 根因

P0 反复验证了一个判断：生产级 AI Agent 框架的困难不在于“调用一次模型 API”，而在于每条能力都必须可测试、可评估、可观测、可降级、可替换。

如果只完成 provider、prompt、RAG 或 Agent 的 happy path，框架很快会遇到这些问题：

- 上游出错时不知道该重试、熔断还是快速失败。
- 模型输出变化时没有 golden case 证明是否退化。
- trace 记录了过多原文，带来 prompt、tool 参数或用户输入泄露风险。
- fake/in-memory 实现和真实 adapter 语义不一致，导致本地测试通过但上线后行为漂移。
- 会话上下文容易被新消息淹没，后续执行者无法从静态文件恢复项目状态。

## 修复

本阶段通过五类动作收束这些风险。

1. **模型与 provider 契约**
   - OpenAI-compatible adapter 支持 Chat、ChatStream、tool call、streaming tool call、usage、错误分类和 context cancel。
   - `TestProviderAdaptersAreReplaceable` 确认 fake provider 与 OpenAI-compatible adapter 对上层暴露一致语义。

2. **Prompt、Trace 与 Eval 闭环**
   - Prompt as Code 使用文件模板、版本和 hash。
   - 日志型 tracer 只记录 hash、长度、token、latency、cost 和状态，不记录原始敏感内容。
   - JSON dataset、确定性 metrics 和 runner 形成默认离线 eval smoke。

3. **RAG 与 Agent 能力地基**
   - recursive chunker、memory vector store、retriever 和 retrieval metrics 已建立初始检索路径。
   - tool registry 与 native tool calling executor 已覆盖 max steps、loop detected、step timeout 和 token budget。

4. **生产式故障诊断**
   - 断路器、provider wrapper、失败 trace、限流器和 fallback cache 让失败具备分类与降级语义。
   - 连续 refill token bucket 替换离散窗口式补充，避免测试实现训练出固定窗口突刺的错误直觉。

5. **后端可替换边界**
   - vectordb、obs、eval dataset、cache、provider 都有契约测试。
   - ADR-0005 和 ADR-0006 记录向量库与可观测平台 adapter 边界。
   - `core-framework-contract.md` 明确“替换后不改变用户可见能力预期”。

## 学到什么

下面三条是可以沉淀为后续面试或设计复盘的“出过事 -> 修好了 -> 学到了”故事。

### 故事 1：评估不能绕过真实工程路径

- **出过事**：P0 smoke 初版可以直接用 fake predict 回放 golden answer，让 eval 分数通过，但这只能证明 eval runner 可运行，不能证明 prompt、provider、trace 和 eval 被串起来。
- **修好了**：`internal/eval/smoke` 改为真实组合路径：加载 golden dataset、渲染 prompt、调用 fake LLM provider、记录 trace，再交给 eval runner 评分。
- **学到了**：AI 能力的评估必须覆盖真实工程链路。否则“评估通过”可能只是测试绕过了最容易出问题的部分。

### 故事 2：限流补 token 方式会改变系统直觉

- **出过事**：内存限流器初版按离散周期补充 token，容易在窗口边界形成短时间突刺，表现更像固定窗口限流。
- **修好了**：refill 调整为按 elapsed time 连续补充，`tokens >= 1` 才允许通过，更接近 token bucket 语义。
- **学到了**：本地实现虽然不是最终分布式实现，但它会塑造开发者对生产行为的直觉。fake/in-memory 也要尊重真实语义。

### 故事 3：adapter 不可替换会让后续选型变贵

- **出过事**：向量库和观测平台一开始就在 pgvector/Milvus、LangFuse/OTEL 之间摇摆。如果过早绑定某个 SDK，上层 RAG、Agent、eval 都可能被平台模型污染。
- **修好了**：先稳定 `vectordb.Store`、`obs.Tracer`、`eval.Dataset`、`cache.FallbackCache`、`llm.Provider` 契约，再用契约测试约束 fake、memory、本地日志和 OpenAI-compatible adapter。
- **学到了**：暂缓选型不是拖延，而是在抽象稳定前避免错误耦合。真正的选型应建立在可替换契约、真实 smoke、评估指标和运维约束之上。

## 后续预防

- 增加或调整测试：下一阶段新增真实 adapter 时，必须先复用对应 `Run*Contract` 或替换契约测试。
- 增加或调整评估 case：RAG 和 Agent golden dataset 需要继续扩展真实失败、工具错误、自我纠错和检索未命中样例。
- 增加或调整 trace 字段：后续可补 `fallback_used`、`retry_count`、`breaker_state`，但普通 trace 仍不得记录原始 query、完整 prompt 或 tool args。
- 增加或调整降级路径：当前多数组件已独立可测，下一阶段应把限流、熔断、fallback cache、provider failover 串成统一调用链。
- 需要补充的 ADR / ROADMAP / tasks：真实 pgvector/Milvus、LangFuse/OTEL、Redis cache 或生产模型路由落地时，需要分别补 adapter ADR、部署 smoke 和成本/延迟评估。
