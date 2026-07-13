# 真实观测后端部署资产

本目录承载本 spec 的部署资产，不承载应用业务配置或真实凭据。应用只连接 OTel Collector；Tempo、Loki、Prometheus、Grafana、SigNoz 和 Langfuse 的后端连接信息分别属于 Collector 或对应 backend profile。

## 当前状态

当前唯一已实现的部署资产是 [versions.env](versions.env)：它固定所有容器镜像 tag，并在首次真实 E2E 后才记录 digest。Compose、Collector、dashboard、alert 与 smoke runner 尚未实现；本目录中的未来路径是任务契约，不能当作可运行配置或已通过验收的证据。

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
| `compose.grafana.yaml` | Grafana 主线 Compose profile | 计划契约，尚未创建 | T051 |
| `collector/collector-grafana.yaml` | Grafana 主线 Collector ingress/fan-out | 计划契约，尚未创建 | T052 |
| `grafana/` | datasource、dashboard、alert provisioning | 计划契约，尚未创建 | T059-T062 |
| `compose.signoz.yaml` | SigNoz 备选 Compose profile | 计划契约，尚未创建 | T141 |
| `collector/collector-signoz.yaml` | SigNoz 三信号与 Langfuse AI fan-out | 计划契约，尚未创建 | T142 |
| `signoz/dashboard.json` | SigNoz 专用运营面板 | 计划契约，尚未创建 | T145 |

## 命令状态

| 命令 | 状态 | 说明 |
| --- | --- | --- |
| `bash hack/observability/config_check_test.sh` | 当前可用 | 无 Docker、无真实凭据的静态 checker 契约测试。 |
| `bash hack/observability/config_check.sh` | 当前可用 | 检查已存在的 profile 资产；在 Compose/Collector 尚未创建时会以缺失资产失败。 |
| `make obs-config-check` | **计划契约（T009）** | 将静态 checker 接入 Make；当前不可调用。 |
| `make obs-grafana-e2e` | **计划契约（T066）** | 真实 Grafana/Langfuse 查询 smoke；当前不可调用。 |
| `make obs-signoz-e2e` | **计划契约（T148）** | 仅在 Grafana 主线验收后提供；当前不可调用。 |

所有真实 profile 必须使用显式环境变量、secret file 或密钥管理器注入凭据。禁止把密钥、token、完整 OTLP header 或生产 endpoint 写入本目录的可提交文件；本地 override 采用已忽略的 `*.local.yaml` 或 `.env.local`。
