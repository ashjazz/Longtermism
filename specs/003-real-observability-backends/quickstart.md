# Quickstart：真实可观测后端与最小 HTTP 闭环

本文是 feature 003 的可执行运维 runbook。命令事实以仓库当前 `Makefile`、
`cmd/obs-smoke` 和 `hack/observability` 为准；配置与数据边界分别以
[runtime configuration contract](contracts/runtime-configuration.md)、
[telemetry contract](contracts/telemetry-contract.md) 和
[smoke report schema](contracts/smoke-report.schema.json) 为准。

## 1. 阅读顺序

1. [spec.md](spec.md)
2. [plan.md](plan.md)
3. [research.md](research.md)
4. [data-model.md](data-model.md)
5. [HTTP API contract](contracts/http-api.yaml)
6. [runtime configuration contract](contracts/runtime-configuration.md)
7. [telemetry contract](contracts/telemetry-contract.md)
8. [real-backend acceptance checklist](checklists/real-backend-acceptance.md)
9. [Grafana 与 SigNoz 选择 runbook](../../docs/observability/10-grafana-vs-signoz-runbook.md)

## 2. 证据状态与通过规则

仓库中的状态必须按证据强度表述：

| 状态 | 能证明什么 | 不能证明什么 |
| --- | --- | --- |
| 命令已实现 | Make target、CLI 或脚本存在，参数与 fail-fast 路径有测试 | 命令已在当前机器或真实后端成功执行 |
| 静态/离线验证 | 配置、schema、runner、查询构造和清理语义通过本地测试 | Collector 实际接收、后端实际存储或查询成功 |
| 真实验收 | 本次唯一 run/window 产生 schema-valid report，且 checklist 引用该报告 | 未来运行、其它 profile 或未执行场景也会成功 |

文档出现命令不代表命令已在真实环境执行。Level 0 和配置契约具备离线验证入口；
Level 2、Level 3 与 Level 4 的真实 E2E 状态仍为待验收，实际状态只看本次命令退出码、
新生成的 schema-valid report，以及 [real-backend acceptance checklist](checklists/real-backend-acceptance.md)
中的证据引用。容器 `healthy`、UI 截图、旧报告、fake backend 测试或文档中的“期望”都不能替代
真实查询报告。

一次 live smoke 只有同时满足以下条件才可写为 passed：

- 命令退出码为 0，报告属于本次唯一 run ID、marker 与 bounded window；
- 报告通过 v3 schema，所有必需 checks 均为 `passed`，且 `failure_stage=none`；
- privacy 场景的八个 surface 都有真实 `attempted=true` 证据且各类别计数为 0；
- cleanup 状态与残留集合满足 §11；
- 报告路径和结论被 checklist 明确引用。

`failed`、`skipped`、缺报告、旧报告、cleanup 失败或未收敛的 capability sentinel 都不是通过。

## 3. 环境准备

### 3.1 工具、端口与本机资源

Level 0 需要 Go、Git、Bash 和 Ruby；Level 1-4 还需要 Docker Engine、Docker Compose、
`curl`、`jq`、`openssl` 与 `base64`。Go module 声明的兼容基线是 Go 1.25。

先做只读版本检查：

```bash
go version
git --version
bash --version
ruby --version
docker --version
docker compose version
curl --version
jq --version
openssl version
```

完整 Grafana+Langfuse profile 的逐服务 limits 合计 7.85 vCPU / 11.5 GiB，
SigNoz+Langfuse 为 7.90 vCPU / 12 GiB；operator budget 为 8 vCPU、12 GiB RAM、
20 GiB observability volumes。20 GiB 是预算，不是 Docker named-volume 自动配额。

已发布端口固定绑定 loopback，当前没有 env override。Grafana 与 SigNoz profile 都使用
4317/4318、13133、8888 和 Langfuse 3001，因此不能同时运行。Grafana 另使用
3000/9090/3100/3200。SigNoz Compose 声明宿主 3301，但容器 healthcheck 使用 8080；
在真实 E2E 解决并证明该差异前，不把 3301 描述为已验证的 UI/query 入口。端口冲突由
Compose fail-fast，不会静默改端口。

### 3.2 外部依赖与费用边界

| 来源 | 何时发生 | 费用/资源事实 |
| --- | --- | --- |
| Go module / container image 下载 | 首次缺缓存或拉取固定镜像 | 网络流量与本机磁盘；不是模型调用费用 |
| 本地 Compose profiles | Level 1-4 | 占用上述 CPU/RAM/volume 预算；由操作者承担本机或 CI compute 成本 |
| Langfuse self-hosted | profile 启动与 AI 查询 | 需要本地依赖凭据；14 天项目 retention 初始化依赖 EE license，许可成本不由仓库承担 |
| OpenAI-compatible provider | chat、privacy、部分 resilience 与 E2E | 真实模型 API 可能按 token/请求计费；运行前必须设置调用预算与限额 |
| 周期 canary job | 仅显式启用的外部 job | 重复产生容器与模型费用；失败属于运维信号，不伪装为普通 PR 代码回归 |

### 3.3 Langfuse cold bootstrap 与 warm start

复制 credential-free 模板。副本已被 Git 忽略；只在本地编辑或由 secret manager 注入：

```bash
cp deploy/observability/.env.local.example deploy/observability/.env.local
```

首次空 volume 启动前，按 [deploy/observability/README.md](../../deploy/observability/README.md)
补齐 Postgres、Redis、ClickHouse、MinIO、Langfuse init/EE、encryption/session 等变量。
每项凭据独立生成，不复用生产值，不把值粘贴进文档、issue、报告或 shell trace。

首次 cold bootstrap：

```bash
make obs-langfuse-bootstrap-up
```

确认项目、14 天 retention 与 project keys 后，在同一被忽略文件中补齐 Grafana admin、
`LANGFUSE_OTLP_AUTHORIZATION` 和 ingestion version，再切换到完整 warm profile。若 cold bootstrap
尚未进入 warm profile，可安全停止且保留 volumes：

```bash
make obs-langfuse-bootstrap-down
```

已存在项目的 headless init 不回写 retention；必须由 operator 在项目设置中迁移并通过 live
retention evidence 验证，不能用静态声明冒充已生效。`LANGFUSE_ENCRYPTION_KEY` 必须在已有
volume 生命周期内保持稳定，轮换规则见 §12。

### 3.4 应用配置与 live smoke 引用

创建独立、被忽略的应用配置；它不会和默认配置自动合并：

```bash
cp manifest/config/config.grafana-smoke.example.yaml manifest/config/config.grafana-smoke.local.yaml
```

Level 1 infra-only 可直接使用示例的 loopback Collector/OTLP/smoke 设置。Level 2+ 还必须在该
独立文件中显式设置 `ai.chat.enabled=true`、非空 provider model、
`observability.smoke.chat.enabled=true` 和 chat authorization env reference；真实值只从环境或
secret manager 注入。应用启动前至少要完成以下所有权配对：

| 用途 | 应用侧 | smoke/operator 侧 |
| --- | --- | --- |
| 模型 | `OPENAI_BASE_URL`、`OPENAI_API_KEY`、服务端 model 配置 | provider 账户预算与限流 |
| chat smoke auth | `LONGTERMISM_CHAT_SMOKE_AUTHORIZATION` | `LONGTERMISM_SMOKE_CHAT_AUTHORIZATION`；两者解析为同一当前值 |
| AI-negative 查询 | 配置引用 `LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL` | CLI 使用同名 query credential |
| score 投影 | `LANGFUSE_BASE_URL`、`LANGFUSE_PUBLIC_KEY`、`LANGFUSE_SECRET_KEY` | query credential 与 evidence/projection 路径 |
| 本地事实 | manifest/evidence/projection/artifact roots | 使用 operator-owned 绝对路径并限制权限，不提交内容 |

只在 loopback 上启动应用，并保持该进程运行：

```bash
GF_GCFG_FILE=manifest/config/config.grafana-smoke.local.yaml go run .
```

所有 `obs-*-smoke` 目标只查询或扰动已经运行的应用/profile；`obs-grafana-e2e`、
`obs-signoz-e2e` 与 `obs-resilience-e2e` 会编排 Compose，但不会替你启动应用。缺任一必需
`LONGTERMISM_SMOKE_*` 引用时，Make preflight 只打印变量名并在网络/runner 前退出。

## 4. Level 0：默认离线门禁

Level 0 不启动 Docker、不读取真实模型或平台凭据，也不调用外部 API：

```bash
make verify
make obs-contract
make obs-smoke-offline
make obs-platform-smoke
make eval-smoke
make obs-config-check
```

证据边界：

- `make verify` 当前真实 recipe 仅为 `go vet ./...` + `go test ./...`；最终 release 聚合目标属于后续任务，不能把尚未实现的 fmt/build/security gate 记在它名下。
- `make obs-contract` 运行观测装配、local smoke 与 OTel mapper 的离线测试。
- `make obs-smoke-offline` 运行受控 observability-chain eval，不连接真实 Collector。
- `make obs-platform-smoke` 使用 `-mod=readonly`、`-count=1` 与 30 秒 timeout，证明 local payload/identity/privacy contract；全部真实 backend checks 必须是 `skipped/not_verified_local_profile`。
- `make eval-smoke` 只验证本地评估回链。
- `make obs-config-check` 只解析受版本管理配置，证明配置结构，不证明 live readiness。

除 `obs-platform-smoke` 明确禁止隐式下载外，其它 Go 命令在 module cache 缺失时可能下载固定依赖；
这属于构建网络，不是外部平台或模型调用。需要 race 或变更行覆盖率时另运行：

```bash
make test-race
make obs-coverage
```

## 5. Level 1：Grafana 基础设施闭环

### 5.1 静态检查、启动与健康诊断

先执行 Level 0，再启动主线：

```bash
make obs-config-check
make obs-grafana-up
make obs-stack-health OBS_PROFILE=grafana
```

首次启动必须先完成 §3.3 cold bootstrap。`obs-stack-health` 只打印 Grafana project 的 Compose
状态，不是通过证据。应用必须已按 §3.4 在另一个终端运行。

### 5.2 Infra-only 查询式 smoke

运行前在当前 shell/secret manager 提供下列引用，值不进入报告或稳定错误：

- `LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL`
- `LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL`
- `LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL`
- `LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL`
- `LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL`
- `LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL`
- `LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL`

然后执行：

```bash
make obs-infra-smoke
```

该命令不发模型请求，因而没有模型 API 费用。60 秒窗口内必须从真实 Tempo、Loki、Prometheus
取得正向事实，并从 AI marker-count 与 Langfuse 取得负向事实；任一查询未尝试、超时或凭据失败
都不能被当成零命中。报告必须显示 infra 请求没有 `ai_trace_id`，且 AI 平面无该 marker。

## 6. Level 2：真实 chat、score 与 privacy

Level 2 需要完整的 §3.4 应用配置、真实模型账户、Langfuse project/query 凭据，以及当前 shell
中的 scenario 引用：

| 目标 | 额外必需引用 |
| --- | --- |
| `make obs-chat-smoke` | app base URL、chat auth、chat manifest root、Tempo/Loki/Prometheus/Langfuse query refs |
| `make obs-langfuse-score-smoke` | Langfuse query refs、local evidence path、projection path；复用已存在的 chat projection 事实 |
| `make obs-privacy-platform-smoke` | app/chat/manifest/privacy artifact roots、Tempo/Loki/Langfuse refs、projection path、Collector config digest/component/export-admission facts |

按事实依赖顺序运行：

```bash
make obs-chat-smoke
make obs-langfuse-score-smoke
make obs-privacy-platform-smoke
```

chat smoke 会产生真实模型调用与可能的 token 费用。score smoke 查询 chat 已持久化的 local
evidence/projection 与 Langfuse score，本身不应伪造新的模型事实。privacy smoke 使用受保护的
loopback chat fixture 和 synthetic canary，扫描八个真实 evidence surface；它同样会调用模型，
所以必须使用隔离账户、预算和低风险输入。

完整 Grafana E2E 会启动/停止 Compose，并依次执行 infra → chat → score → privacy；它仍要求应用
已经运行且全部引用已注入：

```bash
make obs-grafana-e2e
```

通过必须以这次运行新生成的报告为准。Tempo/Loki/Prometheus/Langfuse trace 的普通窗口最多 60 秒，
score 最多 120 秒；旧 run 或只在 UI 中“看到了数据”不能通过。

## 7. Level 3：故障与恢复

Level 3 是破坏性、本地显式 opt-in 验证。它会暂停或重启当前 project 中的后端/Collector，制造
队列积压或 Langfuse API 故障，并可能触发真实 chat，因此需要 Docker、完整 live refs、真实模型
预算和独占的测试 profile。不要对共享开发栈或生产资源运行。

单场景入口：

```bash
make obs-exporter-failure-smoke
make obs-persistent-queue-smoke
make obs-score-worker-failure-smoke
```

- exporter failure 默认针对 Tempo；CLI 的真实目录还定义 Loki 与 Langfuse 目标，由 full aggregate 使用。
- persistent queue 必须证明中断期间业务结果未被改写，恢复/重启后 120 秒内积压处理且 marker 可查；at-least-once 不宣称唯一投递。
- score-worker failure 的 Make target 使用 `langfuse-api` case，必须证明 chat 与 local evidence 不因平台失败丢失。

full aggregate 的单一入口为：

```bash
make obs-resilience-e2e
```

它会启动 Grafana profile，并在最长 30 分钟预算内串行尝试 7 个子场景：Tempo/Loki/Langfuse
exporter、persistent queue、score-worker 的 `langfuse-api`/`queue-full`/`shutdown`。当前
`queue-full` 与 `shutdown` 仍是显式 capability sentinel，会在副作用前 fail-fast；因此命令已实现
不代表完整 Level 3 已完成验收，也不得把其它 5 个结果聚合成假成功。子场景失败不应阻断后续 cleanup，
所有子报告必须先于 `scenario=full` 聚合报告持久化。

## 8. Level 4：SigNoz 兼容 profile

Level 4 只有在 Grafana 主线取得真实验收证据后才可用于“支持声明”。SigNoz 使用独立 Compose
project，但固定宿主端口与 Grafana profile 冲突；先停止主线，再运行：

```bash
make obs-grafana-down
make obs-signoz-e2e
```

`obs-signoz-e2e` 自己会执行 up → infra → chat → down。若完整验收失败，另开一个新 run，才使用
以下分段诊断路径；两条路径二选一，不能在同一次验收中串行执行，否则会重复模型调用与费用：

```bash
make obs-grafana-down
make obs-signoz-up
make obs-signoz-infra-smoke
make obs-signoz-chat-smoke
make obs-signoz-down
```

备选 profile 没有 `obs-stack-health` 支持，
也不包含主线专属 score/privacy 深度 smoke 或 Grafana alerts；健康与兼容性必须由 SigNoz 三信号查询、
Langfuse AI trace/score checks 和新报告证明。真实 chat 仍产生模型 API 费用。

当前 3301 publication/8080 healthcheck 差异、dashboard 导入和 query filter 仍需首次真实 E2E 校准；
在 checklist 引用新报告前，Level 4 保持待验收。

## 9. 故障注入与恢复

运行 Level 3 前：

1. 使用隔离的 `longtermism-observability` project，确认没有其他开发者共享；
2. 确认应用、profile、query refs、evidence/projection paths 和模型预算完整；
3. 记录运行开始时间，确保报告目录可写且不含旧 run 的手工复制品；
4. 不手工删除 persistent queue、evidence 或 volume，否则会破坏恢复证据；
5. 为 5 分钟单场景或 30 分钟 full aggregate 预留维护窗口。

正常退出、失败、INT 或 TERM 时，`obs-resilience-e2e` 的 trap 会先尝试 unpause，再执行不带 `-v` 的
down。若进程被强制终止而 trap 没有运行，先用完整 Compose 定义恢复 paused services，再停止：

```bash
docker compose --project-name longtermism-observability \
  --env-file deploy/observability/versions.env \
  --env-file deploy/observability/.env.local \
  -f deploy/observability/compose.langfuse.yaml \
  -f deploy/observability/compose.grafana.yaml unpause
make obs-grafana-down
```

随后检查最新报告的 `cleanup`。任何 `paused-service`、`temporary-queue-data`、
`score-worker-queue-full` 等 residual 都必须先人工恢复，再用新 run 重跑；不得修改旧报告或只删掉
失败行。当前仓库只允许按 §11 预览 inventory，不执行 volume 删除；销毁交由基础设施 owner 的
批准流程处理。

## 10. 诊断

按失败层级从低成本到高成本排查：

```bash
make obs-config-check
make obs-stack-health OBS_PROFILE=grafana
find build/observability/smoke-reports -maxdepth 1 -type f -name '*.json' -print
```

仓库当前的 direct Langfuse curl target 不经过正式 query adapter 的 endpoint 安全边界，会把 Basic
credential 发往环境变量指定地址，因此本 runbook 不使用它。Langfuse 诊断使用对应的
infra/chat/score 查询式 smoke；不要把含 Authorization 的 `curl -v`、展开后的
`docker compose config` 或未经扫描的容器日志粘贴到 issue/报告。

由操作者选择本次可信报告路径，再只读取闭合字段：

```bash
REPORT_PATH=build/observability/smoke-reports/example-report.json
jq '{schema_version,run_id,profile,scenario,status,checks,cleanup}' "$REPORT_PATH"
```

| `failure_stage` | 首查事实源 |
| --- | --- |
| `preflight` | 缺失 env 名、profile、端口、文件权限和 capability sentinel；不应已有网络副作用 |
| `api` | 应用 loopback/受保护 smoke admission、业务 envelope 与真实模型结果 |
| `export` | Collector 稳定 component ID 的 sent/send_failed/enqueue_failed 与 queue snapshot |
| `query` | Prometheus target、Grafana datasource、Tempo/Loki/Langfuse/SigNoz 有界响应 |
| `cleanup` | paused containers、queue/storage/run residue、临时凭据与临时数据所有权 |

`model_upstream` 只用 provider response 归因，不能借 Collector/Grafana 失败证据改写为 observability
故障。SigNoz 没有可用的 `obs-status`/health target；使用分段 query smoke 或完整 E2E 诊断。

## 11. Cleanup 与安全 reset

### 11.1 每次 smoke 的 cleanup 判定

smoke 自建短期凭据必须在报告生成前撤销或删除（发行方支持时），并删除本地副本；外部注入的长期凭据
属于 operator/provider，不得由 smoke 撤销。当前 targets 不创建平台凭据，合法报告通常记录
`cleanup.temporary_credentials=not_created`，不能把它解释为完成了凭据轮换。

smoke 自建临时文件和数据（run 目录、临时 queue/debug 数据）必须清零；有意保留的低敏 report、
eval evidence、projection snapshot 和 named volumes 是 operator-owned 诊断证据，不冒充临时残留。
每次必须检查：

- `cleanup.temporary_credentials`
- `cleanup.temporary_data`
- `cleanup.residual_resources`

`cleanup.status=not_required` 仅在两项临时资产均为 `not_created` 且 residual resources 为空时合法；
实际执行过 cleanup 时必须为 `completed`、两项临时资产不得为 `failed` 且 residual resources 为空。
`cleanup.status=failed`、任一临时资产为 `failed` 或仍有 residual resources 时，本次运行必须判定失败；
先恢复/清理真实资源，再创建新 run 重跑，禁止手改报告为 passed。

### 11.2 普通停止

普通停止保留 named volumes 和低敏证据：

```bash
make obs-grafana-down
make obs-signoz-down
```

### 11.3 Reset 当前边界

`reset.sh` 当前只适合做只读 inventory preview。Grafana 与 SigNoz/Langfuse Compose 的 named volumes
尚未声明脚本要求的 `longtermism.observability=true` label，因此空的 `planned volumes` 不代表数据已删除；
containers 是按 Compose project 枚举，不受该 volume label 过滤。分别用精确 project 名预览：

```bash
bash hack/observability/reset.sh --project-name longtermism-observability --dry-run
bash hack/observability/reset.sh --project-name longtermism-signoz --dry-run
```

在 volume labels 与 run-root canonical/containment/symlink 校验落地前，本 runbook 禁止执行脚本的
confirm 模式、`obs-reset` target 或传入 run-root；当前实现会接受 `/`、home 或 workspace root，不能靠
操作员记忆替代 fail-closed 校验。需要销毁 profile 数据时，停止在 preview，转交基础设施 owner 使用
平台批准、可审计且逐项确认 target 的流程；不得改用 global prune、`down -v` 或更宽的删除命令。
这项限制不豁免 smoke runner 自身对临时凭据、临时数据和 residual resources 的 cleanup 契约。

## 12. 凭据轮换

仓库没有创建/撤销 provider credential 的 CLI；平台/UI/secret-manager 操作由凭据所有者完成。
通用顺序固定为：先创建并验证新凭据 → 更新被 Git 忽略的 `.env.local` 或 secret manager →
重启真正消费该值的 owner → 运行对应 smoke → 确认新凭据通过对应 smoke 后，才由凭据所有者撤销旧值。
全程只记录 env 名、presence、时间和结果，不记录值。

| 凭据族 | 需要更新/重启的 owner | 验证入口 | 旧值撤销者 |
| --- | --- | --- | --- |
| OpenAI-compatible key/base URL | 应用 provider 配置；重启应用 | `make obs-chat-smoke`，必要时再跑 privacy | provider/operator；smoke 不撤销 |
| Langfuse project key、OTLP header、score key | `.env.local`、Collector warm profile、应用 score adapter、query shell | 运行 `make obs-grafana-e2e`，只认查询式报告 | Langfuse project owner |
| Langfuse query-only credential | smoke query shell/secret manager | 对应 infra/chat/score/privacy smoke | query credential owner |
| chat smoke authorization | 应用的 `LONGTERMISM_CHAT_SMOKE_AUTHORIZATION` 与 CLI 的 `LONGTERMISM_SMOKE_CHAT_AUTHORIZATION` 同步更新；重启应用 | `make obs-chat-smoke` | operator |
| AI-plane query credential | 应用 marker-count admission 与 smoke query client；重启应用 | `make obs-infra-smoke` | operator |
| Grafana admin、DB、ClickHouse、MinIO | 各自 Compose service/secret store | 先按部署文档完成服务级迁移，再跑 config + E2E | 对应基础设施 owner |

`LANGFUSE_INIT_PROJECT_PUBLIC_KEY`/`SECRET_KEY` 应和当前实际 project key 保持一致，避免 volume
重建后恢复出不同 identity；已有 project 不会因重新 bootstrap 自动轮换 key。

`LANGFUSE_ENCRYPTION_KEY` 不是普通 API key，它保护既有加密字段；不得在已有 volume 上无迁移计划直接轮换。
同样，不得用 `down -v`/reset 代替任何 credential rotation。只有 smoke 自己创建且能证明 ownership 的
短期凭据才允许由 smoke cleanup 撤销；当前 targets 的外部注入值全部属于 operator。

## 13. 门禁频率

下表是当前真实 target 的运行策略。Level 1-4 均显式 opt-in；普通 PR 不因第三方短时故障被阻塞。

| 触发 | 必跑命令 | 外部依赖、费用与证据 |
| --- | --- | --- |
| PR | `make verify`、`make obs-contract`、`make obs-smoke-offline`、`make obs-platform-smoke`、`make eval-smoke`、`make obs-config-check` | 无 Docker、无真实凭据、零外部 API 费用；module cache 缺失时可能下载 Go 依赖；只产生离线/静态证据 |
| 观测配置变更 | PR 行全部命令 + `make obs-config-check`；涉及 Collector/backend/provisioning/retention 且有隔离容器环境时再运行 `make obs-grafana-e2e` | 静态部分无 Docker；live 部分需要 Docker、完整凭据和本机预算，真实模型 API 可能计费；必须保存新报告 |
| 阶段里程碑 | Level 0 + `make obs-config-check` + `make obs-grafana-e2e` + `make obs-resilience-e2e` | 需要 Docker、Langfuse、真实模型与独占 profile，真实模型 API 可能计费；未收敛 sentinel 必须保持失败证据 |
| Release candidate | `make verify` + `make obs-coverage` + `make obs-config-check` + `make obs-grafana-e2e` + `make obs-resilience-e2e`；声明 SigNoz 支持时另跑 `make obs-signoz-e2e` | 显式 Docker/credential/budget 前置，真实模型 API 可能计费；每个门禁只认本次 schema report/checklist |
| Scheduled canary | operator job 运行 `make obs-grafana-e2e`；已有 SigNoz 支持声明时独立运行 `make obs-signoz-e2e`，resilience 只放在获批维护窗口 | 非合并门禁；需要隔离 Docker、secret manager、告警路由和调用预算，真实模型 API 可能计费；失败发运维告警 |

推荐把 scheduled canary 的具体 cron 频率交给部署环境配置，而不是硬编码进仓库。最低要求是在每个
release candidate 和观测配置变更后运行相应 live canary；是否增加每日/每周 job 取决于平台稳定性、
费用预算与值班能力。涉及恢复语义的变更必须额外运行 resilience；普通业务 PR 不自动外连。

## 14. 运行完成检查单

每次需要声明真实验收时逐项确认：

- [ ] 运行的是本次新 run/marker/window，不是旧报告或 UI 缓存。
- [ ] 应用与 profile 均为 loopback/隔离测试环境，未使用共享生产资源。
- [ ] 命令退出码为 0，所有必需 checks 与 privacy evidence 符合 v3 schema。
- [ ] cleanup 三字段无失败或残留；外部长期凭据未被 smoke 撤销。
- [ ] 报告、错误和粘贴到审查系统的内容不含 credential、Authorization、endpoint 或 raw payload。
- [ ] 报告路径、日期和关键 checks 已写入 real-backend checklist；未运行项保持未勾选。
- [ ] 普通停止已执行；若要求销毁数据，只完成 inventory 并已转交批准的基础设施流程，没有调用当前禁用的 reset confirm。

只有这些证据闭环后，才能在后续 T166/T169 中更新真实支持声明。
