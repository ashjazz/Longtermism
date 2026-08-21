# ADR-0008：真实可观测后端接入与最小 HTTP 观测闭环

**日期**：2026-07-10
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

ADR-0007 已确定双平面观测的领域边界，但尚未决定真实后端、Collector 拓扑、最小 HTTP 闭环和验收方式。项目需要在不让业务代码绑定具体平台的前提下，真实验证基础设施事实与 AI 语义事实能够被关联、查询、评估和恢复。

这项决策同时受三个约束：默认开发门禁必须离线且无付费依赖；真实后端必须具备生产化的队列、失败归因和隐私边界；框架仍应以最小 HTTP 闭环逐步生长，而不是提前实现完整 Agent 功能面。

## Decision（决策）

应用内采用 GoFrame 基础设施自动埋点、OpenTelemetry API/SDK 标准遥测层与 `pkg/ai/obs.Trace` AI 语义事实源的组合；新实现不引入 `opentracing-go`。应用只通过 OTLP 连接 OTel Collector，默认 gRPC，允许 HTTP/protobuf override；业务代码不得直连 Prometheus、Loki、Tempo、Grafana、SigNoz 或 Langfuse trace ingestion。

Collector 采用 `ingress -> forward connectors -> infra/AI downstream pipelines` 的方案 C。AI usecase 在 root/bridge span 设置 `longtermism.observability.plane="ai"`，AI semantic spans 同样携带该标记；纯基础设施请求不进入 AI pipeline。Tempo、Loki 与 Langfuse OTLP exporters 分别拥有 queue/retry/timeout 和 `file_storage` persistent queue；Prometheus 通过 scrape Collector exporter 获取指标，不属于 push queue。

基础设施观测主线采用 `Prometheus + Loki + Tempo + Grafana`，并提供 `observability-grafana` 本地 profile、provisioned dashboards 和 alert rules。`SigNoz` 作为实施优先级较后的备选基础设施 profile；它只替换 logs/metrics/traces 后端，Langfuse 始终保留为 AI trace/generation/score 后端。Langfuse score 由应用内有界异步 adapter 投影，本地 eval evidence 仍是事实源。

首个真实业务闭环是非流式 `POST /api/v1/chat`，接入服务端配置的 OpenAI-compatible GPT-5.5；另提供受 smoke 开关保护的 `GET /api/v1/observability/infra-smoke` 来验证纯基础设施路由。chat 始终返回 `request_id` 和 opaque `ai_trace_id`，仅 debug 返回有界低敏 `eval_summary`。

验证分层：`obs-smoke-offline` 保护默认离线契约；新增 `obs-platform-smoke` 以本地受控 sender 验证最小平台接入 payload、显式启用和隐私边界，不连接真实后端；`obs-grafana-e2e`、`obs-resilience-e2e` 与 `obs-signoz-e2e` 才验证真实后端接收、查询和恢复。真实平台验证必须采用唯一 marker、限定时间窗口并产生机器可读报告。

## Alternatives Considered（备选方案）

### 方案 1：应用直接对接各后端或平台 SDK

- **优点**：初期可快速看到各平台数据，平台特性调用直接。
- **缺点**：业务代码被 backend endpoint、SDK 类型和多条失败路径污染，切换后端与测试成本高。
- **未采用原因**：违反 `pkg/ai/obs` 作为事实源、Collector 作为出口边界的分层原则。

### 方案 2：只使用 SigNoz 作为统一后端

- **优点**：本地部署与 UI 更一体化，三信号接入路径较短。
- **缺点**：无法替代 Langfuse 的 generation、score 与后续评估工作流，也减少了对经典组件职责边界的学习。
- **未采用原因**：SigNoz 保留为受支持备选；Grafana 经典栈更适合作为当前主线，Langfuse 仍独立负责 AI 平面。

### 方案 3：单一 Collector pipeline 向所有后端导出

- **优点**：配置数量少，最容易启动。
- **缺点**：纯基础设施 span 会误入 Langfuse，两个平面的字段过滤、采样、队列和失败归因无法独立。
- **未采用原因**：选择 ingress 加两个 downstream pipelines，显式表达 infra/AI 路由边界。

### 方案 4：只保留默认离线 smoke 或只运行完整真实 E2E

- **优点**：前者稳定、后者最接近生产。
- **缺点**：前者不能验证平台接入契约，后者慢且受 Docker、凭据和付费服务影响。
- **未采用原因**：新增轻量 `obs-platform-smoke` 填补两者之间的验证层，但明确它不替代真实 E2E。

## Consequences（影响）

### 正面影响

- 应用核心保持对后端和 Langfuse tracing SDK 无感，平台替换集中在 Collector 与 adapter。
- 一次 chat 可以关联 HTTP、日志、指标、AI trace、generation、eval evidence 与 score；纯基础设施端点可反证不会误路由到 AI 平面。
- 本地轻量 smoke 可在无 Docker 和无凭据环境快速保护平台接入契约，真实 E2E 则保留严格的后端查询证明。
- queue、告警、retention、故障归因和清理要求在首次真实接入前已经成为可验收约束。

### 负面影响

- 需要维护两套 compose profile、Collector 配置、dashboard/checklist 和更细的失败证据。
- Collector fan-out 与 persistent queue 增加部署、磁盘容量和恢复测试复杂度。
- `obs-platform-smoke` 与真实 E2E 的边界必须持续清楚，否则容易出现本地 sender 通过却误判真实后端可用。

### 风险与缓解

- **高基数与隐私泄露**：request/run id 不进入 metrics labels；payload policy、baggage allowlist、应用内过滤与 Collector 二次过滤共同约束。`content_raw` 仅在显式授权的 local/test 生成不可序列化的本地调试工件；进入观测后端的仍只能是 metadata-only 或经脱敏的受控内容。
- **观测故障影响业务**：exporter、score worker 和 queue 失败必须被单独指标记录，但不得改写 chat 或 infra-smoke 的业务结果。
- **积压或数据丢失**：Tempo/Loki/Langfuse 使用 persistent queue；验收覆盖 backend pause、Collector restart、queue 满、磁盘不可写和 shutdown timeout。语义是 at-least-once，不承诺 exactly-once。
- **真实平台门禁不稳定或产生费用**：默认 PR 只跑离线门禁；真实 E2E 在首个阶段验收及相关配置变更、release candidate 中显式运行。

## Revisit Conditions（重新审视条件）

- GoFrame contrib 无法表达必要的 OTel resource、采样、fan-out 或测试替身配置。
- Langfuse OTLP 不能稳定表达所需 generation 或 score 关联，或官方 Go SDK 成熟且不会污染核心契约。
- 单进程 score worker 的丢失窗口、吞吐或恢复需求要求演进为 outbox/外部 worker。
- 多服务部署、吞吐或隔离要求使单 Collector 或本地 persistent queue 不再适用。
- 需要支持流式 chat、tool/MCP、RAG、sub-agent 或上下文压缩时，新增行为不能在现有 trace/eval 语义内清晰表达。
- SigNoz 支持成熟度、Grafana stack 运维成本或后端能力变化改变主线与备选的取舍。

## Implementation Appendix（T165）

**记录日期**：2026-08-21
**性质**：accepted ADR 的 append-only implementation note

本附录不改写上述 accepted 决策，只把 feature 003 当前仓库中能够由实现和测试复验的结果、相对
原决策的偏差以及后续重新决策门槛记录下来。这里的 revisit condition 不是已验证结果；未取得 live
报告的能力继续保持待验收。

### 实施结果（仅已验证事实）

| 范围 | 当前仓库事实 | 证据强度 |
| --- | --- | --- |
| 双平面路由 | 真实 HTTP root 属于 infra 平面；应用拥有的 `ai.chat` bridge 与显式 AI semantic spans 才携带 `longtermism.observability.plane=ai` 和 `longtermism.ai.designated=true`。HTTP/DB/Redis 子 span 不按名称猜测 AI 归属 | Go contract/unit tests |
| 运行配置 | 应用唯一 backend endpoint 仍是 Collector；Grafana 与 SigNoz profiles、稳定 Collector component IDs、固定镜像 tags 和资源预算均有静态门禁 | config/compose contract tests；不等于 live 后端通过 |
| Chat/model | chat 默认关闭，默认 model 为空；启用时必须由服务端配置 OpenAI-compatible provider/model 并 fail-fast，不能把原决策中的 GPT-5.5 当作仓库默认事实 | composition tests |
| Logs | completion logs 通过应用 OTel Logs SDK 和 Collector OTLP pipeline 发送；JSONL 只保留为本地诊断工件，不是 Loki 验收生产路径 | mapper/Collector config tests |
| Score projection | 本地 eval evidence 先持久化；projection 初始快照在进入有界内存 queue 前写入本地 store，每个发送、重试和终态转换在下一次外部副作用前同步更新 | score store/worker/lifecycle tests |

上述事实是离线或静态验证结果。Level 2、Level 3 与 Level 4 live E2E 仍待验收；代码、fake
backend、Compose 解析或历史报告都不能替代本次唯一 run/window 的 schema-valid report。尤其是 score
worker 的 queue-full、shutdown-timeout smoke 当前属于离线契约/capability sentinel，不是 live
生产恢复证明。

### 偏差与未验证边界

1. 原文把 request root/bridge 一并描述为 AI 路由边界；最终实现保留 HTTP root 为 infra，只标记应用
   拥有的 `ai.chat` bridge 和显式 AI semantic spans。该差异收紧了 Langfuse 输入面，没有改变
   “业务事实不绑定平台”的 accepted 原则。
2. 原文记录 OpenAI-compatible GPT-5.5；最终 manifest 不提供 model 默认值。真实运行必须显式配置
   provider/model，因此附录不把某个可替换模型写成已验证能力。
3. 初始工作台把 score worker 描述为仅有进程内队列；T184 增加了本地持久化 projection store 与
   startup recovery，收窄了普通进程重开时的丢失窗口。但它不是 transactional outbox，也不提供跨
   主机接管或多消费者协调。
4. 当前 image matrix 固定的是 tags，不是 immutable digests；SigNoz 端口/query、retention 生效、
   完整 Grafana/SigNoz/score live E2E 仍需真实验收。附录不把静态声明升级为运行事实。
5. 当前安全 runbook 把 destructive reset 与 direct credential curl 隔离为不可执行能力；这属于已知
   运维缺口，不能因为目标存在就声称数据销毁或 direct diagnostic 已安全验证。

### Score worker 当前可靠性边界

当前路径按以下顺序执行：local evidence append 成功 → 校验唯一 evidence → `SaveInitial` 保存
`queued` projection → 非阻塞内存 queue admission。sending/retry_wait/重新 queued 在下一次发送前同步
`Update`；sent/permanent/shutdown terminal 在取得结果后同步 `Update`。稳定 `ProjectionID` 让重放请求携带同一幂等目标；
真实 Langfuse 是否合并或更新仍待 live E2E 证明，该 ID 不等于 exactly-once delivery。

- 默认 queue capacity 为 64，可配置，代码硬上限为 4096。queue 满时 worker 立即返回
  `dropped_queue_full` 且不阻塞 chat；只有 lifecycle 的 terminal `Update` 成功时它才成为不会由
  `LoadPending` 自动重投的已持久化终态。若该 `Update` 失败，调用方得到持久化错误，store 仍可能
  保留并在重启时恢复先前的 `queued` 快照。
- worker 使用有界重试和指数退避；默认 score request timeout 为 10s，chat runtime 关闭 score
  lifecycle 的总预算上限为 60s。超时关闭会尝试写入 `failed_shutdown_timeout`；只有 terminal `Update`
  成功时它才成为不会由 `LoadPending` 自动重投的终态。独立 1s 持久化预算也可能失败，此时 worker
  停止当前转换，store 可能保留先前的 `sending`/`retry_wait` pending 快照；worker callback 契约能分类
  `projection_persistence_failed`，但当前 production lifecycle 尚未把该 callback 接入指标。
- 本地 `ScoreProjectionStore` 使用 private regular file、进程内 gate、`flock`、临时文件同步后 rename
  与目录 fsync；文件上限 8 MiB，最多保留 1024 条 terminal history。它可以恢复同一存储上的
  `queued`、`sending`、`retry_wait`；`sending` 以稳定 ID 归一为 queued 后重放。
- evidence JSONL 与 projection JSON 是两个独立存储，不在同一事务中。进程可能在 evidence 已落盘、
  projection 尚未 `SaveInitial` 时退出；事实仍在，但平台投影不会被当前 store 自动补建。
- `flock` 只保护文件读写，没有 claim、lease、visibility timeout 或 partition ownership。多个进程共享
  文件时可能同时恢复并消费；使用独立本地盘时又不能接管其它实例的 pending。稳定 ID 只能降低平台
  重复对象风险，不能证明分布式消费正确。
- 当前 telemetry 只有 queue depth 与 coarse projection telemetry；`queued` counter 还包含 retry 后
  重新入队，不能作为纯新任务 `enqueue_rate`。queue age、稳定吞吐容量和 retry backlog
  尚未被当前指标直接证明；持久化的 `CreatedAt` 也尚未暴露为 `oldest_queue_age` instrument。

### Score worker 演进阈值

下表采用的是后续架构审查门槛，而非已发生或已测得的生产结果。达到任一条件时停止通过“调大 queue/
timeout”延续当前设计，进入新 ADR：

| 触发域 | Adopted revisit condition | 当前设计为何不足 | 新 ADR 的默认评估方向 |
| --- | --- | --- | --- |
| 多进程部署 | 在 chat/API `replica_count > 1`、滚动部署需要实例重叠或 score 需要独立扩缩容之前触发 | 本地 channel 与文件没有跨进程任务 ownership；共享盘和独立盘都不能提供单消费者接管 | transactional outbox + 外部 worker；定义 claim/lease、visibility timeout 与分区 ownership |
| 持久化/SLO | 一旦要求跨主机持久化、主机丢失后接管、evidence 与 projection 原子提交，或平台 score 投递成为生产 SLO | 当前两个本地文件之间存在 crash window，且没有 HA storage/backfill owner | 单进程但需要原子性时先评估 outbox；需要跨节点恢复时评估 outbox + durable broker/外部 worker |
| Shutdown 丢失窗口 | 非故障注入运行出现任一 `failed_shutdown_timeout`/`dropped_queue_full`、terminal persistence error，或业务要求零 shutdown loss window | terminal `Update` 成功后当前 restart recovery 不会重投；Update 失败又会留下结果不确定的旧 pending。只增大 60s/4096 会掩盖容量、持久化与所有权问题 | outbox 保留可重放事实；外部 worker 与应用 shutdown 解耦 |
| Queue age / 吞吐 | 先补独立 arrival、sent、`oldest_queue_age` instruments；随后任一 oldest pending 超过 120s，或连续 3 个 5m 窗口 `enqueue_rate > sustainable_send_rate`，或 depth 持续达到 capacity 的 80% | 当前 queued counter 混入 retry，不能直接证明 arrival rate；没有 age 或可持续服务率 SLO | 外部 worker 容量隔离、水平扩展、背压与容量基准；保留 outbox 原子入口 |
| 重试积压 | 先补 `retry_wait` backlog/age；随后 backlog 连续 3 个 5m 窗口增长不回落，或正常流量出现 retry exhausted/`failed_permanent` | 当前 coarse counter 不能回答仍在重试的数量、年龄和恢复速度 | durable retry schedule、DLQ、限流/熔断与可审计 backfill，由外部 worker 拥有 |

这里的 120s 取自现有 score smoke 查询窗口，只作为首次 revisit 门槛，不代表已建立生产 SLO；3×5m、
80% 同样是 adopted review thresholds。正式采用前必须先补 instrument、基线与真实 canary，再在新 ADR
中按容量测试结果调整。

### ADR 治理边界

本附录只解释实现现状，不接受新的分布式架构。达到任一阈值时，架构变化必须新增 ADR，并以
`amends ADR-0008` 或 `supersedes ADR-0008` 建立链接；不得静默改写本 ADR 的 accepted Decision、
历史 Alternatives 或当时的约束。

新 ADR 至少要定义：evidence/outbox 的事务原子边界、任务 ownership/lease、稳定幂等与重复投递、
retry/DLQ/backfill、容量与 queue-age SLO、多租户隔离、隐私/retention、灰度迁移、回滚和 live 验收。
在新 ADR accepted 且迁移门禁通过前，当前 score worker 仍限定为单应用进程、单机持久化；平台
projection 可以丢失，但已经成功 append 并完成 fsync 的本地 evidence 仍作为事实源保留。

### 实现证据与复验入口

以下引用证明的是 repository contract，不是 live backend evidence：

- `internal/eval/score_projection_store_test.go::TestScoreProjectionStorePersistsOneCurrentSnapshotPerRun`
- `internal/cmd/langfuse_score_lifecycle_test.go::TestLangfuseScoreLifecyclePersistsInitialSnapshotBeforeAdmission`
- `internal/cmd/langfuse_score_lifecycle_test.go::TestBuildLangfuseScoreLifecycleRecoversPendingBeforeStart`
- `internal/observability/langfuse/worker_test.go::TestScoreWorkerReliablyRecordsRetrySequence`
- `internal/observability/langfuse/worker_test.go::TestScoreWorkerShutdownMarksUndrainedProjectionsWithoutDeletingEvidence`
- `internal/observability/smoke/score_failure_runner_test.go::TestRunScoreWorkerFailureSmokeQueueFullDropsProjectionSafely`

复验命令：

```bash
go test -race ./internal/eval ./internal/observability/langfuse ./internal/cmd ./internal/logic/chat
go test ./internal/observability/smoke -run '^TestRunScoreWorkerFailureSmoke' -count=1
```

## References（参考）

- ADR-0006：可观测平台 Adapter 边界。
- ADR-0007：双平面观测与评估体系 v1。
- [真实可观测后端接入决策工作台](../observability/08-real-backend-decision-workbench.md)。
- [真实后端规格质量清单](../../specs/003-real-observability-backends/checklists/requirements.md)。
- [真实后端实施验收清单](../../specs/003-real-observability-backends/checklists/real-backend-acceptance.md)。
