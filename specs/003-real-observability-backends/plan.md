# 实施计划：真实可观测后端与最小 HTTP 闭环

**规格目录**：`specs/003-real-observability-backends`
**规格文件**：`specs/003-real-observability-backends/spec.md`
**计划文件**：`specs/003-real-observability-backends/plan.md`
**分支**：`003-real-observability-backends`
**日期**：2026-07-10

## 概要

本计划把 002 已完成的双平面关联、隐私契约、离线 smoke 和 OpenAI-compatible provider，推进为第一条真实可观测后端闭环。首轮交付以 Grafana 经典栈和 Langfuse 为主线：应用只连接 OTel Collector，纯基础设施请求进入基础设施平面，真实非流式 chat 同时形成基础设施与 AI 语义事实，并把本地 eval evidence 异步投影为 Langfuse score。

计划先交付可独立验收的 infra-only 垂直切片，再交付真实 chat/AI 切片，随后完成隐私、故障恢复、dashboard/alert 和发布门禁。SigNoz 作为备选基础设施 profile，在 Grafana 主线完成后实施，不改变应用接口、AI 事实模型或 Langfuse 平面。

## 技术上下文

**项目类型**：Go 后端服务 + 可独立测试的 AI Agent Harness + 本地容器化可观测环境。

**主要语言/运行环境**：Go module 声明 Go 1.25；当前开发机 Go 1.26.4；GoFrame v2.10.2；Docker Engine 29+ 与 Docker Compose v5。代码必须以 `go.mod` 声明版本为兼容基线。

**当前状态**：已有 `pkg/ai/obs.Trace`、关联身份、baggage allowlist、低敏 mapper、eval evidence、平台 smoke runner、OpenAI-compatible Chat Completions adapter和 GoFrame health API。尚缺真实 OTel provider/meter 装配、真实 Collector 配置、日志采集、后端 compose、chat/infra-smoke API、Langfuse score worker、dashboard/alert 与平台查询式 E2E。

**关键依赖**：GoFrame HTTP 自动 tracing；OpenTelemetry Go API/SDK 与 OTLP exporters；OpenTelemetry Collector Contrib；Prometheus、Loki、Tempo、Grafana；Langfuse self-hosted；后续 SigNoz self-hosted。新增依赖必须固定版本/镜像 digest，并在兼容矩阵中记录。

**存储/外部服务**：Collector `file_storage` 持久队列；Prometheus/Loki/Tempo/Langfuse/SigNoz 自有本地 volumes；本地 eval evidence；真实 OpenAI-compatible 模型服务。应用数据库与观测 volumes 严格隔离。

**测试策略**：TDD。Level 0 默认离线门禁不启动 Docker、不访问真实 LLM；Level 1 验证配置与本地基础设施；Level 2 验证真实 chat/score/privacy；Level 3 验证故障恢复；Level 4 验证 SigNoz。平台 smoke 使用唯一 marker、限定查询窗口和机器可读报告。

**约束**：业务代码只能依赖 OTel API、`pkg/ai/obs` 与应用端口，不得认识具体后端；进程内只有一个全局 TracerProvider；应用到 Collector 默认 OTLP/gRPC，保留 HTTP/protobuf override；Langfuse trace ingestion 使用 OTLP/HTTP；payload 仅允许 metadata-only 或经脱敏的受控内容；观测失败不得改写业务结果；高基数身份不得进入 metrics labels。

**未知项**：无。具体镜像 digest 作为 Phase 1 的锁定产物，在任何 compose 服务实现前完成；GoFrame contrib initializer 若不能满足已定义的 resource、协议、header、TLS、sampling 与测试替身要求，则按 ADR-0008 的回退条件使用窄封装的官方 OTel SDK 装配，并继续复用 GoFrame 自动埋点。

## 宪章检查

### 初始检查

- **评估先行**：每个垂直切片先定义 contract、离线测试和后端查询证据；真实 chat 必须同时产生 eval evidence，满足。
- **核心抽象独立**：`pkg/ai/obs.Trace`、`llm.Provider`、`eval.EvaluationEvidence` 保持核心事实源；平台类型只存在于 app/infrastructure adapter，满足。
- **生产失效可见**：计划独立建模 Collector push、Prometheus pull、Grafana query、Langfuse score worker 和模型上游失败，满足。
- **轻量 Harness 优先**：不引入第三方 Agent 编排框架；仅增加 app usecase、观测 adapter 和后端环境，满足。
- **学习可沉淀**：要求至少一篇真实接入或恢复 journal，并维护 ADR、quickstart、dashboard checklist，满足。
- **默认门禁无付费依赖**：真实 LLM 与平台 E2E 显式 opt-in，默认 PR 运行离线测试，满足。
- **安全边界**：配置契约禁止源码密钥；三种 payload policy、日志/队列/报告扫描和安全 reset 均纳入验收，满足。

### 设计后复查

- `research.md` 已确认 App/Collector 协议分层、GoFrame 初始化回退条件、方案 C pipeline、Loki native OTLP、Langfuse OTLP/score、persistent queue 和版本锁定策略。
- `data-model.md` 已区分领域身份、真实 OTel 身份、平台投影身份、投递状态和内容策略，避免 adapter 猜测事实。
- `contracts/` 已定义 HTTP API、遥测语义、配置所有权和 smoke 报告，不向业务接口泄露后端地址或密钥。
- `quickstart.md` 已按 Level 0-4 描述可运行验证路径，明确尚未实现的命令是计划契约，不将本地 sender 当作真实后端成功。
- 未发现宪章冲突。持久化队列仅用于投递恢复且有容量、保留和清理边界，不成为新的业务事实源。

## 项目结构

### 文档产物

```text
specs/003-real-observability-backends/
  spec.md
  plan.md
  research.md
  data-model.md
  quickstart.md
  contracts/
    http-api.yaml
    runtime-configuration.md
    telemetry-contract.md
    smoke-report.schema.json
  checklists/
    requirements.md
    real-backend-acceptance.md
```

### 代码区域

```text
api/v1/chat/                         chat Req/Res 契约
api/v1/observability/                infra-smoke Req/Res 契约
internal/controller/chat/            薄 HTTP controller
internal/controller/observability/   受保护的 infra-smoke controller
internal/logic/chat/                  AI usecase、AI plane 标记、eval evidence
internal/observability/              OTel 装配、metrics、Langfuse score adapter
internal/cmd/                        配置校验、middleware、DI、lifecycle、路由
pkg/ai/obs/                          稳定事实到 OTel/GenAI 语义映射
pkg/ai/eval/                         本地 evidence，不依赖 Langfuse
deploy/observability/                compose、Collector、后端、dashboard、alerts
hack/observability/                  smoke 查询、故障注入、报告与安全 reset
manifest/config/                     非敏感应用配置和本地 override 示例
```

## 阶段计划

### Phase 0：调研与决策

`research.md` 固化以下问题：

1. App -> Collector 与 Collector -> 各后端的协议为何必须分层。
2. GoFrame contrib 初始化能否满足 gRPC/HTTP override、resource、sampling、header、TLS 和测试替身；何时启用官方 SDK 回退。
3. 方案 C 如何避免公共 processor 重复，同时保证纯 infra 请求不进入 Langfuse。
4. glog JSON 文件如何经 filelog 与 Loki native OTLP 保留 trace correlation。
5. Langfuse OTLP trace 与 score API 如何分离，如何使用稳定幂等键。
6. persistent queue、tail sampling、retention、版本 pin 和本地资源预算如何验证。
7. 为什么 SigNoz 只替换基础设施后端且排在 Grafana 主线之后。

### Phase 1：设计与契约

- `data-model.md` 定义运行配置、关联身份、API envelope、AI observation、score projection、smoke run/report、queue/failure snapshot 和状态转换。
- `contracts/http-api.yaml` 固定 `/api/v1/chat` 与 `/api/v1/observability/infra-smoke` 的请求、响应、错误和 debug 边界。
- `contracts/telemetry-contract.md` 固定 AI plane attribute、span/log/metric 语义、身份映射、高基数禁区和 Collector 路由断言。
- `contracts/runtime-configuration.md` 固定 App、Collector、backend、Grafana 与 Langfuse score adapter 的配置所有权及 fail-fast 规则。
- `contracts/smoke-report.schema.json` 固定机器可读报告，防止旧数据或手工 UI 检查误判通过。
- `quickstart.md` 定义从离线门禁到真实恢复实验的操作顺序和预期结果。

### Phase 2：任务拆分预告

后续 `/speckit-tasks` 应按以下依赖切片，并为每个实现任务生成先失败的测试任务：

1. **质量与版本地基**：补齐 verify/config/security targets，锁定镜像/模块兼容矩阵，移除未使用的 OpenTracing/Jaeger 直接依赖。
2. **应用遥测装配**：配置值对象、单一 provider lifecycle、OTLP gRPC/HTTP、resource、propagation、metrics 和 exporter failure 指标。
3. **Grafana infra-only 切片**：Collector ingress/infra pipeline、Tempo、Prometheus、glog/filelog/Loki、persistent queue、infra-smoke API 和自动后端查询。
4. **真实 chat 切片**：HTTP 契约、OpenAI-compatible provider DI、AI usecase、request/OTel/AI identity、generation observation 和本地 eval evidence。
5. **Langfuse AI 切片**：AI downstream filter/export、OTLP attribute mapping、进程内 score worker、幂等投影和查询 smoke。
6. **隐私与配置模式**：三种 payload policy、production fail-fast、实际后端 canary 扫描、secret scan 和本地 override 保护。
7. **恢复与运营资产**：独立 exporter 故障、queue/restart/drain、score worker failure、dashboard、alerts、retention 和安全 reset。
8. **SigNoz 兼容切片**：独立 profile、三信号查询、dashboard/checklist，并继续验证 Langfuse AI 闭环。
9. **收口证据**：Level 0-4 targets、机器可读报告、quickstart、ADR/journal、最终 Grafana 与 SigNoz 验收。

Grafana 主线的任务必须先完成第 1-7 项；SigNoz 任务不得成为主线首个闭环的前置依赖。

## 风险与缓解

- **全局 provider 冲突**：GoFrame contrib 与官方 SDK 都可能注册全局 provider。通过单一初始化端口、幂等 lifecycle 和进程级测试禁止重复注册。
- **OTel 模块版本漂移**：当前 API/SDK 模块版本不完全一致。先建立兼容矩阵并统一直接依赖，再接真实 exporter；每次升级运行 contract、race 和 Collector smoke。
- **历史 OpenTracing 污染**：当前 `go.mod` 仍有未使用的 OpenTracing/Jaeger 直接依赖。首个质量切片删除并用 `go mod why`/`go mod tidy` 验证无回流。
- **Langfuse 协议误配**：Langfuse OTLP 不支持 gRPC。Collector 到 Langfuse 固定 HTTP/protobuf，并用 direct diagnostic smoke 隔离 endpoint/header 问题。
- **高基数 metrics**：request/run/trace identity 只进入 span、log 和报告，不进入 metric labels；metrics smoke 使用 route/status 增量。
- **敏感数据落盘**：应用出口过滤先于 Collector；queue、日志、报告和后端查询均执行 canary 扫描。`content_raw` 仅能在 local/test 以显式授权生成不可序列化的本地调试工件，标准出口仍只允许 metadata 或经脱敏的受控内容。
- **观测依赖拖垮业务**：exporter 与 score worker 有界异步、独立失败指标和 shutdown 超时；所有故障注入都断言业务结果未被改写。
- **本地资源过重**：compose 提供 infra-only 与 full profile；完整 Grafana+Langfuse/Signoz+Langfuse 本地总预算上限为 12 GiB 内存、8 vCPU、20 GiB observability volumes，并记录实测峰值。
- **E2E 假阳性与残留风险**：每次 smoke 使用唯一 marker、起始时间、轮询超时和后端 API 查询；报告包含每个检查的 failure stage 与 cleanup 状态，禁止只看容器 healthy 或手工 UI。smoke 自建短期凭据必须在报告前撤销（当发行方支持）并删除本地副本，run 目录、临时 queue 数据和调试临时数据必须零残留；外部注入的长期凭据不由 smoke 撤销。
