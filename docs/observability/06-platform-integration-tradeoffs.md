# 平台接入取舍

**关联任务**：T077
**关联规格**：`specs/002-dual-plane-observability/spec.md`
**状态**：drafted

## 理论概念

观测平台接入要区分三层职责：

- **核心语义层**：本项目的 `pkg/ai/obs.Trace`、`Tracer`、`CorrelationIdentity`、`EvaluationEvidence` 等领域对象。它们表达长期稳定的 AI Agent 观测语义。
- **Adapter 层**：把核心语义映射到 OTel span、Langfuse observation、JSON log、本地 span sink 或其它后端。它负责平台字段映射、批量上报、flush、重试、采样和失败保护。
- **平台运行层**：真实 collector、Langfuse endpoint、API key、SDK 初始化、网络、认证、保留策略和 UI 查询。

Observability v1 的取舍是：默认路径必须离线可验证，真实平台只做显式 opt-in smoke。这样能同时满足两个目标：

1. 框架核心可测试、可学习、可回归，不依赖外部服务或付费平台。
2. 后续仍能接入 OTel/Langfuse 等真实平台，而不让平台 SDK 类型反向塑造核心模型。

这里的关键风险是 **SDK 污染**：如果核心接口直接暴露 Langfuse trace/run/generation、OTel span/attribute/exporter 或某个社区 SDK 类型，后续切换平台、做离线测试、维护隐私边界都会变困难。

## 关键问题

- 这个主题解决什么生产问题？
  它解决“观测平台不可用、配置不完整或 SDK 升级时，核心 AI Agent 框架和默认测试被拖垮”的问题。

- 它在传统后端观测和 AI Agent 观测中有什么差异？
  传统后端通常接 OTel/Prometheus/日志平台即可；AI Agent 还需要 Langfuse 这类 AI 语义平台承载 generation、prompt、dataset、score 和 annotation。双平面体系因此要同时保留基础设施标准化和 AI 语义表达力。

- 哪些信息必须可见，哪些信息必须隐藏？
  必须可见的是平台是否启用、sink 类型、endpoint 是否配置、凭据是否存在、resource 身份、lifecycle 状态和 exporter failure。必须隐藏的是 secret 原值、API key、token、完整 prompt、用户输入和平台 SDK 内部对象。

## 工程实验

本项目已经把平台接入前置条件拆成可离线验证的小切片：

1. `internal/cmd/observability.go` 解析 no-op、local、platform 三种 sink。默认关闭外部访问；platform 缺 endpoint 或凭据时回退到 no-op，并保留 skip reason。
2. `internal/cmd/observability_resource.go` 构造 OTel resource 语义的稳定快照，包括 service name、environment、version 和 instance id，但不暴露 OTel SDK 类型。
3. `internal/cmd/observability_lifecycle.go` 用窄接口管理 exporter 初始化和 shutdown，记录 failure status/message，但不把 exporter 错误返回成主流程错误。
4. `pkg/ai/obs/otel_mapper.go` 和 `otel_tracer.go` 用 `TraceSpanSnapshot` 做 OTel-style adapter 壳层，核心 `Tracer` 接口仍只依赖 `obs.Trace`。
5. `specs/002-dual-plane-observability/quickstart.md` 将真实平台 smoke 明确放在 opt-in 区域，不进入默认门禁。

可运行的验证命令：

```bash
go test ./internal/cmd -run 'TestResolveObservabilityConfig|TestBuildObservabilityResource|TestObservabilityTracerProviderLifecycle' -count=1
go test ./pkg/ai/obs -run 'TestMapTraceToSpanSnapshot|TestOTelTracerContract|TestOTelTracerDropsTraceWithoutObservationType' -count=1
```

观察结果应满足：

- 默认配置不会访问真实平台。
- local sink 可用于离线验证。
- platform 缺 endpoint 或凭据时不会外连，并给出 skip reason。
- resource 和 lifecycle 可离线测试，不依赖 OTel SDK 类型。
- OTel-style adapter 保留核心字段和隐私边界，但不改变 `obs.Tracer` 契约。

## 最佳实践

本项目在平台接入上采用以下实践：

- **默认离线验证**：所有核心契约、smoke 和学习资产验证都应在没有 API key、endpoint、collector 的情况下运行。
- **真实平台 opt-in**：只有显式启用、配置完整、运行 smoke 时才访问真实 OTel/Langfuse 后端。
- **窄接口隔离 SDK**：平台 SDK 类型只允许出现在 adapter 或 app-layer；`pkg/ai` 核心不导入平台 SDK。
- **配置解析先挡风险**：配置缺 endpoint 或凭据时，解析结果直接变成 no-op/skip，而不是让下游调用方自己拼条件。
- **上报失败可诊断但不覆盖业务**：exporter/lifecycle failure 进入 failure status/message，不让用户请求失败。
- **隐私契约先于平台便利性**：如果平台 SDK 默认采集 prompt/messages/tool args，adapter 必须关闭或过滤。

暂不采用的做法：

- 不把 Langfuse SDK 或 OTel SDK 作为核心依赖。
- 不让真实平台 smoke 进入默认 CI 门禁。
- 不为了平台 UI 好看而放宽普通 trace 的隐私边界。
- 不用平台 id 替代本项目的 `request_id`、`service_trace_id`、`ai_trace_id`、`eval_run_id`。

## 失败模式

平台接入常见失败方式包括：

- **默认测试依赖外部平台**：本地或 CI 缺 endpoint/API key 就失败，阻塞核心开发。
- **配置半启用**：用户设置了 platform sink 但缺少 endpoint 或凭据，系统仍尝试外连并产生噪声错误。
- **SDK 类型污染核心**：核心接口返回平台 trace id、span 类型或 SDK 对象，导致替换平台困难。
- **平台自动采集过宽**：SDK 默认记录 prompt、messages、tool args 或 headers，突破隐私边界。
- **exporter failure 覆盖业务错误**：collector 不可用导致用户请求失败，或者业务错误被观测错误掩盖。
- **resource 身份不稳定**：service name/environment/version 缺失，平台上无法区分服务实例和部署环境。
- **平台 UI 反向牵引模型**：为了适配某个平台展示字段，把核心 `obs.Trace` 改成平台专属 schema。

## 降级路径

平台接入的降级顺序应是：

1. **未启用**：默认 no-op，不访问外部平台。
2. **本地验证**：使用 local sink、内存 recorder 或 smoke payload 保留可断言证据。
3. **平台配置不完整**：跳过真实平台上报，返回 skip reason，不打印 secret。
4. **平台上报失败**：记录 `telemetry_export_failed` 和低敏错误消息，业务流程继续。
5. **平台 SDK 行为不符合隐私边界**：禁用自动采集或封装过滤 adapter；不能让平台默认行为进入核心。

降级不能做的事是自动切换到不安全上报、打印凭据帮助排障，或把平台错误伪装成业务错误。

## 复盘问题

- 这次实现证明了哪个理论概念？
  平台接入不是核心语义本身。核心要先稳定，再通过 adapter 映射到 OTel/Langfuse，本地 smoke 和平台 smoke 各自负责不同风险。

- 哪个字段或边界最容易被误用？
  `Enabled` 和 `ExternalExportEnabled` 容易混淆；前者表达观测运行状态，后者表达是否允许外连。平台 secret 也最容易被错误写入配置结果或日志。

- 如果线上出问题，应该先看哪条记录？
  先看解析后的 sink、external export 状态和 skip reason，再看 resource 身份和 lifecycle failure status/message，最后查平台 exporter 的低敏错误。

- 后续阶段需要补什么能力？
  Phase 8 需要补真实平台 smoke 配置读取、缺配置跳过、配置齐备时发送最小链路，以及对 Langfuse/OTel SDK 自动采集行为的隐私验证。

## 关联任务 / 测试

- 任务：T004、T027-T033、T040-T041、T077、T082-T090
- 测试：
  - `internal/cmd/observability_config_test.go`
  - `internal/cmd/observability_resource_test.go`
  - `internal/cmd/observability_lifecycle_test.go`
  - `pkg/ai/obs/otel_mapper_test.go`
  - `pkg/ai/obs/otel_tracer_test.go`
  - `pkg/ai/obs/tracer_contract_test.go`
- ADR：
  - `docs/adr/0006-observability-adapter-boundary.md`
  - `docs/adr/0007-dual-plane-observability-evaluation-v1.md`
- Journal：
  - `docs/journal/0005-observation-type-defaulting.md`
  - `docs/journal/0007-observability-privacy-boundary.md`
