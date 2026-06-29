# US4 故障诊断演练：让失败能被分类、追踪和降级

**日期**：2026-06-29
**关联任务**：T069-T079
**关联模块**：pkg/ai/resilience、pkg/ai/obs、pkg/ai/ratelimit、pkg/ai/cache、pkg/ai/agent
**状态**：已复盘

## 发生了什么

US4 阶段围绕“生产式故障诊断”补齐了几类横切能力：断路器、Provider wrapper、失败 trace、内存限流器和 exact/stale fallback cache。它们不是为了让某个单点功能看起来更完整，而是为了让 AI Agent 框架在典型失败下能够回答三个问题：

1. 失败属于哪一类。
2. 是否应该重试、熔断、限流或降级。
3. trace、测试和日志能否解释发生了什么。

本轮重点演练了两条生产高频路径。

第一条是模拟上游失败。LLM provider 返回 `llm.ErrUpstream` 时，`ProviderWrapper` 会把错误交给 circuit breaker 计数；连续达到阈值后，后续调用快速失败为 `ErrCircuitOpen`，避免继续打爆已不可用的上游。与此同时，`obs.FailureStatus` 提供稳定状态词表，例如 `timeout`、`rate_limit`、`budget_exceeded`，后续 trace 和 eval/journal 可以用同一套字段描述失败。

第二条是模拟 Agent 循环。Native tool calling executor 已经能在重复工具调用或限制触达时返回稳定终止原因，例如 `loop_detected`、`step_timeout`、`budget_exceeded`。US4 进一步把这些终止原因提升为可观测状态，使 Agent 不再只是“运行失败”，而是能说明它是因为循环、超时还是预算耗尽而终止。

同时，限流和缓存降级补齐了上游故障周边的保护层：

- `MemoryLimiter` 用连续 refill 的 token bucket 模拟全局、用户和 provider 维度限流，避免固定窗口边界突刺。
- `MemoryFallbackCache` 用 scope + key 隔离缓存，命中时明确标记 `exact` 或 `stale`，避免把旧答案伪装成新鲜答案。

## 根因

这轮暴露的核心工程风险是：如果失败只以普通 error 字符串散落在各模块里，系统很难形成统一诊断。

例如，上游不可用、429 限流、请求超时、Agent 循环、检索未命中和预算耗尽都可能表现为“没有拿到最终答案”。如果没有稳定分类，上层 resilience 可能误判：

- 把 4xx 调用方错误当作上游故障，错误地触发重试或熔断。
- 把真实上游故障当作普通调用失败，导致没有 failover 或 fallback。
- 把 Agent 循环和普通工具错误混在一起，无法复盘 executor 的停止原因。
- 把 stale cache 返回值当成 exact 结果，误导调用方和评估报告。

另一个风险是测试里的本地实现如果过于简化，会把不适合生产的语义带进后续 adapter。最典型的是内存限流器初版采用离散周期 refill：在窗口边界附近可能短时间放过过多请求。这个问题在讨论中被及时发现，并调整为连续 refill，使本地实现和未来 Redis/分布式限流更容易对齐。

## 修复

实际修复分为五个方向。

1. 断路器与 provider wrapper：
   - `pkg/ai/resilience/circuit_breaker.go` 实现 closed/open/half-open 状态转换。
   - `pkg/ai/resilience/provider_wrapper.go` 只把 `errors.Is(err, llm.ErrUpstream)` 的错误计入 breaker。
   - 400、401 等调用方错误原样返回，不触发上游熔断。

2. 失败 trace 词表：
   - `pkg/ai/obs/failure_trace.go` 定义 `FailureStatus`。
   - 首批稳定状态包括 `timeout`、`rate_limit`、`retrieval_miss`、`loop_detected`、`budget_exceeded`。
   - `NewFailureTrace` 和 `WithFailureStatus` 复用现有 `Trace` 和 `TraceOption`，避免新增第二套 schema。

3. 内存限流器：
   - `pkg/ai/ratelimit/memory_limiter.go` 实现本地 token bucket。
   - 每个 key 独立计数，支持 `global`、`user:<id>`、`provider:<name>`。
   - refill 使用连续补充：按 elapsed time 比例增加 token，`tokens >= 1` 才允许请求通过。
   - 测试覆盖并发访问，并通过 `go test -race` 验证无 data race。

4. fallback cache：
   - `pkg/ai/cache/memory_fallback.go` 实现 exact/stale 本地缓存。
   - 内部 key 由 `tenant_id + user_scope + key` 组成，防止跨租户或跨用户污染。
   - ttl 内返回 `source=exact`，过期后在 `StaleTTL` 内返回 `source=stale`，超过 stale 窗口返回 miss。

5. 任务状态和文档同步：
   - `specs/001-agent-framework-spec/tasks.md` 已勾选 T069-T078。
   - 本日志补齐 T079 对故障诊断演练的文档证据。

验证命令包括：

```bash
go test ./pkg/ai/resilience ./pkg/ai/obs ./pkg/ai/ratelimit ./pkg/ai/cache
go test -race ./pkg/ai/resilience ./pkg/ai/obs ./pkg/ai/ratelimit ./pkg/ai/cache
go test -cover ./pkg/ai/obs
go test -cover ./pkg/ai/ratelimit
go test -cover ./pkg/ai/cache
go test ./...
go test -race ./...
go vet ./...
```

阶段内关键覆盖率结果：

```text
pkg/ai/obs        coverage: 99.0% of statements
pkg/ai/ratelimit  coverage: 87.2% of statements
pkg/ai/cache      coverage: 91.1% of statements
```

## 学到什么

生产 AI Agent 的故障处理不能只靠“返回一个 error”。更有价值的是把失败变成可聚合、可复盘、可降级的状态。

这轮最重要的经验有三条。

第一，错误分类必须从 provider 边界开始。`llm.ErrUpstream` 是 resilience 判断重试、熔断和降级的关键信号；4xx 参数或认证错误不能混入这个路径，否则会产生无意义重试，甚至误熔断健康 provider。

第二，Agent executor 的终止原因要和观测状态对齐。`loop_detected`、`step_timeout`、`budget_exceeded` 不是测试里的随意字符串，而是后续 trace、eval、journal 和告警都会使用的诊断维度。

第三，本地 fake/in-memory 实现也要尊重生产语义。内存限流器虽然不是最终分布式实现，但如果它用固定窗口式补充 token，测试和开发体验会训练出错误直觉。连续 refill 的实现更接近真实 token bucket，也更方便未来迁移到 Redis/Lua 或其它集中式限流器。

## 后续预防

- 增加或调整测试：US5 需要补 cache、tracer、provider、vectordb 等契约测试，确保后续真实 adapter 和内存实现遵守同一语义。
- 增加或调整评估 case：Agent golden dataset 后续应显式覆盖 `loop_detected`、`budget_exceeded` 和工具错误自我纠正。
- 增加或调整 trace 字段：后续可考虑在 `Trace` 中增加 `fallback_used`、`retry_count`、`breaker_state` 等字段，但应保持普通 trace 不记录原始 query、完整 prompt 或 tool 参数。
- 增加或调整降级路径：当前只完成本地 fallback cache 和故障状态词表，后续还需要把 provider wrapper、limiter、cache 串入统一调用链，形成“限流 -> 熔断 -> fallback cache -> 优雅失败”的可运行路径。
- 需要补充的 ADR / ROADMAP / tasks：US5 将继续补后端可替换契约；T093 P0 retrospective 应把“连续 refill 取代离散 refill”整理成一个可讲的工程判断案例。
