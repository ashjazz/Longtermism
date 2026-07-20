# 真实观测后端部署资产

本目录承载本 spec 的部署资产，不承载应用业务配置或真实凭据。应用只连接 OTel Collector；Tempo、Loki、Prometheus、Grafana、SigNoz 和 Langfuse 的后端连接信息分别属于 Collector 或对应 backend profile。

## 当前状态

Grafana profile 的 Compose、Collector、datasource、dashboard 与 alert provisioning 已落地；它们仍只通过静态解析与离线契约验证，尚未构成真实后端 E2E 成功证据。`versions.env` 固定容器镜像 tag，并在首次真实 E2E 后才记录 digest。

## Profile 边界与实施顺序

| Profile | 基础设施平面 | AI 平面 | 状态与边界 |
| --- | --- | --- | --- |
| Grafana 主线 | OTel Collector + Prometheus + Loki + Tempo + Grafana | Langfuse trace/score | **优先实现**。先完成 Grafana 主线的正常投递、查询、隐私和恢复证据，才可声明首个真实后端闭环。 |
| SigNoz 备选 | OTel Collector + SigNoz | Langfuse trace/score | **后置实施**。仅替换基础设施 logs/metrics/traces 后端；不得改变应用 OTLP 出口、AI marker、HTTP 契约或 SmokeReport schema。仅在 Grafana 主线 US1-US3 验收后开始。 |

Langfuse 始终是 AI 语义平面：它接收带 AI marker 的 trace 投影，并承载独立的 score 投影；它不替代 Prometheus、Loki、Tempo 或 SigNoz 的基础设施职责。纯基础设施请求不得进入 Langfuse。

## 资产索引

| 路径 | 责任 | 当前状态 | 对应任务 |
| --- | --- | --- | --- |
| `versions.env` | 唯一可读镜像 tag 矩阵 | 已实现；未有 E2E digest | T003 |
| `compose.grafana.yaml` | Grafana 主线 Compose profile | 已实现；真实运行仍需显式注入 Langfuse 配置 | T056 |
| `collector/collector-grafana.yaml` | Grafana 主线 Collector ingress/fan-out | 已实现 | T054 |
| `grafana/` | datasource、dashboard、alert provisioning | 已实现 | T059-T062 |
| `compose.signoz.yaml` | SigNoz 备选 Compose profile | 计划契约，尚未创建 | T141 |
| `collector/collector-signoz.yaml` | SigNoz 三信号与 Langfuse AI fan-out | 计划契约，尚未创建 | T142 |
| `signoz/dashboard.json` | SigNoz 专用运营面板 | 计划契约，尚未创建 | T145 |

## 命令状态

| 命令 | 状态 | 说明 |
| --- | --- | --- |
| `bash hack/observability/config_check_test.sh` | 当前可用 | 无 Docker、无真实凭据的静态 checker 契约测试。 |
| `bash hack/observability/config_check.sh` | 当前可用 | 检查已存在的 profile 资产；在 Compose/Collector 尚未创建时会以缺失资产失败。 |
| `make obs-config-check` | **计划契约（T009）** | 将静态 checker 接入 Make；当前不可调用。 |
| `make obs-grafana-e2e` | 已实现（显式 opt-in） | 启动 Grafana profile、打印健康状态并执行查询式 infra smoke；失败仍关闭当前 profile，报告目录不删除。 |
| `make obs-signoz-e2e` | **计划契约（T148）** | 仅在 Grafana 主线验收后提供；当前不可调用。 |

所有真实 profile 必须使用显式环境变量、secret file 或密钥管理器注入凭据。禁止把密钥、token、完整 OTLP header 或生产 endpoint 写入本目录的可提交文件；本地 override 采用已忽略的 `*.local.yaml` 或 `.env.local`。

## Grafana profile 的本地前置条件

`make obs-grafana-up` 会在 180 秒内等待 profile healthcheck 完成。运行前必须在当前
shell 或被忽略的本地环境文件中提供 Langfuse 运行配置、`GRAFANA_ADMIN_USER`、
`GRAFANA_ADMIN_PASSWORD` 和 Collector 写入 Langfuse 所需的两个 OTLP 变量；Compose
会在任一项缺失时 fail-fast，仓库不提供默认管理员密码。

应用运行在宿主机时，将其 OTLP endpoint 配置为 `127.0.0.1:4317`（或显式选择
`127.0.0.1:4318` 的 HTTP/protobuf 变体）。Compose 只将这两个 Collector ingress
端口发布到 loopback；Tempo、Loki、Prometheus、Grafana 与 Langfuse 的服务 DNS 仍只
存在于 profile 内。

`make obs-grafana-e2e` 假定应用已启动、已启用 infra-smoke 路由，并已指向上述
Collector endpoint；同时需要 `LONGTERMISM_SMOKE_*` 的本地只读查询 URL/credential
引用。它打印 `ps` 仅用于诊断，唯一的通过结论来自 `obs-infra-smoke` 写出的低敏查询报告。
