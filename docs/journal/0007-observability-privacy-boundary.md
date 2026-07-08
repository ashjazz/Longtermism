# Observability Privacy Boundary 隐私边界复盘

**日期**：2026-07-08
**关联任务**：T061-T071
**关联模块**：pkg/ai/obs、internal/eval/smoke
**状态**：已修复 / 已复盘

## 发生了什么

Phase 6 的目标是保护普通观测记录的敏感内容边界。双平面观测体系要求同一次请求能被 logger、OTel span、AI semantic trace、baggage 和 eval smoke 串起来，但这些出口都不能携带原始用户输入、完整 prompt、完整工具参数、密钥、认证 token、个人隐私或外部响应原文。

在实现前，测试先构造了包含多类敏感输入的观测请求，并扫描 logger、span sink、OTel mapper、baggage 和 smoke payload。最初这些测试处于 RED 状态，因为现有出口只依赖调用方“不要传错字段”，缺少统一的出口级保护。

## 理论误区

这次最重要的误区是把“领域模型不主动保存 raw 字段”误认为“观测出口一定安全”。真实工程里，泄露不一定来自显式的 `RawQuery` 字段，也可能来自复用字符串字段、错误地把外部响应塞进模型名或工具名、测试 fixture 临时拼接 payload，或者 adapter 为了平台可读性添加了额外属性。

第二个误区是把 baggage 当成普通 trace metadata。baggage 会跨进程传播，风险比单次本地日志更高；它必须是更窄的 allowlist，而不是“只要值看起来不敏感就允许传播”。

第三个误区是忽视“泄露检测结果”本身也可能泄露。如果 smoke 发现了敏感值并在报告里原文输出，那么测试工具会变成新的敏感信息出口。

## 工程根因

直接原因是各出口的隐私策略分散：logger、OTel mapper 和 baggage 都有自己的字段写入路径，但没有共享同一套敏感 key/value 判断。这样一来，只要某个出口漏接保护，普通 trace 就可能出现敏感 payload。

更深层的工程原因有三点：

1. 早期实现把安全边界主要放在领域对象字段设计上，而不是放到所有实际输出 surface 上。
2. OTel mapper 和 logger 都是“转换层”，容易被误认为只是格式转换，从而低估它们作为数据出站边界的责任。
3. 缺少端到端 privacy smoke，无法证明一次真实观测组合经过所有出口后仍然没有原文泄露。

## 修复

实际修复包括：

- 新增 `pkg/ai/obs/redaction.go`，集中实现 forbidden key/value 扫描 helper，并保证扫描结果只返回字段名和原因，不返回原始值。
- 在 `pkg/ai/obs/logger.go` 接入安全字符串过滤，使结构化日志只保留低敏字段和安全摘要。
- 在 `pkg/ai/obs/otel_mapper.go` 接入同一套字符串属性扫描，确保 OTel span attributes 不输出敏感原文。
- 在 `pkg/ai/obs/baggage_policy.go` 复用统一 value 检测，同时保留 baggage 自身的 key allowlist。
- 新增 `internal/eval/smoke/observability_privacy.go`，组合 logger、span sink、OTel mapper、baggage 和 smoke payload 做端到端扫描。
- 更新 ADR-0007，明确隐私边界已经从原则变成可测试的出口契约。

验证命令：

```bash
go test ./pkg/ai/obs ./internal/eval/smoke -count=1
go test ./...
git diff --check
```

## 学到什么

观测体系的隐私边界必须按“出口”建模，而不是只按“领域结构体字段”建模。只要某个出口会被日志平台、trace 平台、collector、CI smoke 或测试失败报告消费，它就应该被视为数据出站边界。

双平面观测的关键不是把所有信息都串起来，而是在可关联和不过度暴露之间取得平衡。`request_id`、`service_trace_id`、`span_id`、`ai_trace_id`、`eval_run_id` 这类身份字段可以帮助全链路回查；query、prompt、tool 参数和外部响应则应转换成 hash、长度、分类、数量、状态和错误分类等摘要。

隐私保护也不能只靠“删除字段”。更稳的做法是：敏感 key 明确禁止，敏感 value 做集中扫描，baggage 额外使用 allowlist，smoke 覆盖真实组合出口，并且泄露报告本身不回显敏感值。

## 后续预防

- 增加或调整测试：新增 adapter 或观测出口时，必须加入 privacy contract 或扩展现有 smoke surface。
- 增加或调整评估 case：后续 Langfuse dataset experiment、LLM-as-Judge 或 user feedback score 接入时，评估 evidence 也要扫描敏感 payload。
- 增加或调整 trace 字段：新增字符串字段必须说明是否允许进入普通观测面；若字段可能承载原文，应只记录 hash/length/summary。
- 增加或调整降级路径：平台上报失败可以降级到 local/no-op，但降级结果也必须遵守同一套隐私边界。
- 需要补充的 ADR / ROADMAP / tasks：已在 ADR-0007 增加隐私实现回顾；后续真实 Langfuse/OTel collector adapter 落地时，应将本阶段 privacy smoke 扩展为平台接入门禁。
