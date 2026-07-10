# 数据模型：真实可观测后端与最小 HTTP 闭环

本文定义 003 阶段新增或收紧的值对象、关系和状态转换。领域事实继续位于 `pkg/ai`；平台配置、OTel runtime identity 和 Langfuse 投影对象位于应用/infrastructure 边界。

## 1. ObservabilityRuntimeConfig

应用可观测运行配置的安全快照。

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `enabled` | bool | false 时不得创建外连 exporter |
| `mode` | enum | `noop`、`local`、`collector` |
| `environment` | string | 非空；`production` 触发严格隐私校验 |
| `resource` | ResourceIdentity | service name 必填，version/instance 可选 |
| `collector` | CollectorClientConfig | 仅 `collector` mode 使用 |
| `signals` | SignalPolicy | traces/metrics 开关与 logs transport |
| `tracing` | TracePolicy | sampling ratio 在 `[0,1]` |
| `payload` | PayloadPolicy | 三种显式 mode |
| `smoke_enabled` | bool | false 时 infra-smoke 返回 404 |

### 验证

- `collector` mode 必须有合法 endpoint 和受支持 protocol。
- endpoint 只允许 Collector，不接受 backend-specific 字段。
- `production + content_raw` 必须失败。
- 配置快照只保留 header 环境变量名，不保存 header 原值。

## 2. CollectorClientConfig

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `protocol` | enum | `grpc` 或 `http_protobuf`；默认 `grpc` |
| `endpoint` | string | 必填；按协议校验 host/URL 形态 |
| `insecure` | bool | local 可 true；production 需显式授权 |
| `headers_env` | string | 可选，仅记录环境变量名 |
| `timeout` | duration | 正数且有上限 |

它不包含 Tempo/Loki/Prometheus/Grafana/SigNoz/Langfuse 地址。

## 3. ResourceIdentity

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `service_name` | string | 必填，默认 `longtermism` |
| `service_version` | string | 可选，来自构建信息 |
| `service_instance_id` | string | 可选，进程级唯一 |
| `deployment_environment` | string | 必填 |

Resource 是所有信号共享的低基数身份，不携带 request/user/session。

## 4. CorrelationIdentity

沿用 002 的核心值对象，但明确真实运行时来源。

| 字段 | 所有者 | 来源 | 传播规则 |
| --- | --- | --- | --- |
| `request_id` | HTTP middleware | 每请求生成/校验 | response header/meta；span/log；不进入 metric label |
| `service_trace_id` | OTel runtime | 活动 root/bridge SpanContext | span/log/report；不得手工伪造 |
| `span_id` | OTel runtime | 当前活动 span | span/log/report |
| `ai_trace_id` | AI usecase | provider 调用前生成 | response/meta、AI spans、eval evidence |
| `session_id` | 业务边界 | 当前 v1 可空 | allowlist baggage，需低敏/opaque |
| `eval_run_id` | eval runner | 评估运行 | evidence/report，不进入 metrics |

### 不变量

- infra-only 请求没有 `ai_trace_id`。
- chat 在 provider 成功或失败前已经拥有 `ai_trace_id`。
- adapter 不得把 `ai_trace_id` 当作 OTel trace ID 或 Langfuse trace ID。
- 跨服务 baggage 只允许已批准低敏字段；实际 OTel trace/span 由 TraceContext 传播。

## 5. ChatCommand / ChatResult

### ChatCommand

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `message` | string | trim 后非空；UTF-8；最大 32 KiB |
| `debug` | bool | 来自服务端运行模式，不由客户端绕过 |

客户端不能提交 provider、base URL、API key 或 model。第一阶段 system prompt、模型与参数由服务端控制。

### ChatResult

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `content` | string | provider 实际输出 |
| `model` | string | 实际模型身份，可与请求配置不同 |
| `finish_reason` | enum | 沿用 `llm.FinishReason` |
| `usage` | UsageSummary | 低敏 token 计数 |
| `identity` | CorrelationIdentity | request/service/AI 关联 |
| `eval_summary` | EvalSummary? | 仅 debug；序列化后不超过 1 KiB |

## 6. APIEnvelope<T> / ResponseMeta

所有成功与错误响应使用统一结构。

```text
APIEnvelope<T>
  code: integer
  message: string
  data: T | null
  meta: ResponseMeta
```

`ResponseMeta`：

| 字段 | 规则 |
| --- | --- |
| `request_id` | 始终存在；等于 `X-Request-ID` |
| `ai_trace_id` | chat 始终存在；infra-only 不存在 |
| `eval_summary` | 仅 debug；缺少 evaluator 时显式 `not_run` |
| `smoke_run_id` | 仅 smoke enabled 且输入合法时返回 |

错误 envelope 不回显 provider body、endpoint、credential、prompt 或用户原文。

## 7. AIPlaneMarker

```text
key   = longtermism.observability.plane
value = ai
```

### 设置边界

- AI usecase 创建 root/bridge span 时设置。
- `pkg/ai/obs` OTel adapter 为 generation/evaluator 等 AI semantic spans 设置。
- DB、Redis、普通 HTTP client 与无关框架 spans 不设置。
- infra-only 请求任一 span 均不得设置。

## 8. EvaluationEvidence

沿用 `pkg/ai/eval.EvaluationEvidence` 作为本地事实源：dataset identity、sample、metric、score、threshold、regression status 与 correlation identity。它不包含 Langfuse credential 或平台 endpoint。

### 状态

```text
created -> persisted -> projection_queued
                   \-> projection_not_configured
```

本地 `persisted` 不依赖平台 projection 成功。

## 9. ScoreProjection

应用/infrastructure adapter 的不可变投影任务。

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `projection_id` | string | 稳定幂等键；由 evidence identity + metric 派生 |
| `platform` | string | 第一阶段固定 `langfuse` |
| `platform_trace_id` | string | 来自真实 OTel/Langfuse mapping，不由 AI ID 猜测 |
| `platform_observation_id` | string? | generation span score 时必填 |
| `evidence` | EvaluationEvidence snapshot | 防御性副本 |
| `attempt` | int | 从 0 开始，有上限 |
| `created_at` | timestamp | UTC |

### 状态机

```text
queued -> sending -> sent
   |         |
   |         +-> retry_wait -> queued
   +-> dropped_queue_full
   +-> failed_permanent
   +-> failed_shutdown_timeout
```

任何失败都不修改 evidence，不阻塞 chat。`sent` 允许因 at-least-once 重试重复请求，但稳定 projection ID 必须更新同一 score。

## 10. PayloadPolicy

| mode | 允许内容 | 禁止内容 | 环境 |
| --- | --- | --- | --- |
| `metadata_only` | hash、length、category、count、status、score | 原始 query/prompt/output/tool args | 全部 |
| `content_redacted` | 通过检测与脱敏后的受控片段 | 密钥、token、已识别 PII、未脱敏原文 | local/test/受批准 production |
| `content_raw` | 普通原文调试内容 | 密钥、token、已识别 PII 仍禁止 | 隔离 local/test，最长 24h |

`is_debug` 只决定响应诊断摘要，不改变 payload mode。

## 11. SmokeRun

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `run_id` | UUID/opaque string | 每次唯一；低敏 |
| `marker` | string | 每次运行唯一、低敏且可查询；与 `run_id` 分离，禁止进入 metrics labels |
| `profile` | enum | `grafana` 或 `signoz` |
| `scenario` | enum | `infra`、`chat`、`score`、`privacy`、`exporter_failure`、`persistent_queue`、`storage_failure`、`score_worker_failure`、`alert`、`retention`、`platform_contract`、`full` |
| `started_at` | timestamp | UTC；查询下界 |
| `request_id` | string? | 请求完成后填充 |
| `ai_trace_id` | string? | chat 场景填充 |
| `deadline` | timestamp | 后端轮询上界 |

run ID 与 marker 可进入 span/log/report，不进入 metrics labels。

## 12. BackendCheckResult / SmokeReport

### BackendCheckResult

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `backend` | enum | api/tempo/loki/prometheus/grafana/langfuse_trace/langfuse_score/signoz |
| `status` | enum | passed/failed/skipped |
| `duration_ms` | integer | 非负 |
| `query_window` | object | started/deadline |
| `evidence` | map | 低敏计数、状态、identity；禁止 payload |
| `failure_stage` | enum | `none`、`preflight`、`api`、`export`、`query`、`cleanup`；成功检查为 `none`，失败检查必须指出真实失败阶段 |
| `error_class` | string? | 稳定分类，不含原始 body |

### SmokeReport

包含 run identity、marker、profile、scenario、总体状态、各 backend results、cleanup status 与版本矩阵摘要。报告一旦生成不可变，不包含 credentials。cleanup 同时记录 smoke 自行创建的临时凭据/secret file 是否已撤销或删除，以及 run 目录、临时 queue 数据和 raw 调试数据是否残留；调用方注入的长期凭据不属于 smoke 可撤销对象。

## 13. ExporterHealthSnapshot

按 Collector component ID 聚合的只读证据：sent、send_failed、enqueue_failed、queue_size、queue_capacity、queue_age、dropped。Tempo、Loki、Langfuse 分别记录。

Prometheus、Grafana 与 score worker 不使用此实体冒充 exporter 证据：

- Prometheus：target `up`、scrape duration/error、counter delta。
- Grafana：datasource health/query result。
- score worker：queued/sent/failed/dropped。

## 14. 数据生命周期

| 数据 | 本地默认保留 |
| --- | --- |
| Prometheus metrics | 15 天 |
| Loki logs | 7 天 |
| Tempo traces | 7 天 |
| Langfuse metadata/redacted traces | 14 天 |
| `content_raw` | 最长 24 小时且独立项目/volume |
| 低敏 eval evidence/report | 90 天 |
| Collector persistent queue | 仅积压期间；发送后清理 |

安全 reset 只删除当前 compose project 的 observability 资源，不删除应用数据库或无关 volume。
