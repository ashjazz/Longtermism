# 契约：双平面观测与评估体系 v1

## 目的

本契约定义 Observability v1 必须保持的用户可见语义。实现可以使用不同后端或 adapter，但必须证明以下契约不变：请求可关联、字段可诊断、隐私不泄露、失败可分类、评估可回链、学习资产可复用。

## 1. 请求关联契约

每次被观测的用户请求必须满足：

- 存在稳定 `request_id`。
- 至少存在一个基础链路记录或一个 AI 语义记录。
- 如果请求触发 AI 能力，必须存在 `ai_trace_id`。
- 如果请求来自会话，必须保留 `session_id`。
- 如果请求产生评估结果，评估结果必须包含 `request_id` 和 `ai_trace_id`。

验收断言：

- 给定 `request_id`，可以定位服务入口、AI 阶段、辅助步骤、最终 outcome 和评估摘要。
- 给定 `eval_run_id + sample_id`，可以回链到 `request_id` 和 `ai_trace_id`。

## 2. AI 语义记录契约

AI 语义记录必须声明 `observation_type`：

| 类型 | 必填字段 | 说明 |
| --- | --- | --- |
| `generation` | model、prompt identity、usage summary、latency summary、outcome | 模型生成阶段 |
| `retriever` | retrieval count 或 miss status、latency、score summary、outcome | 检索阶段 |
| `tool` | tool name、tool call id、status、latency、safe argument summary | 工具或外部工具服务阶段 |
| `agent` | step index、termination reason、limit status、outcome | Agent 编排阶段 |
| `evaluator` | dataset/sample/metric/score、linked trace identity | 评估阶段 |

验收断言：

- 每种类型至少有一个默认离线测试样例。
- 缺少必填字段时必须快速失败或产生可诊断错误。
- 字段映射到具体平台后仍可还原为稳定测试快照。

## 3. Outcome 与失败分类契约

所有观测记录必须使用稳定 outcome：

- `success`：请求或阶段成功完成。
- `failure`：请求或阶段失败，且没有可用降级结果。
- `terminated`：请求或阶段被限制、取消或主动终止。
- `degraded`：请求或阶段使用降级结果完成。

常见失败分类至少包括：

- timeout
- rate_limited
- upstream_failure
- retrieval_miss
- tool_error
- loop_detected
- budget_exceeded
- telemetry_export_failed

验收断言：

- 默认离线验证必须覆盖不少于 7 类 outcome/failure 组合。
- 上报失败不得让用户请求失败；必须产生内部可诊断状态。

## 4. 隐私契约

普通观测记录禁止包含：

- 原始用户输入
- 完整 prompt
- 完整工具参数
- API key、JWT、session token、password
- 个人隐私原文
- 外部 API 响应原文

普通观测记录允许包含：

- hash
- 长度
- 语言或分类
- 数量
- 分数摘要
- 状态
- 错误分类
- token 与成本摘要

验收断言：

- 隐私测试必须构造包含敏感输入、敏感工具参数和密钥的样例。
- 普通观测 payload 中敏感原文命中数必须为 0。

## 5. 评估证据契约

每条评估证据必须包含：

- dataset name/version
- sample id
- metric name
- score
- eval run id
- request id
- ai trace id

验收断言：

- 至少 90% 的评估样例结果可以回链到请求和 AI 阶段。
- 得分下降或阈值失败时，报告必须能定位失败样例和对应 trace。

## 6. 默认验证与真实平台 smoke 契约

默认验证必须：

- 不依赖真实外部平台。
- 不依赖真实 API key。
- 可重复运行。
- 输出可断言记录或报告。

真实平台 smoke 必须：

- 显式 opt-in。
- 在缺少配置时跳过或给出清晰错误。
- 至少发送一条包含基础链路、AI generation、retriever 或 tool、eval 摘要的示例链路。
- 不进入默认门禁。

## 7. 学习资产契约

每个主要工程切片必须关联学习资产：

- 学习目标
- 理论概念
- 标准语义或最佳实践
- 工程实验
- 失败模式
- 降级路径
- 复盘问题

验收断言：

- 至少 4 个主要切片具备“理论概念 -> 工程实验 -> 最佳实践 -> 复盘问题”的学习链路。
- 学习资产必须链接到相关 spec、plan、ADR、journal 或测试证据。

## 8. 基础设施平面验收摘要

基础设施平面只描述传统服务事实，不承载 AI prompt、用户原文、tool args 或外部响应原文。它至少必须保留以下低敏字段：

- `request_id`：一次用户请求的稳定入口。
- `service_trace_id`：基础设施平面的 trace 身份。
- `span_id`：当前基础设施 span 身份。
- `parent_span_id`：可选的上游 span 身份。
- `component`：HTTP、service、DB、cache、exporter 等组件分类。
- `method` / `path` / `status`：服务入口或调用摘要。
- `latency_ms`：阶段耗时摘要。
- `outcome` / `error_class`：稳定结果状态和错误分类。

验收断言：

- 默认配置必须解析为 no-op 或 local sink，不得访问真实平台。
- 真实平台缺少 endpoint 或凭据时必须解析为运行态关闭或跳过，并保留低敏诊断原因。
- service resource 必须至少包含 service name 与 deployment environment，可选包含 version 和 instance id。
- TracerProvider/exporter lifecycle 初始化与 shutdown 必须幂等。
- exporter 失败不得覆盖业务结果；业务自身失败必须继续作为业务错误暴露。
- handler-to-core 传播只能携带低敏 correlation identity，不能传播 query、prompt、tool args、token 或外部响应。

离线验收命令：

```bash
go test ./internal/cmd -count=1
go test ./internal/eval/smoke -run 'TestInfrastructureSpanSmoke|TestContextPropagationSmoke|TestInfrastructureExportFailureSmoke' -count=1
```

这些命令是契约验收的一部分，不依赖 OTel collector、Langfuse、LangSmith、Phoenix、Braintrust 或任何真实外部平台。

## 9. 双平面关联验收摘要

双平面关联层必须把基础设施平面、AI 语义平面和评估证据串成一次请求的完整事实链。关联层不拥有平台专属 schema，只拥有本项目稳定身份语义。

必须支持的关联身份：

- `request_id`：完整请求链路入口。
- `service_trace_id`：基础设施平面 trace 身份。
- `span_id`：基础设施平面 parent/current span 身份。
- `ai_trace_id`：AI 语义平面 trace 身份。
- `session_id`：可选会话身份。
- `eval_run_id`：评估运行身份。
- `sample_id`：评估样例身份。

验收断言：

- 给定 `request_id`，必须能定位服务入口、AI generation、retriever/tool/agent/evaluator 记录、eval evidence 和最终 outcome。
- 给定 `ai_trace_id`，必须能回到 `service_trace_id` 与根或父 `span_id`。
- 给定 `eval_run_id + sample_id`，必须能回到 `request_id`、`ai_trace_id`、`service_trace_id` 和 `span_id`。
- AI 语义记录必须通过 parent/link 规则指向基础设施 span，而不是复制基础设施字段形成第二套事实源。
- 缺失 `request_id`、`service_trace_id`、`span_id` 或 `ai_trace_id` 等关键身份时，必须快速失败或产生可诊断错误，不得由 adapter 猜测默认身份。
- 关联链路必须覆盖 success、upstream_failure、retrieval_miss、tool_error、loop_detected、budget_exceeded 和 degraded 等代表性 outcome。
- eval 回链率低于 90% 时，默认离线验证必须失败并列出缺失样例的低敏身份信息。

离线验收命令：

```bash
go test ./internal/eval/smoke -run TestObservabilityChainSmoke -count=1
go test ./internal/eval/smoke -run TestEvalTraceLinkSmoke -count=1
go test ./pkg/ai/obs -run 'TestBuildDualPlaneLinks|TestRequestObservationChainRecorder' -count=1
```

平台边界：

- 任何 OTel、Langfuse、LangSmith、Phoenix、Braintrust 或其它平台 adapter 都只能消费上述稳定身份和低敏快照。
- 平台字段名、span kind、trace UI 组织方式或数据集对象模型不得反向改变本契约。
- 默认离线验收通过前，不得把真实平台上报成功视为契约完成证据。
