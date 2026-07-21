# Grafana 主线基础设施验收清单

**范围**：User Story 1 的 Grafana 主线。本文是验收运行的记录模板，不是一次真实运行的
结果；除明确标记的 provisioned 资产外，所有结论必须由某一次机器可读报告证明。

## 通过规则

- 仅 `scenario=infra`、`profile=grafana`、`status=passed` 且符合
  [`smoke-report.schema.json`](../contracts/smoke-report.schema.json) 的报告，可关闭 SC-001。
- 报告的 `run_id`、`marker`、开始/结束时间、每项 `backend/status/failure_stage/duration_ms`
  与 `cleanup` 必须存在；任何 failed 或 skipped check 都不能以其它 backend 的通过抵消。
- 证据目录应只记录报告文件的相对路径、生成时间与不可变 run ID；不得记录 credential、
  OTLP header、backend 原始 response、prompt 或截图。Grafana/Langfuse UI 截图只能辅助诊断，
  不能单独关闭任何 checkbox。
- `make obs-stack-health`、Compose healthcheck、静态配置测试和 dashboard provision 成功只说明
  前置条件或资产存在；它们不等同于 E2E 通过。

## 验收运行记录

| 字段 | 本次证据 |
| --- | --- |
| 执行日期/操作者 | 待填写 |
| 命令 | `make obs-infra-smoke` |
| 报告相对路径 | 待填写：`build/observability/smoke-reports/<run>.json` |
| run_id / marker | 待填写（只写报告中已有的低敏身份） |
| schema 版本 | 待填写（当前契约为 `2`） |
| 总体状态 | 待填写；只有 `passed` 可关闭 SC-001 |

## SC-001：基础设施三信号与 AI 负向路由

**成功标准**：健康的主线环境中，纯基础设施验证请求的服务链路、结构化日志和聚合指标能在
60 秒内按本次身份定位，且 AI 平面无对应记录。

- [ ] 报告为 `profile=grafana`、`scenario=infra`、`status=passed`，且 `finished_at - started_at <= 60s`。
- [ ] `api` check 为 `passed`，证明受保护 infra 请求被实际触发；不以容器 health 替代。
- [ ] `tempo` check 为 `passed`，证据仅含 `matched_spans`，证明本次 marker 的服务链路可查。
- [ ] `loki` check 为 `passed`，证据仅含 `matched_logs`，证明本次 marker 的结构化 JSON 日志可查。
- [ ] `prometheus` check 为 `passed`，`metric_delta > 0`，证明 route/status 聚合指标相对基线增长；
  不要求也不允许 request/run/trace ID 成为 metric label。
- [ ] `langfuse_trace` check 为 `passed` 且 `matched_traces = 0`，证明 infra-only 请求没有进入 AI 平面。
- [ ] `collector` check 为 `passed` 且 `marker_received = 0`，证明 AI downstream filter 没有收到该 marker。
- [ ] 报告没有 credential、Authorization、原始 payload 或平台 response；若失败，保留 schema-valid
  failed report 并按 `failure_stage/error_class` 诊断，不能用重跑后的一次通过覆盖失败事实。

**当前状态**：待首次真实 `obs-infra-smoke` 报告。当前静态/离线测试和 Compose 配置不能关闭
SC-001。

## SC-006：告警资产与后续 firing/resolved 验证

**成功标准**：服务错误、投递失败、积压压力、本地存储压力四类告警各有一次触发与恢复证据。

| 告警类别 | Provision 状态 | 真实触发/恢复证据 | 当前结论 |
| --- | --- | --- | --- |
| 服务错误 | 已 provision | 待 US3 `scenario=alert` 机器可读报告 | **已 provision、待 US3 firing/resolved 验证** |
| Collector 投递失败 | 已 provision | 待 US3 `scenario=alert` 机器可读报告 | **已 provision、待 US3 firing/resolved 验证** |
| 持久队列积压/年龄压力 | 已 provision | 待 US3 `scenario=alert` 机器可读报告 | **已 provision、待 US3 firing/resolved 验证** |
| 本地存储压力 | 已 provision | 待 US3 `scenario=alert` 机器可读报告 | **已 provision、待 US3 firing/resolved 验证** |

- [x] 四类规则已由 `deploy/observability/grafana/alerts/observability.rules.yaml` provision；这是资产
  存在结论，不代表规则已在真实 Grafana 中 firing/resolved。
- [ ] 每一类告警均有一次受控故障注入产生的 `scenario=alert` schema-valid 报告，包含对应的
  firing 机器证据。
- [ ] 同一受控故障恢复后，每一类告警均有恢复机器证据；不能只用 Grafana UI 截图或手动观察。
- [ ] 报告可将 alert 状态关联到稳定 Collector component ID、run marker 与 failure stage，同时
  不暴露 credential、原始 payload 或 query response。

**当前状态**：SC-006 不能在 US1 关闭；T062 仅完成规则 provision。其 firing/resolved E2E
验收由 User Story 3 的故障与恢复任务承担。

## 不能作为通过证据的项目

- Docker container 的 `healthy`、`docker compose ps` 或 Grafana datasource provision 成功。
- 单独的 `make verify`、`make obs-contract`、静态 YAML/Compose 校验或 fake-Docker 测试。
- 手工页面截图、浏览器搜索结果、未包含唯一 marker 的历史数据。
- 未通过 schema 验证、超过 60 秒窗口、缺任一 required check，或只声称“看起来正常”的输出。
