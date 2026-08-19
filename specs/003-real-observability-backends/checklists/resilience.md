# 故障恢复与告警验收矩阵（Resilience）

**范围**：User Story 3。本文是 SC-003（8+ 故障域）、SC-004（跨重启恢复）与
SC-006（四类告警触发/恢复）的验收运行记录模板，不是一次真实运行的结果；
除 provisioned 资产外，所有结论必须由某一次机器可读报告证明。

## 通过规则

- 每行场景必须有一份 `scenario` 匹配、`profile=grafana` 且符合
  [`smoke-report.schema.json`](../contracts/smoke-report.schema.json) 的报告；
  报告 `run_id`、`marker`、`started_at/finished_at`、每项 check 与 `cleanup` 必须存在。
- 证据源列必须与失败域目录（`internal/observability/failure/catalog.go`）一致：
  推送失败用 Collector 组件遥测，scrape 失败用 target 遥测，score worker 用 worker 遥测，
  模型失败用 provider 响应——任何行不得把 exporter 失败与 pull/query/业务失败混用证据。
- 恢复窗口是机器可判读的上界（报告内 query window/deadline），超过窗口视为该行失败。
- 任何 failed 或 skipped check 都不能以其它 backend 的通过抵消；`cleanup=failed`
  的行不得关闭。
- 静态规则/资产检查（grafana_alerts_test、config_check）只能证明前置条件，
  不能关闭任何行；`make obs-stack-health` 同理。

## 故障场景验收矩阵

> 注入方式列描述受控故障；「业务断言」列必须是运行期内可机器比较的事实。
> 当前状态：exporter-failure（3 目标）、persistent-queue 与 score-worker langfuse-api
> case 的 live composition 已收敛（T130：otelcol 快照后端 + DockerControl 注入 +
> 受保护 trigger + warm-up 动态投影身份解析）；queue-full 与 shutdown case 仍受
> 能力哨兵约束（进程内通道无受控实现）。对应行在取得真实报告前保持未勾选。

| # | 场景（scenario） | 注入方式 | 业务断言 | 证据源（与 T120 目录一致） | 恢复窗口 | cleanup 断言 | 关联 SC | 状态 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | exporter_failure（tempo 出口） | `docker compose -p <project> pause tempo` | 故障前/故障中 HTTP status 与 body hash 一致（业务结果不改写） | collector component telemetry：`otlp/tempo` send_failed delta > 0、queue delta；loki/langfuse failed_delta = 0 且至少一个出口 sent_delta > 0 | 注入期 ≤ 120s | unpause 成功；失败则报告 `paused-service` residual | SC-003 | - [ ] |
| 2 | exporter_failure（loki 出口） | pause loki 服务 | 同上（HTTP 结果不变） | `otlphttp/loki` send_failed delta > 0；tempo/langfuse 继续投递 | ≤ 120s | unpause；`paused-service` residual 兜底 | SC-003 | - [ ] |
| 3 | exporter_failure（langfuse 出口） | pause langfuse-web | 同上（HTTP 结果不变） | `otlphttp/langfuse` send_failed delta > 0；tempo/loki 继续投递 | ≤ 120s | unpause；`paused-service` residual 兜底 | SC-003 | - [ ] |
| 4 | persistent_queue（跨重启恢复） | pause tempo → 产生 marker 流量 → restart otel-collector → unpause | 中断期间产生的待发送记录在恢复窗口内可查询（marker 命中） | collector queue snapshot：积压高水位 > 0、drain sent delta ≥ 积压；duplicate_delivered 如实记录（at-least-once，不宣称 exactly-once） | drain ≤ 120s（`PersistentQueueDrainWindow`） | unpause；迟到 marker 隔离 | SC-004 | - [X] |

> 行 4 证据：三份真实 passed 报告（首份
> `persistent_queue-1787104476021357000.json`，2026-08-19），journal 0013。
> 边界：跨重启时 sent 会计不可比，`duplicate_delivered=0` 是"不可推导、不宣称"
> 而非"证实为零"；duplicate>0 的观察需单进程连续计数窗口，记为后续实验。
| 5 | storage_failure / queue_exhaustion | 有界流量逼近队列容量 | 不要求业务失败 | collector queue snapshot：queue_size ≥ capacity 且 dropped/enqueue_failed delta > 0 | deadline 有界 | 无注入残留 | SC-003 | - [ ] |
| 6 | storage_failure / unwritable_disk | 使持久队列 storage 挂载不可写 | 不要求业务失败 | collector storage error：`storage_writable=false` 且 enqueue_failed delta > 0 | 恢复 ≤ 120s 且恢复后 `VerifyCollectorHealthy` | 恢复写入能力；失败则 `unwritable-storage` residual | SC-003 | - [ ] |
| 7 | storage_failure / shutdown_timeout | 阻塞 Collector 停机超过 grace period | 不要求业务失败 | collector lifecycle：`shutdown_timed_out=true` 且 dropped delta > 0 | 重启 ≤ 120s 且 verify healthy | 重启成功；失败路径仍 verify | SC-003 | - [ ] |
| 8 | score_worker_failure / langfuse_api | 使 Langfuse API 对 score worker 不可达 | 故障期 chat 响应与基线一致；本地 eval evidence digest 不变 | score worker telemetry：projection 恢复 `sent`、`score_attempts` ≥ 1、`PlatformScoreCount = 1`（幂等） | 投影恢复 ≤ 120s（`ScoreWorkerFailureRecoveryWindow`） | 恢复平台；失败则 `langfuse-api-unavailable` residual | SC-003 | - [ ] |
| 9 | score_worker_failure / queue_full | 制造 score worker 队列满 | chat 响应不变；本地 evidence 完整 | score worker telemetry：`dropped_queue_full` 降级事实入报告（`dropped_projections` ≥ 1、`matched_scores = 0`） | ≤ 120s | 清空队列；失败则 `score-worker-queue-full` residual | SC-003 | - [ ] |
| 10 | score_worker_failure / shutdown | 阻塞 worker 停机 | chat 响应不变；evidence 完整 | score worker telemetry：`shutdown_timed_out=true`、重启后 projection `sent` | ≤ 120s | 重启成功 | SC-003 | - [ ] |
| 11 | 模型上游故障（分类边界） | provider 返回 429/5xx/timeout（受控 stub 或真实降级） | 失败按业务错误语义呈现（429/502/504），ai_trace_id 保留 | provider response：业务错误类 `rate_limited/upstream_unavailable/upstream_timeout`；任何观测证据源不得冒充 | 无恢复窗口（业务失败） | 无 | SC-003 | - [ ] |
| 12 | 告警四类 firing/resolved（见下表） | 按告警类别注入对应条件 | 告警触发与恢复都不改写业务结果 | Grafana/Prometheus 告警状态查询（时间证据），禁止 rule 文件存在性冒充 | 每类 firing 与 resolved 各 ≤ 120s | 恢复告警条件；失败则 `alert-condition-active` residual | SC-006 | - [ ] |

## SC-003 关闭条件

- [ ] 上表第 1-11 行中至少 8 行取得 schema-valid 报告且对应 check 全部 passed；
- [ ] 所有已执行行中「观测故障导致业务结果被改写」的计数为 0（api check 与
      chat hash/status 断言共同证明）；
- [ ] 每一行报告中的证据源与 T120 失败域目录一致（由 `obs-config-check` 与
      runner 契约测试持续守护）。

## SC-004 关闭条件

- [ ] 第 4 行报告证明：中断积压 > 0 → 重启 → 120s 内 marker 可查询；
- [ ] 报告显式声明 at-least-once（`PersistentQueueDeliveryGuarantee`），
      `duplicate_delivered` 证据存在（可为 0），不得宣称 exactly-once；
- [ ] drain 超时或 duplicate 缺失时该行保持失败，不得静默关闭。

## SC-006 关闭条件（四类告警）

| 告警类 | 规则 uid | 触发证据 | 恢复证据 | 状态 |
| --- | --- | --- | --- | --- |
| 服务错误（http） | `longtermism-http-error-rate` | 一次 `scenario=alert` 报告中该类 firing 时间证据 | 同次或对应报告中该类 resolved/normal 时间证据 | - [ ] |
| 投递失败（exporter） | `longtermism-exporter-delivery-failure` | 同上 | 同上 | - [ ] |
| 积压压力（queue） | `longtermism-exporter-queue-saturation` / `-queue-age` | 同上 | 同上 | - [ ] |
| 本地存储压力（storage） | `longtermism-collector-storage-pressure` | 同上 | 同上 | - [ ] |

- [ ] 四类告警**全部**取得 firing 与 resolved 证据后，SC-006 才能关闭；
      任何一类缺失都不允许部分关闭。
- [ ] 告警规则必须先通过 T132 校准后的指标名契约测试（真实指标，非伪造名）；
      规则文件存在、静态语法检查或 UI 截图不得作为关闭依据。

## 已知收敛缺口（不视为失败）

- exporter-failure、persistent-queue 与 score-worker langfuse-api case 的 live
  composition 已于 T130 收敛（快照/组合后端 + DockerControl 注入 + 受保护 trigger；
  score-worker 的投影身份经 warm-up chat + FindByProjectionID 动态解析，平台幂等
  事实经 ScoreCountByID 精确查询）。
- score-worker 的 queue-full（进程内队列填充）与 shutdown（宿主进程内 worker 停机）
  case 没有可用受控通道：默认装配以 `errResilienceCapabilityUnavailable` fail-fast
  （禁止伪造证据）。该缺口关闭前，矩阵第 9-10 行保持未勾选——事实缺失不被猜测为通过。
