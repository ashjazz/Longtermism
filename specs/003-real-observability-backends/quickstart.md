# Quickstart：真实可观测后端与最小 HTTP 闭环

本文是 003 阶段的验证/run guide。Level 0 离线门禁与 Level 1 静态/部署命令已实现并通过离线验证；涉及真实后端的命令（Level 2+ 的 E2E、Level 3 resilience、Level 4 SigNoz）已装配但需要 Docker 与显式凭据，其通过状态以 [real backend acceptance checklist](checklists/real-backend-acceptance.md) 的实际证据为准——文档出现命令不代表它已经跑过真实验收。

## 1. 阅读顺序

1. [spec.md](spec.md)
2. [plan.md](plan.md)
3. [research.md](research.md)
4. [data-model.md](data-model.md)
5. [HTTP API contract](contracts/http-api.yaml)
6. [runtime configuration contract](contracts/runtime-configuration.md)
7. [telemetry contract](contracts/telemetry-contract.md)
8. [real backend acceptance checklist](checklists/real-backend-acceptance.md)

## 2. 本地前置条件

- Go 1.25 compatible toolchain
- Docker Engine 与 Docker Compose
- `curl`, `jq`
- 可用的本地磁盘空间和端口；完整 profile 预算见 runtime configuration contract
- Level 2+ 才需要 OpenAI-compatible 与 Langfuse credentials

先检查工具，不启动服务：

```bash
go version
docker --version
docker compose version
jq --version
```

## 3. Level 0：默认离线门禁

目标：不启动 Docker、不访问真实模型或平台，验证代码质量、关联、隐私与 eval 回链。

```bash
make verify
make obs-contract
make obs-smoke-offline
make obs-platform-smoke
make eval-smoke
```

当前已可运行且收紧后的本地平台契约（US5）：

```bash
make obs-platform-smoke
```

期望与证据边界：

- 运行性质：无 Docker、无任何 credential、默认零网络（`-mod=readonly` 使缺依赖直接失败而不是隐式联网下载），30 秒预算内完成（`-timeout=30s`，超时即失败并指认超时测试），`-count=1` 防止测试缓存把红测漂白成绿；`hack/observability/make_level0_test.sh` 以 `env -i` 最小环境持续守护这些性质。
- 它证明的是三份受控契约：local controlled-sender 配置边界（显式 opt-in，对生产凭据 env 零读取）、双平面 payload 与 identity（infra 面无 AI marker、AI span 携带生产路由 marker、request/service/AI/eval 身份显式分离且零外连）、以及隐私 canary（三种 payload policy 下合成 secret/PII/业务原文在外发表面零命中，命中立即失败且输出只含类别/计数）。
- 受控运行产物是 schema-valid 的 local `platform_contract` 报告（profile=local）：报告内全部真实后端 checks 一律 `skipped` 并注明 `not_verified_local_profile` 范围，任何真实后端都不得标记 passed。
- 因此 controlled sender 保护的是 payload/identity/privacy contract，**不是**后端验收：Collector 真实接收与 Grafana/SigNoz 查询闭环只由 `make obs-grafana-e2e`（Level 2）与 `make obs-signoz-e2e`（Level 4）证明。
- env 入口 `LONGTERMISM_PLATFORM_SMOKE_ENABLED` / `LONGTERMISM_PLATFORM_SMOKE_PROVIDER` 只影响受控运行的显式装配，且只接受严格的 `1/true`、`0/false`；本目标与仓库内任何凭据变量无关。

## 4. 准备本地配置

仓库默认配置必须保持无密钥。创建被 Git 忽略的 local override 或仅在当前 shell 注入变量：

```bash
export OPENAI_BASE_URL="<openai-compatible-v1-base-url>"
export OPENAI_API_KEY="<set-locally>"
export LANGFUSE_BASE_URL="http://127.0.0.1:3001"
export LANGFUSE_PUBLIC_KEY="<set-locally>"
export LANGFUSE_SECRET_KEY="<set-locally>"
```

不要把真实值写入命令历史、文档、提交、报告或聊天。实施后的启动检查只输出“配置存在/缺失”，不输出值。

## 5. Level 1：配置与 Grafana 基础设施

先验证静态配置：

```bash
make obs-config-check
```

期望：

- Compose 两个 profile 可解析。
- Collector 配置可 validate。
- 镜像没有 `latest`，端口/healthcheck/resource/retention 均已声明。
- 应用配置只包含 Collector endpoint。

启动 Grafana 主线：

```bash
make obs-grafana-up
make obs-stack-health OBS_PROFILE=grafana
```

默认 UI：Grafana `http://127.0.0.1:3000`，Langfuse `http://127.0.0.1:3001`。实际端口以 local override 为准。

### Infra-only smoke

```bash
make obs-infra-smoke
```

期望在 60 秒内自动证明：

- API envelope 与 `X-Request-ID` 一致，无 `ai_trace_id`。
- Tempo 能按 request/run marker 查询 root span。
- Loki 能查询关联 JSON log。
- Prometheus route/status counter 与 histogram 相对基线增长。
- AI filter outgoing-items 增量为 0，Langfuse 中无该 marker。

命令必须输出符合 [smoke-report.schema.json](contracts/smoke-report.schema.json) 的报告。

## 6. Level 2：真实 chat 与 AI 平面

显式配置真实上游与 Langfuse credentials 后运行：

```bash
make obs-chat-smoke
make obs-langfuse-score-smoke
make obs-privacy-platform-smoke
make obs-grafana-e2e
```

### 手工 API 形态检查

```bash
curl -sS \
  -H 'Content-Type: application/json' \
  -d '{"message":"Reply with one short sentence."}' \
  http://127.0.0.1:8000/api/v1/chat | jq
```

期望：

- 响应符合 [HTTP contract](contracts/http-api.yaml)。
- `request_id` 与 `ai_trace_id` 存在；debug 时 `eval_summary` 不超过 1 KiB。
- Tempo、Loki、Prometheus 与 Langfuse trace 在 60 秒内可查询。
- 本地 eval evidence 存在；Langfuse score 在 120 秒内可查询或产生明确 projection failure。
- AI root/bridge 与 semantic spans 有 AI plane marker，无关 child spans 未被标记。

### 隐私 smoke

隐私命令使用仓库内 synthetic markers，不使用真实秘密。它必须查询实际后端和本地报告，断言未脱敏命中数为 0。payload 可使用 metadata-only 或经脱敏的受控内容；`content_raw` 只在 local/test 加 `raw_content_enabled=true` 时产生不可 JSON 序列化的本地调试工件，绝不进入实际后端或报告。若 smoke 为本次运行自行创建短期凭据或 secret file，必须在退出前撤销（当发行方支持）并删除；运行目录、临时 queue 数据和调试临时数据也必须清零，最终报告以 cleanup 状态证明零残留。调用方提供的长期凭据不属于 smoke 可撤销对象。

## 7. Level 3：故障与恢复

```bash
make obs-exporter-failure-smoke
make obs-persistent-queue-smoke
make obs-score-worker-failure-smoke
make obs-resilience-e2e
```

期望：

- Tempo/Loki/Langfuse exporter 的失败证据按 component 区分。
- 一个 push exporter 失败时其它出口继续成功。
- Prometheus pull/Grafana query 故障不伪造 Collector send-failed。
- 后端暂停、Collector 重启、后端恢复后，120 秒内 queue 排空且 marker 可查。
- queue 满、磁盘不可写、permanent error、shutdown timeout 有 dropped/failed 证据。
- score worker 失败不阻塞 chat，本地 evidence 不丢失。
- 命令退出时恢复所有被暂停容器，并报告 residual resources、smoke 自建临时凭据的撤销/删除状态，以及 run 目录、临时 queue 数据和调试临时数据的零残留状态。

## 8. Dashboard 与告警

Grafana provisioning 必须无需手工创建：

- Prometheus/Loki/Tempo datasources healthy。
- request/error/latency、Collector exporter、LLM token/cost、eval regression 可见。
- 能从日志或指标定位 trace。
- HTTP error、exporter send/enqueue failure、queue saturation/age、storage pressure alerts 均有 firing/resolved 证据。

## 9. Level 4：SigNoz 兼容性

仅在 Grafana 主线验收后：

```bash
make obs-signoz-up
make obs-stack-health OBS_PROFILE=signoz
make obs-signoz-e2e
```

期望：SigNoz 中 logs/metrics/traces 可查询；Langfuse 中 AI trace/score 仍可查询。应用 endpoint、API contract 和埋点不因 profile 改变。

## 10. 诊断

```bash
make obs-status
make obs-direct-langfuse-smoke
```

`obs-direct-langfuse-smoke` 只隔离 Langfuse endpoint/header/ingestion 问题，不属于正式应用拓扑，也不能替代 Collector fan-out E2E。

## 11. 停止与安全清理

普通停止保留 volumes：

```bash
make obs-grafana-down
make obs-signoz-down
```

破坏性清理必须显式确认：

```bash
CONFIRM_RESET=1 make obs-reset OBS_PROFILE=grafana
```

reset 先打印删除预览，只删除当前 Compose project 带 observability labels 的容器、volumes 和本地 raw 调试工件；不得匹配应用数据库或无关 volume。

## 12. 阶段验收

Grafana 主线：

```bash
make verify
make obs-config-check
make obs-grafana-e2e
make obs-resilience-e2e
```

SigNoz 支持声明：

```bash
make obs-signoz-e2e
```

最终证据包括机器可读 reports、real backend checklist、dashboard/alert assets 和至少一篇真实接入或恢复 journal。
