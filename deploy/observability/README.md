# 真实观测后端部署资产

本目录承载本 spec 的部署资产，不承载应用业务配置或真实凭据。应用只连接 OTel Collector；Tempo、Loki、Prometheus、Grafana、SigNoz 和 Langfuse 的后端连接信息分别属于 Collector 或对应 backend profile。

## 当前状态

Grafana profile 的 Compose、Collector、datasource、dashboard 与 alert provisioning 已落地；它们仍只通过静态解析与离线契约验证，尚未构成真实后端 E2E 成功证据。`versions.env` 固定容器镜像 tag，并在首次真实 E2E 后才记录 digest。

## Profile 边界与实施顺序

| Profile    | 基础设施平面                                               | AI 平面                | 状态与边界                                                                                                                     |
| ---------- | ---------------------------------------------------- | -------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Grafana 主线 | OTel Collector + Prometheus + Loki + Tempo + Grafana | Langfuse trace/score | **优先实现**。先完成 Grafana 主线的正常投递、查询、隐私和恢复证据，才可声明首个真实后端闭环。                                                                     |
| SigNoz 备选  | OTel Collector + SigNoz                              | Langfuse trace/score | **后置实施**。仅替换基础设施 logs/metrics/traces 后端；不得改变应用 OTLP 出口、AI marker、HTTP 契约或 SmokeReport schema。仅在 Grafana 主线 US1-US3 验收后开始。 |

Langfuse 始终是 AI 语义平面：它接收带 AI marker 的 trace 投影，并承载独立的 score 投影；它不替代 Prometheus、Loki、Tempo 或 SigNoz 的基础设施职责。纯基础设施请求不得进入 Langfuse。

## 资产索引

| 路径                                 | 责任                                        | 当前状态                                                                             | 对应任务       |
| ---------------------------------- | ----------------------------------------- | -------------------------------------------------------------------------------- | ---------- |
| `versions.env`                     | 唯一可读镜像 tag 矩阵                             | 已实现；未有 E2E digest                                                                | T003       |
| `compose.langfuse.yaml`            | Langfuse 自托管与首次 cold bootstrap            | 已实现；含 Langfuse、ClickHouse、Redis、Postgres 与仅内部可见的 MinIO，不解析 Collector project key | T066A      |
| `compose.grafana.yaml`             | Grafana 主线的 Collector/三信号/Grafana profile | 已实现；与 Langfuse Compose 组合后构成 warm-start 主线                                       | T056、T066A |
| `.env.local.example`               | 无凭据本地运行配置模板                               | 已实现；复制出的 `.env.local` 被忽略                                                        | T066A      |
| `collector/collector-grafana.yaml` | Grafana 主线 Collector ingress/fan-out      | 已实现                                                                              | T054       |
| `grafana/`                         | datasource、dashboard、alert provisioning   | 已实现                                                                              | T059-T062  |
| `compose.signoz.yaml`              | SigNoz 备选 Compose profile                 | 计划契约，尚未创建                                                                        | T141       |
| `collector/collector-signoz.yaml`  | SigNoz 三信号与 Langfuse AI fan-out           | 计划契约，尚未创建                                                                        | T142       |
| `signoz/dashboard.json`            | SigNoz 专用运营面板                             | 计划契约，尚未创建                                                                        | T145       |

## 命令状态

| 命令                                             | 状态                            | 说明                                                                                               |
| ---------------------------------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------ |
| `bash hack/observability/config_check_test.sh` | 当前可用                          | 无 Docker、无真实凭据的静态 checker 契约测试。                                                                  |
| `bash hack/observability/config_check.sh`      | 当前可用                          | 检查已存在的 profile 资产；在 Compose/Collector 尚未创建时会以缺失资产失败。                                             |
| `make obs-config-check`                        | **计划契约（T009）**                | 将静态 checker 接入 Make；当前不可调用。                                                                      |
| `make obs-grafana-e2e`                         | 已实现（显式 opt-in）                | 启动 Grafana profile、执行 infra → chat → score → privacy 四个查询式 smoke 聚合门禁；任一失败仍关闭当前 profile，报告目录不删除。 |
| `make obs-chat-smoke`                          | 已实现（显式 opt-in）                | 受保护 live chat smoke：只查询已运行服务，要求 `--live` 与全部 T181 引用。                                            |
| `make obs-langfuse-score-smoke`                | 已实现（显式 opt-in）                | 验证上一次真实 chat run 的异步 score 投影（本地 evidence 先于平台结果）。                                               |
| `make obs-privacy-platform-smoke`              | 已实现（显式 opt-in）                | 八 surface 隐私组合 smoke；synthetic canary 未脱敏命中必须为 0。                                                |
| `make obs-direct-langfuse-smoke`               | 已实现（诊断）                       | 只诊断 Langfuse ingestion 可达与凭据可用；不产生报告，**不属于** `obs-grafana-e2e` 通过依据。                             |
| `make obs-exporter-failure-smoke`              | 已实现（显式 opt-in，能力收敛中）          | 单出口故障注入（tempo 目标）；CLI 默认 composition 当前 fail-fast（能力哨兵），命令与预检已装配。                                |
| `make obs-persistent-queue-smoke`              | 已实现（显式 opt-in，能力收敛中）          | 跨 Collector 重启的持久队列恢复；同样受 CLI 能力哨兵约束。                                                            |
| `make obs-score-worker-failure-smoke`          | 已实现（显式 opt-in，能力收敛中）          | score worker 故障（langfuse-api case）；同样受 CLI 能力哨兵约束。                                               |
| `make obs-resilience-e2e`                      | 已实现（显式 opt-in）                | 启动 profile 后由 CLI 串行编排 7 个子场景；trap 先 unpause 再 down，中断后 paused services 必被恢复。                    |
| `make obs-reset`                               | 已实现（破坏性，`CONFIRM_RESET=1` 门控） | label-scoped 重置：只删当前 project 的 observability 卷与 `run-*` 残留，绝不 prune，不触碰应用数据。                     |
| `make obs-signoz-e2e`                          | **计划契约（T148）**                | 仅在 Grafana 主线验收后提供；当前不可调用。                                                                       |

所有真实 profile 必须使用显式环境变量、secret file 或密钥管理器注入凭据。禁止把密钥、token、完整 OTLP header 或生产 endpoint 写入本目录的可提交文件；本地 override 采用已忽略的 `*.local.yaml` 或 `.env.local`。

## Grafana profile 的本地前置条件

首次启动和后续启动刻意分为两条命令，不能靠 Compose service selection 绕开变量插值：
完整 profile 会在创建 Collector 前验证 Langfuse project key；而该 key 必须先在 Langfuse
项目中创建。两条路径都使用固定的 `longtermism-observability` Compose project，因此共享
同一组 Langfuse named volumes，绝不需要为拿到 key 而删除数据卷。

先复制本地模板（它本身不含任何可用凭据）：

```bash
cp deploy/observability/.env.local.example deploy/observability/.env.local
```

填入 `LANGFUSE_*` 的自托管运行配置。`make` 在该文件存在时自动读取它；也可以继续只在
当前 shell export 变量。不要提交 `.env.local`，不要把真实值填回 example。

### 首次冷启动：headless 创建 14 天 retention 项目

1. 填写 `.env.local.example` 中首次 bootstrap 分组的所有空值，包括数据库/Redis/ClickHouse
   connection string、普通与管理员 ClickHouse 密码、MinIO root 与应用身份、加密/会话密钥、
   `LANGFUSE_EE_LICENSE_KEY` 及全部 `LANGFUSE_INIT_*` identity/credential；此时不需要
   Grafana 管理员信息或 Collector OTLP 变量。

   本地 Compose 网络中请使用服务 DNS，而不是容器名称或宿主机端口。例如：
   ```dotenv
   LANGFUSE_DATABASE_URL=postgresql://langfuse:<LANGFUSE_POSTGRES_PASSWORD>@langfuse-db:5432/langfuse
   LANGFUSE_DIRECT_URL=postgresql://langfuse:<LANGFUSE_POSTGRES_PASSWORD>@langfuse-db:5432/langfuse
   LANGFUSE_REDIS_CONNECTION_STRING=redis://langfuse-redis:6379
   LANGFUSE_CLICKHOUSE_URL=http://langfuse-clickhouse:8123
   LANGFUSE_CLICKHOUSE_MIGRATION_URL=clickhouse://langfuse-clickhouse:9000
   LANGFUSE_CLICKHOUSE_USER=langfuse
   LANGFUSE_S3_EVENT_UPLOAD_BUCKET=langfuse
   LANGFUSE_MINIO_ROOT_USER=langfuse
   ```
   `LANGFUSE_CLICKHOUSE_PASSWORD`、`LANGFUSE_CLICKHOUSE_ADMIN_PASSWORD`、`LANGFUSE_MINIO_ROOT_USER`、`LANGFUSE_MINIO_ROOT_PASSWORD` 与
   `LANGFUSE_MINIO_SECRET_ACCESS_KEY` 都是你本地生成的强随机值；其中 ClickHouse 密码必须用
   `openssl rand -hex 32` 分别生成（64 位小写十六进制），其余值也应独立生成并长期保存。
   `LANGFUSE_CLICKHOUSE_ADMIN_PASSWORD` 只交给 ClickHouse 与初始化任务，Langfuse web/worker
   只获得 `LANGFUSE_CLICKHOUSE_PASSWORD` 对应的 `default` 数据库账户。该账户需要完成 Langfuse
   自身 schema migration，故在本地 profile 中拥有该数据库内的完整权限，不能用于项目外查询。
   MinIO root
   凭据只交给初始化任务；Langfuse web/worker 只使用独立的
   `LANGFUSE_MINIO_ACCESS_KEY_ID`/`LANGFUSE_MINIO_SECRET_ACCESS_KEY`，其策略限制为该 bucket 的
   `events/` 前缀，并包含 retention 清理所需的 `DeleteObject`。`langfuse-minio` 不发布宿主机端口，bootstrap 会通过
   one-shot initializer 自动、幂等地创建 `LANGFUSE_S3_EVENT_UPLOAD_BUCKET`，无需手工打开
   MinIO Console 或创建桶。另一个 one-shot initializer 会以同样的幂等方式创建/授权
   `langfuse` ClickHouse 用户，因此即使先前失败的冷启动已留下 ClickHouse 数据卷，也不要
   用 `down -v` 重置。MinIO 仍位于默认 Compose 网络，所以同一 profile 的其他容器在网络层
   也可解析它的服务 DNS；本地配置依靠不发布端口和独立、最小权限账户收紧边界。持久卷保存原始
   事件，敏感环境还必须依赖宿主机磁盘访问控制或加密。

   `LANGFUSE_ENCRYPTION_KEY` 必须用 `openssl rand -hex 32` 生成一次并长期保留；它同时注入
   Langfuse web 与 worker，用于解密既有数据中的加密字段，不能在已有数据卷上随意更换。
   Headless project key 必须保留 Langfuse 识别的前缀，可分别生成：
   ```bash
   printf 'lf_pk_%s\n' "$(openssl rand -hex 24)"
   printf 'lf_sk_%s\n' "$(openssl rand -hex 24)"
   ```
   将结果只写入被忽略的 `.env.local`，不要复制到命令历史、日志或本模板。
2. 执行 `make obs-langfuse-bootstrap-up`。它只启动 Langfuse web/worker 与它们依赖的
   Postgres、ClickHouse、Redis、MinIO 及一次性建桶任务，并等待可验证的健康检查完成。
   Langfuse `3.185.0` 的 worker 镜像未提供稳定 HTTP 或脚本 health endpoint，因此不为它
   伪造存活探针；worker 的启动依赖由 Compose 的已完成初始化任务保证，web `/api/health`
   才是该 bootstrap 的服务就绪依据。
   web 容器预留 `2GiB` 内存，并将 Node 堆限制为 `1536MiB`；这是为了容纳首次 model catalogue
   与 migration，避免 Node 在默认 cgroup heap 上限触发 `JavaScript heap out of memory`。因此本机
   Docker 可用内存需要覆盖整个 profile 声明的 `12GiB` 上限。
3. bootstrap 会按显式 `LANGFUSE_INIT_*` identity 幂等创建首个用户、组织、项目和 API key，
   并在新项目上设置 14 天 retention。该策略依赖 Langfuse EE license；没有 license 时不能
   用无限保留冒充成功。对于已存在于 named volumes 的项目，headless 初始化不会回写 retention，
   必须在项目设置中显式迁移并核验 14 天策略。可打开 `http://127.0.0.1:3001` 检查结果。
4. 在被忽略的 `.env.local`（或当前 shell）中填写 `GRAFANA_ADMIN_USER`、
   `GRAFANA_ADMIN_PASSWORD`、`LANGFUSE_OTLP_AUTHORIZATION` 和
   `LANGFUSE_OTEL_INGESTION_VERSION`，然后执行 `make obs-grafana-up`。

`LANGFUSE_OTLP_AUTHORIZATION` 使用 headless 初始化的 project public/secret key，由 Collector
写入 header，格式为 `Basic <base64(public:secret)>`；

```bash
printf '%s:%s' '<Public Key>' '<Secret Key>' | base64
```

它不是 Langfuse Web service 的启动配置。项目 key 后续轮换时，只更新本地值并重启 warm
profile；不要通过 `down -v` 或重新 bootstrap 来轮换 key。

如果首次初始化尚未进入 warm-start，可以用 `make obs-langfuse-bootstrap-down` 停止 bootstrap
服务；完整 Grafana profile 已在运行时请只使用 `make obs-grafana-down`，避免留下仍依赖
Langfuse 的 Collector/基础设施容器。

> 注意：compose 插值把 `LANGFUSE_INIT_*` 与 `LANGFUSE_EE_LICENSE_KEY` 声明为必填，因此
> 任何 profile 命令（含 `down`/`ps`）都要求这些变量存在。它们只在空库时被 headless init
> 幂等消费；warm start 只需非空占位值。推荐把这些变量持久化在被忽略的
> `deploy/observability/.env.local`（compose 已通过 `--env-file` 加载），避免每个新
> shell 重新 export；`LANGFUSE_INIT_PROJECT_PUBLIC_KEY`/`_SECRET_KEY` 应与项目实际
> key 一致（可从 `LANGFUSE_OTLP_AUTHORIZATION` 的 base64(pk:sk) 反推），保证万一
> volume 重建后 bootstrap 能还原出与 Collector 一致的凭据。

### 非首次启动：常规 warm start

项目 key 已存在且本地配置已齐全时，直接执行 `make obs-grafana-up`。它会组合
`compose.langfuse.yaml` 和 `compose.grafana.yaml`，并在缺少任意 Collector OTLP 变量时
fail-fast，避免用空 header 启动一个看似健康、实际丢失 AI 平面投递的 profile。

首次使用（或保留了旧卷后重启）时，profile 会先运行一个一次性的、固定版本 BusyBox
初始化器：它只在 `collector-data` 中创建三套持久队列目录，并将其交给非 root Collector 的
`10001:<当前宿主机 GID>`。Collector 镜像本身不含 shell，因此不能在其容器内执行这一步；无需
手工 `chown`，也不要用 `down -v` 删除诊断卷。

同样因为 Collector、Loki 与 Tempo 镜像是无 shell 的精简镜像，Compose 使用无特权的
`*-health-probe` 容器请求它们各自真实的 HTTP readiness endpoint。`make obs-grafana-up --wait`
等待的是这些真实 HTTP 就绪证据，不是对服务二进制的伪检查；Prometheus 镜像则直接使用它自带的
`wget` 探测 `/-/ready`。

如果修改了 Collector、Loki、Tempo 或 Prometheus 的 bind-mounted 配置文件，现有容器不会因文件内容
变化自动重建。请执行 `make obs-grafana-down` 后再执行 `make obs-grafana-up`；两条命令都不会使用
`-v`，因此不会删除 named volumes。

应用运行在宿主机时，将其 OTLP endpoint 配置为 `127.0.0.1:4317`（或显式选择
`127.0.0.1:4318` 的 HTTP/protobuf 变体）。Compose 只将这两个 Collector ingress
端口发布到 loopback；Tempo、Loki、Prometheus、Grafana 与 Langfuse 的服务 DNS 仍只
存在于 profile 内。

### 本地 infra smoke 启动顺序

`make obs-infra-smoke` 是一次**查询式验收**：它不会启动 Docker Compose，也不会启动应用。
先完成下列顺序，再执行它：

1. 依照上面的冷启动或 warm-start 路径完成 `make obs-grafana-up`（首次创建 key 后也必须
   执行这一步）。应用完成日志经 OTLP 直接进入 Collector → Loki native OTLP，本地 smoke
   **不再依赖**宿主机 JSONL 文件、filelog 采集、bind mount 或文件 GID 共享；Collector
   自己的持久队列仍由 profile 的 one-shot initializer 初始化，应用必须由执行此命令的
   同一普通用户启动，不要用 `sudo`。
2. 复制 `manifest/config/config.grafana-smoke.example.yaml` 为已忽略的
   `config.grafana-smoke.local.yaml`。这是**独立配置文件**，不会与 `config.yaml` 或
   `config.local.yaml` 自动合并；它会将应用绑定为 `127.0.0.1:8000`，改为 `collector` mode、
   指向 `127.0.0.1:4317`，显式启用 trace、metrics、OTLP 日志与 smoke 路由，并通过
   `observability.smoke.ai_plane.authorization_env` 引用 marker-count AI-negative 端点的
   只读 credential。不要在共享网络上以全网卡监听的方式启用该无认证探测路由。

   如果你此前已创建过该 `.local.yaml`，请合并示例新增的 `logs_transport: otlp`、
   `local_jsonl_enabled: false` 与 `ai_plane.authorization_env` 设置，或重新复制示例后再
   恢复自己的非敏感设置；旧文件中的 `observability.logs.path` JSONL 前置已废弃。
3. 在另一个终端以显式配置启动应用：
   ```bash
   GF_GCFG_FILE=manifest/config/config.grafana-smoke.local.yaml go run .
   ```
   若应用不在默认的 `http://127.0.0.1:8000`，额外设置 `LONGTERMISM_SMOKE_APP_BASE_URL`，
   并仍将该应用入口限制在 loopback。GoFrame 同样支持等价的
   `go run . --gf.gcfg.file=manifest/config/config.grafana-smoke.local.yaml` 形式。
4. 仅在本地 shell、secret 文件或密钥管理器中设置下列查询引用；不要把 credential 值写入
   可提交的 YAML、Makefile 或文档：
   ```bash
   export LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL=http://127.0.0.1:9090
   export LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL=http://127.0.0.1:3100
   export LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL=http://127.0.0.1:3200
   export LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL=http://127.0.0.1:3001
   export LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL='<local read-only credential>'
   export LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL=http://127.0.0.1:8000
   export LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL='<local read-only credential>'
   make obs-infra-smoke
   ```
   Prometheus、Loki、Tempo 和 Langfuse 地址是 Grafana profile 发布到 loopback 的本地查询
   入口；AI-plane 地址是第 3 步启动的应用入口。`LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL`
   同时是应用侧 `marker-count` 端点的只读 credential（示例通过
   `observability.smoke.ai_plane.authorization_env` 引用同一个环境变量），因此应用启动
   前必须先 export 它；缺失时 marker-count 端口保持关闭。credential 的格式由对应的 query
   client 契约决定：Langfuse 查询 credential 是 `base64(<public key>:<secret key>)`
   （顺序不能颠倒），与 OTLP header 的 base64 部分一致。Make target 会先检查全部七项
   必填引用，缺失时只打印变量名和本文档位置，绝不打印值。

`make obs-grafana-e2e` 会代为执行第 1 步，并在结束时关闭当前 Compose profile；它仍假定
第 2、3、4 步已经完成。它打印 `ps` 仅用于诊断，唯一的通过结论来自 `obs-infra-smoke` 写出的
低敏查询报告。手动调试完成后使用 `make obs-grafana-down` 停止 profile；该命令不删除 named
volumes，以保留故障诊断证据。
