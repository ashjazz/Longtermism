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

### 3. 双平面请求关联验证

目标：证明一次请求可以关联服务入口、AI 阶段、检索/工具/Agent 阶段、eval evidence 和最终 outcome。

当前测试命令：

```bash
go test ./internal/eval/smoke -run TestObservabilityChainSmoke -count=1
```

当前命令入口：

```bash
go run ./cmd/eval-smoke -smoke observability-chain -scenario success
go run ./cmd/eval-smoke -smoke observability-chain -scenario retrieval_miss
```

期望结果：

- 给定 `request_id` 可以找到基础设施入口、AI 语义阶段、eval evidence 和最终 outcome。
- 成功、失败、终止和降级状态都能被解释。
- 至少覆盖成功、上游失败、检索 miss、工具错误、循环终止、预算耗尽和降级 7 类 outcome。

双平面查询关系：

- `request_id` 是一次用户请求的总入口，用于查询完整 `RequestObservationChain`。
- `service_trace_id` 是基础设施平面的 trace 身份，用于定位 HTTP/service、DB、cache 或外部调用等传统服务 span。
- `span_id` 是基础设施平面的当前或父 span 身份；AI 语义记录通过 `parent_span_id` 指回它。
- `ai_trace_id` 是 AI 语义平面的 trace 身份，用于查询 generation、retriever、tool、agent 和 evaluator 等 AI 阶段。
- `eval_run_id + sample_id` 是评估证据入口，用于回链到 `request_id` 与 `ai_trace_id`，再反查服务入口和 AI 阶段。

默认 `observability-chain` 命令会输出一份本地 JSON 结果，包含 `RequestID`、`ServiceTraceID`、`RootSpanID`、`RootAITraceID`、`EvalRunID`、`ServiceStages`、`AIObservations` 和 `EvalEvidence`。该命令不访问真实 OTel collector、Langfuse 或模型服务。

### 4. Eval-to-trace 回链验证

目标：证明评估报告能回链到产生输出的请求和 AI 阶段。

当前测试命令：

```bash
go test ./internal/eval/smoke -run TestEvalTraceLinkSmoke -count=1
```

当前命令入口：

```bash
go run ./cmd/eval-smoke \
  -dataset internal/eval/golden/p0_smoke.json \
  -dataset-name p0-smoke \
  -dataset-version p0-smoke-local \
  -eval-run-id eval-run-p0-smoke-local
```

期望结果：

- 评估样例包含 dataset、sample、metric、score。
- 至少 90% 样例能回链到 `request_id`、`ai_trace_id`、`service_trace_id` 与 `span_id`。
- 低于 90% 时失败，并列出缺失 `request_id`、`ai_trace_id`、`service_trace_id` 或 `span_id` 的样例。
- 指标低于阈值的失败样例可以定位对应 `request_id`、`ai_trace_id`、metric 和失败摘要。
- `cmd/eval-smoke` 默认输出低敏 JSON 信封，顶层包含 `report` 与 `evalEvidence`。
- `report.dataset` 必须包含 `name` 和 `version`，避免不同数据集仅靠版本号混淆。
- `evalEvidence[]` 只允许显示 `sample`、`metric`、`score` 和 `traceIdentity`。
- `traceIdentity` 必须包含 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id` 和 `eval_run_id`。
- 输出不得包含原始用户 query、完整 prompt、answer 原文、context 原文或外部响应原文。

示例输出形态：

```json
{
  "report": {
    "dataset": {
      "name": "p0-smoke",
      "version": "p0-smoke-local"
    },
    "sampleCount": 3,
    "scores": {
      "context_hit": 1,
      "exact_match": 1
    }
  },
  "evalEvidence": [
    {
      "sample": "p0-smoke-happy-path",
      "metric": "exact_match",
      "score": 1,
      "traceIdentity": {
        "request_id": "req-p0-smoke-p0-smoke-happy-path",
        "service_trace_id": "svc-trace-p0-smoke-p0-smoke-happy-path",
        "span_id": "span-p0-smoke-p0-smoke-happy-path",
        "ai_trace_id": "ai-trace-p0-smoke-p0-smoke-happy-path",
        "eval_run_id": "eval-run-p0-smoke-local"
      }
    }
  ]
}
```

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

目标：在显式配置后验证真实观测平台可以接收最小生产观测切片。真实平台 smoke 是手动 opt-in 验证，不属于默认离线门禁，也不应被 `go test ./...`、`make test` 或默认 `make obs-smoke` 强制依赖。

### 安全边界

- 不要把 endpoint、API key、public key、secret key、token 或其它凭据写入源码、测试 fixture、文档示例、提交记录或聊天记录。
- `manifest/config/config.yaml` 只保留空占位和注释，真实值必须通过环境变量或密钥管理器注入。
- 真实平台 smoke 没有默认 endpoint，也没有降级默认 URL；缺少 endpoint 或凭据时必须 skip 或返回可读诊断。
- smoke payload 只允许携带低敏链路快照、`CredentialPresent` 和关联身份，不允许回显 secret 原值。

### 默认跳过验证

当前默认测试命令：

```bash
go test ./internal/eval/smoke -run 'TestResolvePlatformSmokeConfig|TestPlatformSmokeSkipsByDefaultWithoutExternalSend' -count=1
```

期望结果：

- 默认 `PlatformSmokeConfigInput{}` 解析为 skipped。
- sender 不会被调用。
- skip reason 能说明 smoke 未启用或缺少配置。

### 手动 opt-in 配置

手动验证真实平台前，在当前 shell 或本地密钥管理器中设置配置。以下是变量形态示例，不要把真实值写入仓库或聊天记录：

```bash
export GF_OBSERVABILITY_SMOKE_ENABLED=true
export GF_OBSERVABILITY_SMOKE_PROVIDER=otlp
export GF_OBSERVABILITY_SMOKE_ENDPOINT="https://collector.example.com"
export GF_OBSERVABILITY_SMOKE_APIKEY="<set-in-your-shell-or-secret-manager>"
```

如果平台使用 public/secret key 组合，则使用：

```bash
export GF_OBSERVABILITY_SMOKE_PUBLICKEY="<set-in-your-shell-or-secret-manager>"
export GF_OBSERVABILITY_SMOKE_SECRETKEY="<set-in-your-shell-or-secret-manager>"
```

当前本地契约命令：

```bash
go test ./internal/eval/smoke -run 'TestResolvePlatformSmokeConfig|TestPlatformSmoke' -count=1
```

期望结果：

- 配置缺失时测试跳过或输出清晰错误。
- 配置齐备时，`RunPlatformSmoke` 构造一条低敏最小链路并通过 `PlatformSmokeSender` 发送。
- 示例链路包含基础设施 stage、AI generation、retriever 或 tool、eval evidence 摘要。
- payload 包含 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id` 和 `eval_run_id`，便于真实平台上回查双平面关联。
- payload 不包含 API key、secret key、token、原始 query、完整 prompt、tool args 或外部响应原文。
- smoke 不属于默认门禁；真实平台 adapter 失败不得影响默认离线验证。

### 当前实现边界

T082-T085 约束的是 `internal/eval/smoke` 的 platform smoke runner：配置解析、默认 skip、最小 payload 和 sender 边界。它不等价于应用层 `internal/cmd/observability.go` 的完整配置桥接验证。后续真实 adapter 或命令入口落地时，还需要增加从 GoFrame 配置/env 到 `PlatformSmokeConfigInput` 的桥接契约，确保应用默认配置也不会生成启用状态或默认外部 endpoint。

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
