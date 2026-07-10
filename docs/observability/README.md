# Observability v1 学习资产索引

本目录包含两类文档：01-07 是在 `specs/002-dual-plane-observability` 的 T072-T078 中建立的学习资产；08 是当前 `specs/003-real-observability-backends` 的真实后端决策工作台。它不是普通文档附录，而是 Observability 的学习轨道：每个主题都要把理论概念、工程实验、最佳实践和复盘问题连接到实际实现任务。

## 学习路径

1. [系统可观测性基础](01-observability-foundations.md)
   - 目标：理解 logs、metrics、traces、SLO 与故障诊断的关系。
   - 工程落点：服务入口观测、请求链路、失败状态与默认离线 smoke。
   - 测试证据：`internal/eval/smoke/infrastructure_span_test.go`、`internal/eval/smoke/infrastructure_export_failure_test.go`、`internal/cmd/observability_*_test.go`。

2. [分布式追踪](02-distributed-tracing.md)
   - 目标：理解 trace、span、context、baggage、传播边界和常见误用。
   - 工程落点：request_id、service_trace_id、span_id、ai_trace_id 的关联身份设计。
   - 测试证据：`pkg/ai/obs/correlation_test.go`、`pkg/ai/obs/baggage_policy_test.go`、`pkg/ai/obs/dual_plane_link_test.go`。

3. [AI Agent 可观测性](03-ai-agent-observability.md)
   - 目标：理解 generation、retriever、tool、agent step、token/cost、loop 和 budget 诊断。
   - 工程落点：AI 语义记录、Agent step 摘要、tool 调用摘要和失败分类。
   - 测试证据：`pkg/ai/obs/otel_mapper_test.go`、`pkg/ai/rag/retriever_observation_test.go`、`pkg/ai/agent/executor_observation_test.go`。

4. [评估证据关联](04-evaluation-evidence.md)
   - 目标：理解 dataset、sample、metric、score、回链与回归判断。
   - 工程落点：eval report 与 request_id、ai_trace_id 的关联。
   - 测试证据：`pkg/ai/eval/evidence_test.go`、`pkg/ai/eval/runner_trace_link_test.go`、`internal/eval/smoke/eval_trace_link_test.go`。

5. [隐私边界](05-privacy-boundaries.md)
   - 目标：理解普通 trace 与敏感原文留存的边界。
   - 工程落点：hash/summary、敏感字段扫描、baggage 风险和审计链路边界。
   - 测试证据：`pkg/ai/obs/privacy_contract_test.go`、`pkg/ai/obs/redaction_test.go`、`internal/eval/smoke/observability_privacy_test.go`。

6. [平台接入取舍](06-platform-integration-tradeoffs.md)
   - 目标：理解默认离线验证、真实平台 opt-in smoke、adapter 边界和 SDK 污染风险。
   - 工程落点：OTel/Langfuse adapter、配置占位、真实平台 smoke 和上报失败降级。
   - 测试证据：`internal/cmd/observability_config_test.go`、`internal/cmd/observability_lifecycle_test.go`、`pkg/ai/obs/otel_tracer_test.go`。

7. [双平面关联](07-dual-plane-correlation.md)
   - 目标：理解基础 span、AI observation、eval evidence 的 parent/link/baggage 规则。
   - 工程落点：`RequestObservationChain`、`BuildDualPlaneLinks`、baggage allowlist 和完整链路 smoke。
   - 测试证据：`pkg/ai/obs/chain_recorder_test.go`、`pkg/ai/obs/dual_plane_link_test.go`、`internal/eval/smoke/observability_chain_test.go`。

8. [真实可观测后端接入决策工作台](08-real-backend-decision-workbench.md)
   - 目标：梳理并收敛真实可观测组件、后端服务、Collector 拓扑和最小 HTTP API 闭环的决策。
   - 工程落点：GoFrame/OTel SDK 分工、Grafana 主线/SigNoz 备选、Langfuse AI 平面、`POST /api/v1/chat` 最小闭环与 `obs-platform-smoke` 本地接入契约。
   - 状态：核心决策已沉淀为 [ADR-0008](../adr/0008-real-observability-backends-and-minimal-http-loop.md)；工作台继续记录实施细化。

## 工程证据索引

| 主题 | 工程切片 | 推荐验证命令 |
| --- | --- | --- |
| 基础设施平面 | 配置、resource、lifecycle、HTTP/service span、export failure | `go test ./internal/cmd ./internal/eval/smoke -run 'TestResolveObservabilityConfig|TestBuildObservabilityResource|TestObservabilityTracerProviderLifecycle|TestInfrastructure' -count=1` |
| 分布式追踪 | 关联身份、context 传播、baggage 白名单、link 规则 | `go test ./pkg/ai/obs -run 'TestCorrelationIdentity|TestBaggage|TestBuildDualPlaneLinks' -count=1` |
| AI 语义平面 | observation type、mapper、retriever observation、agent step observation | `go test ./pkg/ai/obs ./pkg/ai/rag ./pkg/ai/agent -run 'TestValidateObservationType|TestMapTraceToSpanSnapshot|TestBasicRetrieverRecordsRetrieval|TestExecutorRecords' -count=1` |
| 评估证据 | dataset identity、evidence、runner trace link、eval trace smoke | `go test ./pkg/ai/eval ./internal/eval/smoke -run 'TestNewEvaluationEvidence|TestRunner.*Trace|TestEvalTraceLinkSmoke' -count=1` |
| 隐私边界 | safe summary、redaction、logger/mapper/baggage/privacy smoke | `go test ./pkg/ai/obs ./internal/eval/smoke -run 'TestScanForbiddenPayloadFields|TestCrossAdapterPrivacyContract|TestObservabilityPrivacySmoke' -count=1` |
| 平台接入 | no-op/local/platform 配置、resource/lifecycle、OTel-style adapter | `go test ./internal/cmd ./pkg/ai/obs -run 'TestResolveObservabilityConfig|TestObservabilityTracerProviderLifecycle|TestOTelTracer' -count=1` |
| 双平面关联 | request chain、parent/link、eval evidence 回链、完整链路 smoke | `go test ./pkg/ai/obs ./internal/eval/smoke -run 'TestRequestObservationChainRecorder|TestBuildDualPlaneLinks|TestObservabilityChainSmoke' -count=1` |

## 使用方式

- 开始一个实现切片前，先在对应学习资产中补充“学习目标”和“关键问题”。
- 完成一个实现切片后，补充“工程实验”“最佳实践”“失败模式”和“复盘问题”。
- 若实现中出现真实失败或修复，优先写入 `docs/journal/`，并从本目录链接过去。
- 不记录 API key、token、用户隐私、完整 prompt、原始 query 或完整 tool 参数。

## 规格关联

- 学习资产来源规格：`specs/002-dual-plane-observability/`
- 当前真实后端规格：`specs/003-real-observability-backends/spec.md`
- 当前决策工作台：`docs/observability/08-real-backend-decision-workbench.md`
- 当前 ADR：`docs/adr/0008-real-observability-backends-and-minimal-http-loop.md`
