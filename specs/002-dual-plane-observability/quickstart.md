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
- 后续 T019-T031 会把该入口替换为完整离线请求链路 smoke。

### 1. Contract 验证

目标：证明所有 tracer/adapter 都保留稳定字段、记录顺序、防御性拷贝和隐私边界。

预期命令形态：

```bash
go test ./pkg/ai/obs -run 'Test.*Contract|Test.*Privacy' -count=1
```

期望结果：

- 关键 AI 字段可还原为稳定测试快照。
- 普通 payload 不包含原始 query、完整 prompt、tool args 或密钥。
- 多条 trace 顺序稳定。

### 2. 请求关联验证

目标：证明一次请求可以关联服务入口、AI 阶段、检索/工具/Agent 阶段和最终 outcome。

预期命令形态：

```bash
go test ./internal/eval/smoke -run TestObservabilityChainSmoke -count=1
```

期望结果：

- 给定 `request_id` 可以找到所有相关阶段。
- 成功、失败、终止和降级状态都能被解释。
- 至少覆盖成功、上游失败、检索 miss、工具错误、循环终止、预算耗尽和降级 7 类 outcome。

### 3. Eval-to-trace 回链验证

目标：证明评估报告能回链到产生输出的请求和 AI 阶段。

预期命令形态：

```bash
go test ./internal/eval/smoke -run TestEvalEvidenceLinksToTrace -count=1
```

期望结果：

- 评估样例包含 dataset、sample、metric、score。
- 至少 90% 样例能回链到 `request_id` 与 `ai_trace_id`。
- 失败样例可以定位对应 trace 和失败摘要。

### 4. 上报失败降级验证

目标：证明观测后端不可用不会影响用户主流程。

预期命令形态：

```bash
go test ./pkg/ai/obs -run TestTelemetryExportFailureDoesNotFailRequest -count=1
```

期望结果：

- adapter 上报失败时主流程仍返回原业务结果或明确业务失败。
- 观测失败本身被记录为可诊断状态。

### 5. 学习资产验证

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
