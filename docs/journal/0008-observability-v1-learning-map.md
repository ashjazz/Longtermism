# Observability v1 学习地图

**日期**：2026-07-08
**关联任务**：T072-T080
**关联模块**：docs/observability、pkg/ai/obs、pkg/ai/eval、internal/eval/smoke、internal/cmd
**状态**：已复盘

## 发生了什么

Phase 7 的目标不是继续增加观测功能，而是把 Observability v1 已经形成的理论知识、工程实验、最佳实践和复盘问题串成一条可学习、可回查的路径。

在前几个阶段里，我们已经实现了双平面观测与评估体系的核心工程切片：基础设施 span、AI semantic observation、request chain、eval evidence、隐私边界、平台接入配置和 smoke 验证。T072-T079 将这些切片拆成七份学习资产，并在 `docs/observability/README.md` 中形成阅读顺序和测试证据索引。

T080 进一步把这条学习路径记录到 journal，作为后续复盘和面试表达时的总地图：它不替代 ADR，也不替代具体学习资产，而是回答“Observability v1 到底学了哪几件事、每件事落到了哪些工程证据上”。

## 学习地图

Observability v1 的学习路径按从底层事实到高层评估的顺序展开：

1. 先理解系统可观测性的基本目标：日志、指标和链路不是为了收集更多数据，而是为了缩短定位问题的时间。
2. 再理解分布式追踪：trace、span、context 和 baggage 用来表达请求经过了哪些服务边界，以及跨进程传播什么身份。
3. 接着进入 AI Agent 可观测性：generation、retriever、tool 和 agent step 需要表达语义阶段，而不只是普通函数耗时。
4. 然后把一次请求与评估体系连接起来：dataset、sample、metric、score 和 evidence 必须能回链到 request 和 AI trace。
5. 同时建立隐私边界：可观测不是保存原文，普通观测面应优先保存身份、hash、长度、分类、状态和错误类别。
6. 最后讨论平台接入：OTel、Langfuse 或其他平台只能作为 adapter 和 sink，不能反向污染核心领域契约。
7. 在总链路层，把基础设施平面、AI 语义平面和评估证据通过稳定关联身份串成一次请求的可解释事实链。

## 六个核心主题

### 基础设施平面

基础设施平面负责传统服务事实：HTTP 请求、service span、错误类别、延迟、资源配置和 exporter 生命周期。它回答的是“这次请求在系统里怎么流动、在哪个服务边界失败、平台上报是否影响主流程”。

工程证据：

- `internal/eval/smoke/infrastructure_span.go` 验证基础设施 span 的最小烟测。
- `internal/eval/smoke/infrastructure_export_failure.go` 验证 exporter 失败不能覆盖业务结果。
- `internal/cmd/observability_bootstrap.go` 和 `internal/cmd/observability_lifecycle.go` 验证 noop/local/collector 装配、Collector-only 边界和生命周期降级。
- `docs/observability/01-observability-foundations.md` 和 `docs/observability/02-distributed-tracing.md` 记录理论基础。

### AI 语义平面

AI 语义平面负责 Agent 领域事实：generation、retriever、tool、agent step、token、cost、loop、budget 和能力边界。它回答的是“AI 过程实际做了什么、为什么产生这个结果、是否触发了策略或预算约束”。

工程证据：

- `pkg/ai/obs/observation_type.go` 明确 observation type 不能由 adapter 猜测。
- `pkg/ai/rag/retriever_observation_test.go` 验证检索阶段的语义观测。
- `pkg/ai/agent` 相关测试验证 agent step 记录。
- `docs/observability/03-ai-agent-observability.md` 记录 AI Agent 观测主题。

### 双平面关联

双平面关联层负责把基础设施 trace 和 AI semantic trace 串起来。核心身份包括 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id`、`session_id` 和 `eval_run_id`。它回答的是“从用户请求、服务 span、AI 阶段到评估证据，能否沿同一条链路互相回查”。

工程证据：

- `pkg/ai/obs/correlation_identity.go` 定义关联身份。
- `pkg/ai/obs/dual_plane_link.go` 定义双平面 link。
- `pkg/ai/obs/chain_recorder.go` 保存 request 维度的完整事实链快照。
- `pkg/ai/obs/baggage_policy.go` 控制跨进程传播字段。
- `docs/observability/07-dual-plane-correlation.md` 记录 parent/link/baggage 规则。

### 隐私边界

隐私边界负责回答“什么东西绝对不能进入普通观测面”。本阶段的结论是：query、完整 prompt、tool args、token、密钥、JWT、密码和外部响应原文都不能进入日志、span、baggage、mapper 或 smoke 报告；需要诊断时应使用 hash、长度、分类、数量、状态和错误类别。

工程证据：

- `pkg/ai/obs/redaction.go` 集中实现 forbidden key/value 扫描。
- `pkg/ai/obs/logger.go` 和 `pkg/ai/obs/otel_mapper.go` 在出口层过滤字符串属性。
- `pkg/ai/obs/baggage_policy.go` 使用更窄的 allowlist。
- `internal/eval/smoke/observability_privacy.go` 覆盖端到端隐私 smoke。
- `docs/journal/0007-observability-privacy-boundary.md` 记录隐私边界复盘。
- `docs/observability/05-privacy-boundaries.md` 记录学习资产。

### 评估证据

评估证据负责把一次评估从“分数”变成“可追溯事实”。dataset identity、sample、metric、score、threshold、request_id 和 ai_trace_id 一起构成回归判断的上下文。它回答的是“某个能力是否退化、退化来自哪个样本、能否回查到对应请求与 AI 阶段”。

工程证据：

- `pkg/ai/eval/dataset_identity.go` 将 dataset name 和 version 建模为完整身份。
- `pkg/ai/eval/evidence.go` 定义可回链的 evidence。
- `pkg/ai/eval/runner.go` 生成评估报告和 evidence。
- `internal/eval/smoke/eval_trace_link.go` 验证 eval 到 trace 的回链。
- `docs/observability/04-evaluation-evidence.md` 记录评估证据学习资产。

### 平台接入

平台接入负责把本项目稳定领域快照映射到真实平台，例如 OTel collector 或 Langfuse。关键边界是：平台 SDK 不进入核心契约，真实平台默认不外连，缺少配置时应 no-op/local，平台失败不影响业务主流程。

工程证据：

- `pkg/ai/obs/otel_mapper.go` 将核心 trace 映射为 OTel-style span snapshot。
- `pkg/ai/obs/otel_tracer.go` 保持 adapter 壳层，不猜测缺失语义。
- `internal/cmd/observability_resource.go` 生成基础设施 resource 摘要。
- `internal/cmd/observability_bootstrap.go` 明确 noop/local/collector 的装配与 Collector-only 边界。
- `docs/observability/06-platform-integration-tradeoffs.md` 记录平台接入取舍。

## 工程验证索引

本阶段学习资产需要和测试证据保持同步。当前最小验证命令包括：

```bash
go test ./pkg/ai/obs -run 'TestCorrelationIdentity|TestBaggageFieldsFromCorrelationIdentity|TestBuildDualPlaneLinks|TestRequestObservationChainRecorder|TestMapTraceToSpanSnapshot|TestOTelTracerContract|TestCrossAdapterPrivacyContractRejectsRawPayload' -count=1
go test ./pkg/ai/eval -run 'TestNewEvaluationEvidence|TestValidateEvaluationEvidence|TestRunnerAddsEvaluationEvidence|TestRunnerReportsMissingTraceLink|TestRunnerAddsFailedRegressionEvidence' -count=1
go test ./internal/eval/smoke -run 'TestInfrastructureSpan|TestInfrastructureExportFailure|TestObservabilityChainSmoke|TestEvalTraceLinkSmoke|TestObservabilityPrivacySmoke' -count=1
go test ./internal/cmd -run 'TestBuildObservabilityBootstrap|TestBuildObservabilityResource|TestObservabilityProviderLifecycle' -count=1
```

这些命令不是为了证明文档存在，而是为了证明文档中的学习主题确实对应可运行的工程切片。

## 学到什么

Observability v1 的核心学习成果是“双平面分工 + 关联身份 + 评估回链 + 隐私边界”。基础设施平面擅长解释服务行为，AI 语义平面擅长解释 Agent 决策，评估证据擅长解释能力质量，平台 adapter 负责展示和上报；它们不应该互相替代。

学习型项目不能只积累代码，也要积累“为什么这样设计”的证据。学习资产、ADR、journal 和测试应该形成闭环：ADR 记录决策，学习资产解释概念，测试证明行为，journal 记录阶段性经验和错误修正。

## 后续预防

- 增加或调整测试：后续每新增一个观测出口、评估 evidence 类型或平台 adapter，都必须补对应 contract/smoke，而不是只更新文档。
- 增加或调整评估 case：真实 RAG、prompt version、loop 策略和上下文压缩策略进入评估体系时，应先补 dataset identity、metric 和 trace 回链。
- 增加或调整 trace 字段：新增字段必须归属到基础设施平面、AI 语义平面、关联层或评估证据之一，避免“看起来方便”的混合字段。
- 增加或调整降级路径：平台不可用、exporter panic、collector 超时或 Langfuse 配置缺失时，业务结果与本地诊断应保持可用。
- 需要补充的 ADR / ROADMAP / tasks：Phase 8 真实平台 smoke 开始后，应将这份学习地图更新为包含 opt-in 平台验证结果的 v1.1 记录。
