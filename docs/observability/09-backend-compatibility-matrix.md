# 真实观测后端兼容矩阵

**最后更新**：2026-07-13
**状态说明**：本文件中的“计划基线”是已固定、待实现验证的组合；只有带有真实 smoke report 路径的条目才能标记为“E2E 已验证”。当前没有此类条目。

## 1. 应用运行时与 OTel 模块

| 组件 | 当前固定版本 | 兼容性状态 | 约束与验证来源 |
| --- | --- | --- | --- |
| Go module language | Go 1.25 | 计划基线 | `go.mod` 是最低兼容基线；开发机版本不改变该承诺。 |
| GoFrame | v2.10.2 | 计划基线 | 仅作为 HTTP/框架基础埋点来源；应用运行时只能有一个全局 OTel provider。官方版本来源：<https://github.com/gogf/gf/releases/tag/v2.10.2>。 |
| OTel API / metric / trace | v1.44.0 | **待 T005 对齐** | 目前 API 版本高于 SDK/exporter；不得将这个混合图宣称为兼容已验证。 |
| OTel SDK / OTLP trace exporters | v1.38.0 | **待 T005 对齐** | T005 必须以同一经过测试的 OTel 模块族替换直接依赖，并运行 race/contract 测试。OTel Go 的 traces/metrics 为 stable、logs 仍为 beta：<https://opentelemetry.io/docs/languages/go/>。 |
| OTLP protobuf | v1.7.1 | **待 T005 对齐** | 随最终 OTel Go module graph 统一，不单独升级。 |
| GoFrame OTel contrib | v2.10.2（间接） | 计划基线 | 先通过单 provider lifecycle 测试；若其初始化不能表达本规格的 TLS/header/sampling/test double，则使用 `internal/cmd` 的窄官方 SDK 装配。 |

**E2E 已验证**：无。T005 完成模块统一、T026 完成 exporter 装配并通过真实 smoke 后，才可在本表补充 report 路径、日期、平台架构和 image digest。

## 2. 容器后端计划基线

所有镜像 tag 的唯一来源是 [`deploy/observability/versions.env`](../../deploy/observability/versions.env)。该文件附有每个变量的官方发布链接；本表不重复密钥或 endpoint。

| 后端族 | 固定版本 | 角色 | 状态 |
| --- | --- | --- | --- |
| OTel Collector Contrib | 0.153.0 | App 唯一 OTLP 出口、infra/AI fan-out、queue | 计划基线 |
| Prometheus | 3.11.0 | 指标 scrape 与查询 | 计划基线 |
| Loki | 3.7.2 | OTLP/filelog 日志存储 | 计划基线 |
| Tempo | 2.10.5 | OTLP trace 存储与查询 | 计划基线 |
| Grafana | 13.0.2 | datasource、dashboard、alert | 计划基线 |
| Langfuse / worker | 3.185.0 | AI trace 与异步 score 投影 | 计划基线 |
| Langfuse ClickHouse / PostgreSQL / Redis | 26.4.4.38 / 17.10 / 8.8.0 | Langfuse 自托管依赖 | 计划基线 |
| SigNoz / its Collector / ClickHouse | 0.126.0 / 0.126.0 / 26.4.4.38 | 备选基础设施 profile | 计划基线；仅在 Grafana 主线后验证 |

官方资料：Collector release <https://github.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v0.153.0>、Prometheus <https://github.com/prometheus/prometheus/releases/tag/v3.11.0>、Loki <https://github.com/grafana/loki/releases/tag/v3.7.2>、Tempo <https://github.com/grafana/tempo/releases/tag/v2.10.5>、Grafana <https://github.com/grafana/grafana/releases/tag/v13.0.2>、Langfuse <https://github.com/langfuse/langfuse/releases/tag/v3.185.0>、SigNoz <https://github.com/SigNoz/signoz/releases/tag/v0.126.0>。

## 3. 升级顺序

一次变更只升级一个后端族；禁止把 Go module、Collector 和多个存储后端混在同一个兼容性变化中。

1. **OTel Go module graph**：先更新 API/SDK/exporter/proto 的目标模块族，执行 `go mod tidy`、`go mod verify`、`go test -race ./...` 和本地 contract tests。
2. **GoFrame integration**：在不改变业务埋点的前提下验证单 provider、propagation 与 shutdown；失败时停在窄 SDK adapter，不引入第二个 provider。
3. **Collector**：更新 `versions.env` 的单一 Collector tag，执行静态 config fixture 与 Collector profile validation；再验证 queue、tail sampling 和 export failure evidence。
4. **Grafana 主线存储**：按 Prometheus、Loki、Tempo、Grafana 的单独族顺序更新，每次只运行该后端的健康、查询和 dashboard/alert smoke。
5. **Langfuse 与依赖**：先读取官方 self-host migration notes，备份其独立 volume，再升级 Langfuse 与匹配依赖；重新验证 trace OTLP 与 score API 两个失败域。
6. **SigNoz**：最后、独立于 Grafana 主线升级；必须同时重新证明 SigNoz 三信号与 Langfuse AI trace/score。

## 4. 回滚与验证

回滚只恢复本次变更的版本族，且不使用 `latest`、`docker system prune` 或跨项目 volume 删除。

1. 停止当前 observability profile，保留 volumes 与本次低敏 smoke report。
2. 将 `versions.env` 或 Go module graph 恢复到上一个已验证 Git commit；容器类变更需同时恢复该后端配置格式。
3. 对有持久化 schema 的 Langfuse、ClickHouse、PostgreSQL 或 SigNoz，先按其官方 downgrade/migration 指南判断是否支持回退；未知时只恢复应用/Collector，保留数据 volume 并标记人工处置。
4. 重跑下列与变更范围相符的验证，失败时保留 error class、版本与 report path，不输出 credentials 或 payload。

| 验证命令 | 当前可用性 | 用途 |
| --- | --- | --- |
| `go mod tidy && go mod verify && go test -race ./...` | 当前可用 | OTel Go module / GoFrame 回滚的最低验证。 |
| `bash hack/observability/config_check_test.sh` | 当前可用 | 静态 Checker 契约与无密钥 fixture 验证。 |
| `make obs-config-check` | T009 后可用 | 对实际版本矩阵、Compose 与 Collector profile 运行静态门禁。 |
| `make obs-grafana-e2e`、`make obs-resilience-e2e` | T066/T129 后可用 | Grafana 主线真实查询、恢复与 queue 证据。 |
| `make obs-signoz-e2e` | T147 后可用 | SigNoz profile 与 Langfuse AI 平面兼容证据。 |

## 5. Digest 记录规则

首次真实 E2E 通过后，记录每个实际拉取镜像的 digest、CPU 架构、Compose profile、Collector config hash 和对应 smoke report。tag 便于人工审查；digest 才是 release/CI 的不可变输入。未经该验证，禁止填写或猜测 digest。
