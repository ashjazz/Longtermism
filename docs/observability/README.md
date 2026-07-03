# Observability v1 学习资产索引

本目录服务于 `specs/002-dual-plane-observability`。它不是普通文档附录，而是 Observability v1 的学习轨道：每个主题都要把理论概念、工程实验、最佳实践和复盘问题连接到实际实现任务。

## 学习路径

1. [系统可观测性基础](01-observability-foundations.md)
   - 目标：理解 logs、metrics、traces、SLO 与故障诊断的关系。
   - 工程落点：服务入口观测、请求链路、失败状态与默认离线 smoke。

2. [分布式追踪](02-distributed-tracing.md)
   - 目标：理解 trace、span、context、baggage、传播边界和常见误用。
   - 工程落点：request_id、service_trace_id、span_id、ai_trace_id 的关联身份设计。

3. [AI Agent 可观测性](03-ai-agent-observability.md)
   - 目标：理解 generation、retriever、tool、agent step、token/cost、loop 和 budget 诊断。
   - 工程落点：AI 语义记录、Agent step 摘要、tool 调用摘要和失败分类。

4. [评估证据关联](04-evaluation-evidence.md)
   - 目标：理解 dataset、sample、metric、score、回链与回归判断。
   - 工程落点：eval report 与 request_id、ai_trace_id 的关联。

5. [隐私边界](05-privacy-boundaries.md)
   - 目标：理解普通 trace 与敏感原文留存的边界。
   - 工程落点：hash/summary、敏感字段扫描、baggage 风险和审计链路边界。

6. [平台接入取舍](06-platform-integration-tradeoffs.md)
   - 目标：理解默认离线验证、真实平台 opt-in smoke、adapter 边界和 SDK 污染风险。
   - 工程落点：OTel/Langfuse adapter、配置占位、真实平台 smoke 和上报失败降级。

## 使用方式

- 开始一个实现切片前，先在对应学习资产中补充“学习目标”和“关键问题”。
- 完成一个实现切片后，补充“工程实验”“最佳实践”“失败模式”和“复盘问题”。
- 若实现中出现真实失败或修复，优先写入 `docs/journal/`，并从本目录链接过去。
- 不记录 API key、token、用户隐私、完整 prompt、原始 query 或完整 tool 参数。

## 当前关联规格

- 规格：`specs/002-dual-plane-observability/spec.md`
- 计划：`specs/002-dual-plane-observability/plan.md`
- 任务：`specs/002-dual-plane-observability/tasks.md`
- ADR：`docs/adr/0007-dual-plane-observability-evaluation-v1.md`
