# OTel Tracer ObservationType 默认值误判复盘

**日期**：2026-07-06
**关联任务**：T041
**关联模块**：pkg/ai/obs
**状态**：已修复 / 已复盘

## 发生了什么

在实现 `OTelTracer` adapter 时，契约测试 `records multiple traces in order` 里的两条 trace 没有显式设置 `ObservationType`。由于 `MapTraceToSpanSnapshot` 已经要求 `ObservationType` 必须属于稳定词表，OTel adapter 的契约测试失败。

当时错误地加入了 `traceWithDefaultObservationType`，把缺失类型的 trace 默认视为 `ObservationTypeAgent`。这让测试通过了，但也把一个测试 fixture 的语义缺口扩散成了生产实现的隐式默认。

这个判断是严重设计失误：AI 语义观测里的阶段类型不是展示字段，而是后续 trace、eval、平台 adapter、链路回放和性能分析都会依赖的事实字段。adapter 没有资格猜测一个缺失类型的 trace 是 agent、generation 还是 retriever。

## 根因

直接原因是为了让新 adapter 复用旧 `Tracer` 契约测试，优先修了实现，而没有先审查旧测试数据是否满足新语义。

更深层的工程原因有三点：

1. 把“测试通过”误当成“契约正确”，没有识别出旧测试 fixture 缺少 `ObservationType` 本身就是契约缺口。
2. 把 adapter 当成兼容层时边界过宽，允许它补领域事实，而不是只做格式映射和安全失败。
3. 没有及时把“显式优于隐式”落实到观测字段级别，尤其是 AI 语义阶段、失败状态和双平面关联身份这些核心字段。

## 修复

实际修复包括：

- 删除 `pkg/ai/obs/otel_tracer.go` 中的 `traceWithDefaultObservationType`。
- `OTelTracer.Record` 直接调用 `MapTraceToSpanSnapshot(trace)`，继续复用 mapper 的严格校验。
- 在 `pkg/ai/obs/tracer_contract_test.go` 中为旧契约测试补齐显式 `WithObservationType(ObservationTypeGeneration)` 和 `WithObservationType(ObservationTypeAgent)`。
- 在 `pkg/ai/obs/otel_tracer_test.go` 中增加回归测试：缺失 `ObservationType` 的 trace 不应被 OTel tracer 记录。

验证命令：

```bash
go test $(find pkg/ai/obs -maxdepth 1 -name '*.go' ! -name '*_test.go' | sort) pkg/ai/obs/tracer_contract_test.go pkg/ai/obs/otel_tracer_test.go -run 'TestOTelTracerDropsTraceWithoutObservationType|TestOTelTracerContract|TestLoggerTracerContract' -count=1
go test $(find pkg/ai/obs -maxdepth 1 -name '*.go' ! -name '*_test.go' | sort) pkg/ai/obs/chain_recorder_test.go pkg/ai/obs/otel_mapper_test.go pkg/ai/obs/tracer_contract_test.go pkg/ai/obs/otel_tracer_test.go -run 'TestRequestObservationChainRecorder|TestMapTraceToSpanSnapshot|TestOTelTracerDropsTraceWithoutObservationType|TestOTelTracerContract|TestLoggerTracerContract' -count=1
git diff --check
```

包级测试仍因 T042 尚未实现 `BuildDualPlaneLinks` 相关类型而失败，这属于后续任务的预期 RED 缺口。

## 学到什么

契约测试的价值在于表达稳定语义。如果为了复用契约测试而在实现里猜测缺失事实，测试就从保护网变成了错误语义的传播器。

后续面对类似问题时，应先区分三类情况：

1. 旧测试 fixture 语义不完整：修测试，补契约。
2. 实现没有遵守明确契约：修实现。
3. 真实生产边界允许缺失字段：设计显式错误、丢弃策略或低敏诊断状态，而不是伪造事实。

尤其在 AI Agent 观测体系中，`ObservationType`、`FailureStatus`、`request_id`、`service_trace_id`、`ai_trace_id`、`eval_run_id` 都属于事实字段。事实字段缺失时应该暴露问题，而不是由底层 adapter 猜一个“看起来合理”的值。

## 后续预防

- 增加或调整测试：为 adapter 增加“缺失核心语义字段时不能猜测”的回归测试。
- 增加或调整评估 case：后续 eval evidence 回链测试也要覆盖缺失关联身份时的失败路径。
- 增加或调整 trace 字段：保持核心观测字段显式；如需派生字段，应在命名和注释中区分事实字段与派生字段。
- 增加或调整降级路径：观测平台失败可以降级为 no-op/local sink，但不能把缺失语义字段降级成虚假的业务事实。
- 需要补充的 ADR / ROADMAP / tasks：已在 `AGENTS.md` 增加“语义优先约束”，作为后续所有任务的项目级规则。
