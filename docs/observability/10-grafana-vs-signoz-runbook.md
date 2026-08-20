# Grafana 主线 vs SigNoz 备选：能力、成本与选择边界 Runbook

**最后更新**：2026-08-20
**状态说明**：本文件区分三个证据层级——**静态契约**（`make obs-config-check` 与各 hack 契约测试，当前全绿）、**离线行为**（Go 单测/fake runner，当前全绿）与**真实 E2E**（Level 4，需要 Docker 与真实后端）。当前**没有任何一条真实 E2E report**：两 profile 的 E2E 结论位都保持"待首跑"状态，只有在 `build/observability/smoke-reports/` 下取得 schema-valid report 后才可填补引用。在填补之前，本文不宣称任何一方的端到端可用性，也不宣称两者功能等同。

## 1. 选择边界

| 决策问题 | 结论 |
| --- | --- |
| 默认选谁？ | **Grafana 主线**。首批完整资产（dashboard、alert、恢复与隐私证据）都在主线；备选 profile 的通过证据不改变主线优先级。 |
| 什么时候考虑 SigNoz？ | 主线验收完成后，偏好一体化（三信号同库、单一 query 入口、更少组件）的本地部署场景。 |
| 两者能否同时跑？ | **不能**。各自使用独立 compose project（`longtermism-observability` vs `longtermism-signoz`），Langfuse 栈在各自 project 内独立实例化、观测卷互不可见；同时运行会重复占用 12GiB 级预算。 |
| 切换 profile 要改应用吗？ | **不要**。应用只连接 OTel Collector（4317/4318），receiver、脱敏、marker、HTTP 契约与 report schema 完全一致；切换是部署层选择（`LONGTERMISM_SMOKE_PROFILE` + `--profile`）。 |

## 2. 能力矩阵（非等同声明）

| 能力面 | Grafana 主线 | SigNoz 备选 |
| --- | --- | --- |
| infra 三信号存储/查询 | Prometheus / Loki / Tempo 分立 | SigNoz（ClickHouse 单栈，`signoz-otel-collector` 摄取） |
| 首屏 dashboard | `grafana/dashboards/observability-overview.json`（provisioned） | `signoz/dashboard.json`（等价运营问题，独立资产；导入方式待 E2E 校准） |
| 告警资产 | `grafana/alerts/observability.rules.yaml`（已落地） | **无对应资产**——备选 profile 不提供告警规则，故障发现依赖主线告警或手动查询 |
| AI trace / score 投影 | Langfuse（共享同一 `compose.langfuse.yaml`） | Langfuse（同上；`langfuse_score` 在备选 chat E2E 中是独立 check） |
| score/privacy 深度证据 | `obs-langfuse-score-smoke`、`obs-privacy-platform-smoke` | **不支持**——这两类证据面绑定主线后端组件，备选 CLI 直接拒绝（usage failure） |
| infra-only AI 负向 | 支持（`collector`/`langfuse_trace` 零命中） | 支持且语义一致（先见正向再证负向） |
| metrics 摄取路径 | Collector pull exporter（8889）→ Prometheus scrape | Collector OTLP push → `signoz-otel-collector`（本 profile 无 Prometheus） |
| UI 入口 | Grafana `127.0.0.1:3000` | SigNoz `127.0.0.1:3301`（应用 API 健康端点为容器内 8080） |

**明示差异（不宣称等同的部分）**：备选 profile 没有告警资产、没有 score/privacy 深度验收入口、dashboard 导入路径与 SigNoz filter 语法的第一手校准都依赖真实 E2E；`signoz-otel-collector` 使用无 shell 的官方最小镜像（官方文档明确不可配置 CMD-SHELL healthcheck），其健康性由查询闭环证明而非容器探测。

## 3. 成本对照

两 profile 共享同一预算声明（`x-observability-budget`：8 vCPU / 12 GiB / 20 GiB volumes），但结构不同：

| 维度 | Grafana 主线 | SigNoz 备选 |
| --- | --- | --- |
| compose 服务数 | 17（collector + 4 后端 + probes/init + Langfuse 栈 8） | 14（collector + signoz + signoz-otel-collector + clickhouse + probes/init + Langfuse 栈 8） |
| 预算分配依据 | `deploy/observability/compose.grafana.yaml` 逐服务 limits | `deploy/observability/compose.signoz.yaml` 逐服务 limits（SigNoz 侧 3.35 vCPU / 4625 MiB） |
| 预算求和门禁 | `compose_grafana_test.sh` 静态求和 ≤ 8 / 12GiB | `compose_signoz_test.sh` 静态求和 ≤ 8 / 12GiB |
| 实测峰值 | **待 E2E**（首跑后记录） | **待 E2E**（首跑后记录） |

## 4. 故障证据路径

- **分出口归因**：两 profile 的 Collector 都为每个 exporter 维护独立持久队列（主线 `queue/{tempo,loki,langfuse}`；备选 `queue/{signoz,langfuse}`）与独立 `otelcol_exporter_send_failed_*` 指标——infra 与 AI 投递故障互不掩盖，这在两份 dashboard 的 "Collector Export Failures" 面板中都有分出口视图。
- **失败分类**：smoke report 的 error_class 集合两 profile 共享（`backend_timeout` / `authentication_failed` / `malformed_response` / `backend_unavailable` / `invalid_query` / `marker_missing` / `identity_mismatch` / `metric_delta_missing` / `unexpected_evidence`；备选额外有 `score_projection_missing`）。
- **清理边界**：两 profile 的 `down` 都不带 `-v`（保留低敏报告与故障证据卷）；备选清理严格限定 `longtermism-signoz` project，由 `make_signoz_e2e_test.sh` 的 fake 工具测试守护。
- **报告位置**：`build/observability/smoke-reports/`（git 忽略目录），schema 由 `contracts/smoke-report.schema.json` 固定。

## 5. 验证路径与证据状态

| 层级 | 入口 | 当前状态 |
| --- | --- | --- |
| Level 0 静态契约 | `make obs-config-check`、`bash hack/observability/compose_signoz_test.sh` 等 | **全绿**（T136-T142 契约与实现咬合） |
| 离线行为 | `go test ./internal/observability/... ./cmd/obs-smoke/...` | **全绿**（T138/T139/T143/T144/T147） |
| Level 4 真实 E2E | `make obs-grafana-e2e` / `make obs-signoz-e2e` | **待首跑**——见下方引用位 |

**E2E 结论引用位（首跑后填写，格式：report 路径 + 日期 + 关键 check 状态）**：

- Grafana 主线：`（待 obs-grafana-e2e 首份 schema-valid report）`
- SigNoz 备选：`（待 obs-signoz-e2e 首份 schema-valid report；预期校准点：SigNoz filter 语法对 `longtermism.smoke.run_id` attribute key 的存储投影、dashboard 导入、UI 端口 3301 的实际映射）`

在引用位填补之前，任何"备选 profile 已验证可用"的说法都不成立。

## 6. 运维命令速查

| 操作 | 主线 | 备选 |
| --- | --- | --- |
| 启动 | `make obs-grafana-up` | `make obs-signoz-up`（wait-timeout 240s，ClickHouse 冷启动更慢） |
| 停止（保留证据） | `make obs-grafana-down` | `make obs-signoz-down` |
| infra smoke | `make obs-infra-smoke` | `make obs-signoz-infra-smoke` |
| chat smoke | `make obs-chat-smoke` | `make obs-signoz-chat-smoke` |
| 端到端 | `make obs-grafana-e2e` | `make obs-signoz-e2e`（up → infra → chat；不含 score/privacy） |
| 破坏性重置 | `make obs-reset CONFIRM_RESET=1` | **无独立 reset**——备选清理即 `obs-signoz-down`；跨 project 的残留用主线 `obs-reset` 时注意其操作的主 project 范围 |

备选 smoke 的环境预检引用（变量名，不含值）：`LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL`（必需）、`LONGTERMISM_SMOKE_SIGNOZ_INGESTION_KEY`（可选，仅查询认证）、`LONGTERMISM_SMOKE_LANGFUSE_*`、`LONGTERMISM_SMOKE_AI_PLANE_*`；chat 另需 `LONGTERMISM_SMOKE_APP_BASE_URL` / `CHAT_AUTHORIZATION` / `CHAT_MANIFEST_ROOT`。本文件不记录任何 credential 值。

## 7. 常见故障排查（备选特有）

| 症状 | 解释与处置 |
| --- | --- |
| `signoz-otel-collector` 容器显示 unhealthy/无 healthcheck | 预期行为：官方最小镜像无 shell，禁止 CMD-SHELL 探测。健康性看 `obs-signoz-e2e` 查询闭环是否通过。 |
| SigNoz UI（3301）打不开但 API 8080 健康 | UI 端口映射以真实 E2E 校准为准；先确认 `docker compose -p longtermism-signoz ps` 的端口列。 |
| smoke 报 `marker_missing` 而 compose 已 healthy | 查询谓词与 SigNoz 存储投影的匹配问题（见 §5 校准点）：检查该 run 的 filter 是否命中 attribute key 的实际存储形式。 |
| infra 通过但 `langfuse_score` 失败 `score_projection_missing` | Langfuse score 投影是异步的：先在主线 profile 上确认投影链路，再回备选重跑；备选不掩盖该失败类别。 |
