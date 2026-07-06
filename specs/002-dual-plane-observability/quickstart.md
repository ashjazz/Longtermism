# Quickstart：双平面观测与评估体系 v1

本文描述规划后的验证路径。具体命令会在 `/speckit-tasks` 和实现阶段落地；当前文件定义每条路径要证明什么。

## 前置条件

- 已阅读 `specs/002-dual-plane-observability/spec.md`。
- 已阅读 `docs/adr/0006-observability-adapter-boundary.md` 与 `docs/adr/0007-dual-plane-observability-evaluation-v1.md`。
- 默认验证不得要求真实外部平台、API key 或付费服务。

## 默认离线验证

### 0. Makefile 入口

目标：提供一个稳定的本地入口，让后续实现逐步把 Observability v1 离线 smoke 接入默认验证。

当前命令：

```bash
make obs-smoke
```

当前期望：

- Phase 1 阶段输出清晰 TODO。
- 不要求真实观测平台 endpoint、API key 或付费服务。
- Phase 3 已落地基础设施平面离线测试；`make obs-smoke` 仍是聚合入口占位，后续任务会把它替换为完整离线请求链路 smoke。

### 1. 基础设施平面离线验证

目标：证明应用层观测配置、service resource、provider lifecycle、HTTP/service span、handler-to-core 传播和 exporter 失败保护都能在本地验证，且默认不访问真实观测平台。

当前命令：

```bash
go test ./internal/cmd -count=1
go test ./internal/eval/smoke -run 'TestInfrastructureSpanSmoke|TestContextPropagationSmoke|TestInfrastructureExportFailureSmoke' -count=1
```

期望结果：

- 默认配置解析为 `noop`，不会访问真实平台。
- `local` sink 只启用本地离线记录。
- 真实平台缺少 endpoint 或凭据时解析为运行态关闭，并保留诊断原因。
- service resource 包含 `service.name` 与 `deployment.environment`，并支持可选 `service.version`、`service.instance.id`。
- TracerProvider lifecycle 初始化与 shutdown 都是幂等的，exporter 失败记录为 `telemetry_export_failed`，并保留低敏错误消息。
- HTTP/service span 离线快照包含 method、path、status、duration、request_id、service_trace_id、span_id 和 outcome。
- handler-to-core 传播只携带低敏关联身份，敏感 baggage 会被拒绝。
- exporter 失败不会覆盖业务结果；业务自身失败仍作为业务错误返回。

### 2. Contract 验证

目标：证明所有 tracer/adapter 都保留稳定字段、记录顺序、防御性拷贝和隐私边界。

预期命令形态：

```bash
go test ./pkg/ai/obs -run 'Test.*Contract|Test.*Privacy' -count=1
```

期望结果：

- 关键 AI 字段可还原为稳定测试快照。
- 普通 payload 不包含原始 query、完整 prompt、tool args 或密钥。
- 多条 trace 顺序稳定。

### 3. 请求关联验证

目标：证明一次请求可以关联服务入口、AI 阶段、检索/工具/Agent 阶段和最终 outcome。

预期命令形态（Phase 4 双平面关联层落地后启用）：

```bash
go test ./internal/eval/smoke -run TestObservabilityChainSmoke -count=1
```

期望结果：

- 给定 `request_id` 可以找到所有相关阶段。
- 成功、失败、终止和降级状态都能被解释。
- 至少覆盖成功、上游失败、检索 miss、工具错误、循环终止、预算耗尽和降级 7 类 outcome。

### 4. Eval-to-trace 回链验证

目标：证明评估报告能回链到产生输出的请求和 AI 阶段。

预期命令形态（Phase 4/5 评估证据回链落地后启用）：

```bash
go test ./internal/eval/smoke -run TestEvalEvidenceLinksToTrace -count=1
```

期望结果：

- 评估样例包含 dataset、sample、metric、score。
- 至少 90% 样例能回链到 `request_id` 与 `ai_trace_id`。
- 失败样例可以定位对应 trace 和失败摘要。

### 5. 上报失败降级验证

目标：证明观测后端不可用不会影响用户主流程。

当前基础设施平面命令：

```bash
go test ./internal/eval/smoke -run TestInfrastructureExportFailureSmoke -count=1
```

AI 语义平面预期命令形态：

```bash
go test ./pkg/ai/obs -run TestTelemetryExportFailureDoesNotFailRequest -count=1
```

期望结果：

- adapter 上报失败时主流程仍返回原业务结果或明确业务失败。
- 观测失败本身被记录为可诊断状态。

### 6. 学习资产验证

目标：证明主要工程切片都有学习目标和复盘链路。

预期检查：

```bash
find docs -path '*observability*' -o -path '*journal*'
```

期望结果：

- 至少 4 个主要切片具备学习资产。
- 每个学习资产包含理论概念、工程实验、最佳实践和复盘问题。

## 真实平台 opt-in smoke

目标：在显式配置后验证真实观测平台可以接收最小生产观测切片。

前置配置：

- 平台 endpoint。
- 公钥/密钥或授权信息，以环境变量或本地配置注入。
- 明确启用 smoke 的开关。

预期命令形态：

```bash
go test ./internal/eval/smoke -run TestObservabilityPlatformSmoke -count=1
```

期望结果：

- 配置缺失时测试跳过或输出清晰错误。
- 配置齐备时发送至少 1 条完整示例链路。
- 示例链路包含基础链路、AI generation、retriever 或 tool、eval 摘要。
- smoke 不属于默认门禁。

## 完成判据

Observability v1 进入任务执行阶段前，本目录应包含：

- `plan.md`
- `research.md`
- `data-model.md`
- `contracts/observability-v1-contract.md`
- `quickstart.md`

实现完成后，至少应能证明：

- 默认离线验证通过。
- 隐私泄露命中数为 0。
- eval-to-trace 回链率不低于 90%。
- 真实平台 smoke 可 opt-in 验证。
- 学习资产覆盖不少于 4 个主要工程切片。
