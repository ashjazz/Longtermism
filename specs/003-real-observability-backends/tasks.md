# 任务拆解：真实可观测后端与最小 HTTP 闭环

**输入**：`specs/003-real-observability-backends/spec.md`、`plan.md`、`research.md`、`data-model.md`、`quickstart.md`、`contracts/`、`checklists/real-backend-acceptance.md`
**前置文档**：`docs/adr/0008-real-observability-backends-and-minimal-http-loop.md`、`docs/observability/08-real-backend-decision-workbench.md`、`.specify/memory/constitution.md`
**生成日期**：2026-07-10

## 任务格式规则

所有任务必须使用 markdown checkbox、顺序任务编号、可选并行标记、用户故事标记和精确文件路径，例如：`- [ ] T001 [P] [US1] 在 path/to/file.go 执行具体任务；质量门控：说明验收要求。`

执行约束：

1. 每个代码或基础设施行为都必须遵循 RED -> GREEN -> REFACTOR：测试任务先落地并确认因目标能力缺失而失败，实现任务才可开始；不得通过放宽断言、跳过测试或硬编码 fixture 让测试变绿。
2. 每个任务只聚焦一个文件、一个功能、一个测试面或一个文档面；需要跨文件闭环时，通过相邻任务与依赖关系组合，不把它们合并成大任务。
3. 默认 Level 0 门禁不得启动 Docker、访问真实 LLM、Langfuse 或付费服务；Level 1-4 必须显式 opt-in，并使用唯一 run marker、限定查询窗口和机器可读报告。
4. 业务代码只能面向 OTel API、`pkg/ai/obs`、`pkg/ai/eval` 和应用端口；Tempo、Loki、Prometheus、Grafana、SigNoz 与 Langfuse trace endpoint 只能出现在 Collector、部署或平台 adapter 边界。
5. 所有敏感内容测试都使用 synthetic canary；任何日志、错误、queue、报告或测试输出不得包含真实 credential、Authorization、原始 prompt/output 或已识别 PII。
6. Go 测试优先采用表驱动风格；相关阶段结束前运行 `go test -race`，新核心 Go 代码覆盖率不得低于 80%，usecase 目标不低于 90%。

## Phase 1：Setup - 质量与版本地基

**目标**：先固定版本、目录、配置检查和默认离线入口，使后续任何容器或 exporter 变更都可重复验证。

**独立验收**：`make obs-config-check` 能在无密钥、无容器运行的环境中校验版本、Compose/Collector 静态约束和本地配置保护；`go mod why` 不再显示项目直接依赖 OpenTracing/Jaeger。

- [X] T001 在 `hack/observability/config_check_test.sh` 编写配置检查器 RED 测试，使用临时 fixture 覆盖 `latest` 镜像、缺失 healthcheck/resource limit、host 端口冲突、backend endpoint 泄漏到应用配置、OpenTracing/Jaeger 直接依赖回流、无效 Collector pipeline、不可用 persistent-storage 路径和合法最小配置；质量门控：先运行该脚本确认因检查器尚未实现而失败，测试不得读取真实 `.env`。
- [X] T002 在 `hack/observability/config_check.sh` 实现静态配置检查器；质量门控：使 T001 GREEN，使用结构化 YAML/Compose 解析能力而非脆弱字符串替换，在启动 Compose 前以 `invalid_collector_pipeline` 或 `storage_path_unavailable` 拒绝无效配置，错误必须指出文件与稳定错误类别且不打印 secret 值。
- [X] T003 [P] 在 `deploy/observability/versions.env` 固定 Collector、Prometheus、Loki、Tempo、Grafana、Langfuse 及其依赖、SigNoz 的明确 patch tag；质量门控：禁止 `latest`，每个变量附官方来源注释，首次真实 E2E 后再补 digest，不虚构未验证 digest。
- [X] T004 [P] 在 `docs/observability/09-backend-compatibility-matrix.md` 记录 Go/GoFrame/OTel 模块与容器版本兼容矩阵、升级顺序和回滚验证命令；质量门控：必须区分“计划基线”与“E2E 已验证”，链接到官方来源且不复制密钥配置。
- [X] T005 在 `go.mod` 与 `go.sum` 移除未使用的 `opentracing-go`/Jaeger 直接依赖并统一 OTel 直接依赖；质量门控：运行 `go mod tidy`、`go mod why`、`go test ./...`，不得通过升级无关依赖制造额外漂移。
- [X] T006 [P] 在 `.gitignore` 增加 `*.local.yaml`、`.env.local`、观测 smoke reports、临时 queue/volume 元数据的忽略规则；质量门控：保留可提交示例文件，运行 `git check-ignore` 证明本地 secret override 被忽略且正式配置未被误忽略。
- [X] T007 [P] 在 `manifest/config/config.local.example.yaml` 提供无密钥本地 override 示例；质量门控：默认 `observability.enabled=false`、`smoke.enabled=false`，只写环境变量名，不包含任何 backend endpoint 或 credential 值。
- [X] T008 [P] 在 `deploy/observability/README.md` 建立部署资产索引与 profile 边界；质量门控：明确 Grafana 主线先行、SigNoz 后置、Langfuse 始终属于 AI 平面，并标注所有尚未实现的命令为计划契约。
- [X] T009 在 `Makefile` 增加 `verify`、`obs-contract`、`obs-smoke-offline`、`obs-config-check` 的 Level 0 入口；质量门控：默认目标不启动 Docker、不读取真实 LLM/Langfuse credential，任一子检查失败必须返回非零退出码。
- [X] T010 在 `specs/003-real-observability-backends/checklists/requirements.md` 补充任务生成后的需求可追溯审查结果；质量门控：逐项引用 FR/SC、tasks phase 与未解决风险，不得把“已有任务”误记为“已实现”。

## Phase 2：Foundational - 应用遥测与验证公共能力

**目标**：建立所有用户故事共享的安全配置值对象、单一 OTel provider、传播、指标、结构化日志和机器可读 smoke 报告能力。

**独立验收**：在无 Docker、无外部服务环境运行 `go test -race ./internal/cmd/... ./internal/observability/... ./pkg/ai/obs/...`，可以证明配置 fail-fast、单 Provider 生命周期、gRPC/HTTP override、身份传播、高基数禁区和报告隐私边界。

### Tests - RED

- [X] T011 [P] 在 `internal/cmd/observability_runtime_config_test.go` 编写 `ObservabilityRuntimeConfig` 表驱动 RED 测试；质量门控：覆盖 noop/local/collector、缺 endpoint、非法 protocol/timeout、production insecure 未授权、配置快照不保留 header 值，并先确认目标断言失败。
- [X] T012 [P] 在 `pkg/ai/obs/payload_policy_test.go` 编写 payload mode 的 RED 测试；质量门控：覆盖 metadata-only、redacted、raw 的 local/test 显式授权门禁、标准导出快照脱敏与不可序列化的本地 raw 调试工件；synthetic secret/PII 在全部可外发快照中命中数为 0。
- [X] T013 [P] 在 `internal/cmd/observability_lifecycle_test.go` 扩展单一全局 Provider RED 测试；质量门控：覆盖一次初始化、重复初始化拒绝/复用、shutdown 幂等、flush 超时与无 exporter 模式，使用 OTel test exporter 且不外连。
- [X] T014 [P] 在 `internal/cmd/observability_exporter_test.go` 编写 OTLP exporter 配置 RED 测试；质量门控：覆盖默认 gRPC、HTTP/protobuf override、resource/header/TLS/timeout/sampling 映射和仅保存 header env 名，禁止 backend-specific endpoint。
- [X] T015 [P] 在 `internal/cmd/observability_propagation_test.go` 编写 TraceContext+Baggage 传播 RED 测试；质量门控：证明 OTel trace/span 由 SpanContext 传播，baggage 仅包含 allowlist 低敏身份且不会把 `ai_trace_id` 猜成 TraceID。
- [X] T016 [P] 在 `internal/cmd/request_context_test.go` 编写 request identity 中间件 RED 测试；质量门控：覆盖生成/接受合法 `X-Request-ID`、拒绝超长或非法字符、response header/meta 一致、并发请求不共享身份。
- [X] T017 [P] 在 `internal/observability/metrics_test.go` 编写首批指标 RED 测试；质量门控：覆盖 HTTP/LLM/eval/score/queue instruments 和允许标签集合，并明确断言 request/trace/AI/session/smoke ID、raw route、prompt hash 不得成为 labels。
- [X] T018 [P] 在 `internal/observability/logging_test.go` 编写 JSON completion/error log RED 测试；质量门控：覆盖 UTC、request/trace/span、route template、status/duration/error class、AI/smoke 条件字段与 secret/PII/raw payload 零命中。
- [X] T019 [P] 在 `internal/observability/smoke/report_test.go` 编写不可变 `SmokeReport` RED 测试；质量门控：覆盖 run/profile/scenario/marker/check/failure_stage/cleanup 状态聚合、防御性拷贝、过期时间窗口拒绝、smoke 自建临时凭据/数据 cleanup 证据，以及 credential/raw payload 零命中。
- [X] T020 [P] 在 `internal/observability/smoke/schema_test.go` 编写 JSON Schema 契约 RED 测试；质量门控：使用 `contracts/smoke-report.schema.json` 验证 infra/chat/score/privacy/exporter_failure/persistent_queue/storage_failure/score_worker_failure/alert/retention/platform_contract/full 场景及 marker、每个 check 的 failure_stage、临时凭据/数据 cleanup 字段；同时覆盖缺字段、非法 backend、额外属性、非法 failure_stage、负 duration 失败，先确认 validator 缺失导致测试失败。
- [X] T021 [P] 在 `internal/observability/smoke/poller_test.go` 编写限定时间窗口轮询 RED 测试；质量门控：使用 fake clock 覆盖立即成功、延迟成功、超时、旧 marker、context cancel，禁止 `time.Sleep()`。
- [X] T022 [P] 在 `internal/observability/privacy/scanner_test.go` 编写 synthetic canary 扫描 RED 测试；质量门控：覆盖 API/log/queue/backend/report 文本、常见 token/Authorization/PII 形式和 redacted 值，输出只包含类别与计数。

### Implementation - GREEN/REFACTOR

- [X] T023 在 `internal/cmd/observability_runtime_config.go` 实现不可变运行配置值对象与 fail-fast matrix；质量门控：使 T011 GREEN，错误只暴露字段名/credential-present，不允许应用配置出现 Tempo/Loki/Prometheus/Grafana/SigNoz/Langfuse trace 地址。
- [X] T024 在 `pkg/ai/obs/payload_policy.go` 实现独立 payload policy 与强制敏感检测入口；质量门控：使 T012 GREEN，核心包不依赖平台 SDK，`content_raw` 只在 local/test 显式授权时生成不可序列化的本地调试工件，生产 raw 直接失败。
- [X] T025 在 `internal/cmd/observability_lifecycle.go` 收紧单一 TracerProvider/MeterProvider lifecycle；质量门控：使 T013 GREEN，初始化和 shutdown 幂等、批处理可 flush、失败不 panic，并运行 `go test -race ./internal/cmd`。
- [X] T026 在 `internal/cmd/observability_exporter.go` 实现 traces+metrics 共用配置的窄 OTel SDK 初始化器及 gRPC/HTTP 分支；质量门控：使 T014 GREEN，只创建 App -> Collector exporters，复用 GoFrame 自动 tracing，不注册互相竞争的全局 provider。
- [X] T027 在 `internal/cmd/observability_propagation.go` 装配 W3C TraceContext 与 allowlist Baggage propagator；质量门控：使 T015 GREEN，禁止任意 baggage 透传，跨服务测试不得出现 raw payload。
- [X] T028 在 `internal/cmd/request_context.go` 实现 request identity 中间件；质量门控：使 T016 GREEN，使用 opaque 低敏 ID、route template 而非 raw path，并保持 health endpoint 回归测试通过。
- [X] T029 在 `internal/observability/metrics.go` 实现 OTel Meter instruments 与低基数属性 builder；质量门控：使 T017 GREEN，业务代码只调用语义端口，`go test -race` 无共享状态竞争。
- [X] T030 在 `internal/observability/logging.go` 实现 GoFrame `glog` JSON 结构化字段构造；质量门控：使 T018 GREEN，不接 OTel logs SDK，不记录 provider body、prompt/output、Authorization 或 endpoint credential。
- [X] T031 在 `internal/observability/smoke/report.go` 实现报告值对象与稳定错误分类；质量门控：使 T019 GREEN，报告生成后不可变，失败检查不能被后续 passed 覆盖，报告完成前必须记录 smoke 自建临时凭据/数据的 cleanup 结果。
- [X] T032 在 `internal/observability/smoke/schema.go` 实现本地 schema 校验器；质量门控：使 T020 GREEN，禁止通过网络解析 `$schema`，错误包含 JSON path 且不回显敏感值。
- [X] T033 在 `internal/observability/smoke/poller.go` 实现 context 驱动的 bounded poller；质量门控：使 T021 GREEN，查询必须同时使用 run marker 与 `[started_at, deadline]`，超时后迟到数据不得满足后续 run，并运行 `go test -race ./internal/observability/smoke`。
- [X] T034 在 `internal/observability/privacy/scanner.go` 实现统一 synthetic canary/敏感模式扫描器；质量门控：使 T022 GREEN，scanner 在所有 payload modes 启用并只返回低敏分类证据。
- [ ] T035 在 `manifest/config/config.yaml` 落地 runtime configuration contract 的非敏感默认 shape；质量门控：默认观测、chat 与 infra-smoke 均关闭，默认 App endpoint 只能指向 Collector，`make obs-config-check` 通过。
- [X] T036 在 `Makefile` 增加 `obs-coverage`、foundational 单测与 race 聚合入口；质量门控：显式覆盖 `./internal/cmd/... ./internal/observability/... ./internal/logic/chat/... ./pkg/ai/obs/...`，生成 ignored 的 `build/coverage/observability.out`。新核心代码是本 spec 新增或修改的、位于上述包中的非 generated/non-config-only 可执行 Go 行；以 merge-base 为基线，从同一测试运行生成的 coverprofile 中只统计这些行，分母为所有此类可执行行、分子为其中命中行，阈值 >=80%。chat usecase 按 `internal/logic/chat` 的全部非 generated 可执行行单独计算，阈值 >=90%。基线不存在时以首次提交前的空树为基线；重命名按新路径计入。generated/config-only 排除项必须使用可审查 allowlist，不得排除失败包、既有未覆盖行或复用缓存假通过。
- [X] T036A [P] 在 `internal/cmd/observability_bootstrap_test.go` 编写应用观测装配 RED 测试；质量门控：先固定 `enabled`/mode/signal/smoke 的唯一语义，覆盖 noop/local 零网络、collector 恰好一次创建和全局安装 trace+meter provider、初始化失败 fail-fast、重复装配不产生第二套 provider/middleware，以及带 deadline 的 flush/shutdown；同时固定 HTTP ingress 对 `traceparent`/`tracestate` 的信任策略，证明不可信 remote sampled 标记不绕过采样预算、未审计 tracestate 不会自动跨第三方传播。测试不得读取真实 secret 或启动 server。
- [X] T036B 在 `internal/cmd/observability_bootstrap.go` 实现配置加载到 `ObservabilityProviderLifecycle` 的纯装配边界；质量门控：使 T036A GREEN，按“resolve runtime → resource/exporter config → transient header source → exporter bundle → provider lifecycle”的固定顺序组合，不让 `BuildObservabilityOTLPExporterConfig` 重复解析 raw input，只有 composition root 可以成对声明 exporter provider 所有权。
- [X] T036C 在 `internal/cmd/observability.go` 删除旧 `ObservabilitySinkPlatform` 直连平台模型及其测试；质量门控：新 bootstrap 契约已通过后执行，仓库扫描不得留下 `ExternalEndpoint`、`Platform` sink 或应用侧 platform credential 字段；缺 Collector 配置的 collector mode 必须 fail-fast，禁止静默降级 noop。
- [X] T036D 在 `internal/cmd/observability_lifecycle.go` 将 tracer-only 命名收敛为 `ObservabilityProviderLifecycle`；质量门控：更新相邻测试与 bootstrap 调用，行为不变，明确该对象仅管理 trace/meter provider 和 OTLP bundle，不管理 Langfuse score worker。

## Phase 3：User Story 1 - 验证服务请求的基础观测（P1）

**用户故事目标**：启动 Grafana 经典主线后，通过受保护的 infra-smoke 请求自动查到 Tempo trace、Loki JSON log 和 Prometheus 指标增量，并反证 AI pipeline/Langfuse 没有该运行。

**独立测试标准**：运行 `make obs-infra-smoke`，60 秒内输出符合 schema 的 passed 报告；报告证明 API envelope/header 一致、三类基础信号可查、AI filter 输出增量为 0、Langfuse 无 marker；关闭 smoke 时端点返回 404。

### Tests - RED

- [X] T037 [P] [US1] 在 `api/v1/observability/infra_smoke_test.go` 编写 Go API 契约 RED 测试；质量门控：覆盖成功 envelope、始终存在 request_id、合法/非法 run marker、`ai_trace_id`/`eval_summary` 必须缺失和 disabled 404。
- [X] T038 [P] [US1] 在 `internal/logic/observability/infra_smoke_test.go` 编写 usecase RED 测试；质量门控：覆盖 run marker 进入 span/log、指标增加、无 AI identity/marker、观测 adapter 失败不改写业务 `ok`，使用内存 exporter。
- [X] T039 [P] [US1] 在 `internal/controller/observability/infra_smoke_test.go` 编写 controller RED 测试；质量门控：覆盖参数边界、统一 envelope、404 和内部错误脱敏，controller 不直接访问 OTel backend。
- [X] T040 [P] [US1] 在 `internal/cmd/routes_observability_test.go` 编写路由门控 RED 测试；质量门控：证明启用时注册 GET 路由、关闭时不可达、超限请求返回 429，重复装配不产生双路由或重复 middleware。
- [X] T041 [P] [US1] 在 `internal/observability/http_logging_test.go` 编写 HTTP completion log RED 测试；质量门控：覆盖 infra-smoke 成功/错误字段、真实 SpanContext identity、route template 和 synthetic secret 零命中。
- [X] T042 [P] [US1] 在 `hack/observability/collector_grafana_config_test.sh` 编写 Collector 主线静态 RED 测试；质量门控：断言 traces ingress+forward/infra+forward/ai、metrics、filelog、稳定 component IDs、三套 persistent queues、AI filter、二次字段删除，以及 smoke/error/regression/指定 AI trace 的完整保留采样。
- [X] T043 [P] [US1] 在 `hack/observability/glog_filelog_fixture_test.sh` 编写 glog->filelog->Loki 配置映射 RED 测试；质量门控：fixture 覆盖 JSON、trace correlation、malformed line 隔离、文件 rotation、Loki native OTLP/structured metadata/低基数 label 与 secret/raw payload 丢弃，作为 T055/T058 的先行测试。
- [X] T044 [P] [US1] 在 `hack/observability/compose_grafana_test.sh` 编写 Grafana profile 与 wiring 静态 RED 测试；质量门控：覆盖固定镜像、loopback 端口、healthcheck、resource limit、独立 volume/retention、Tempo/Prometheus 配置引用、Grafana datasource UID、Langfuse 依赖、Make target dry-run 和 12GiB/8vCPU/20GiB 预算声明，作为 T056/T057/T059/T060/T066 的先行测试。
- [X] T045 [P] [US1] 在 `hack/observability/grafana_dashboard_test.go` 编写基础 dashboard JSON RED 测试；质量门控：断言 request/error/latency、export failure、queue、日志到 trace 关联查询均使用 provisioned datasource UID 和低基数 labels。
- [X] T046 [P] [US1] 在 `hack/observability/grafana_alerts_test.go` 编写基础告警规则 RED 测试；质量门控：覆盖 HTTP error、exporter send/enqueue failure、queue saturation/age、storage pressure 的 firing/resolved 条件和稳定 component ID。
- [X] T047 [P] [US1] 在 `internal/observability/backend/grafana_query_test.go` 编写 Tempo/Loki/Prometheus/Grafana 查询客户端 RED 测试；质量门控：httptest 覆盖成功、认证/超时/5xx/畸形响应和旧数据，错误只保留 backend+error_class。
- [X] T048 [P] [US1] 在 `internal/observability/smoke/infra_runner_test.go` 编写 infra E2E runner RED 测试；质量门控：使用 fake backend 证明基线/增量查询、AI 负向断言、60 秒 deadline、cleanup 和 schema report，禁止只检查容器 healthy。

### Implementation - GREEN/REFACTOR

- [X] T049 [US1] 在 `api/v1/observability/infra_smoke.go` 定义 GoFrame Req/Res 与统一 envelope 类型；质量门控：使 T037 GREEN，字段与 `contracts/http-api.yaml` 一致，run marker 最大 128 字节且不接受额外输入。
- [X] T050 [US1] 在 `internal/logic/observability/infra_smoke.go` 实现纯基础设施 usecase；质量门控：使 T038 GREEN，不创建 `ai_trace_id` 或 AI plane marker，telemetry failure 只记录诊断事实。
- [X] T051 [US1] 在 `internal/controller/observability/infra_smoke.go` 实现薄 controller；质量门控：使 T039 GREEN，只做校验/调用/错误映射，不包含 backend 查询或业务观测拼装。
- [X] T052 [US1] 在 `internal/cmd/cmd.go` 消费 T036B 已准备的应用观测装配结果，在启动前初始化并在 server 退出路径以 deadline flush/shutdown，然后注册受配置保护的 infra-smoke 路由、request middleware 与配置化限流；质量门控：使 T040 GREEN，并运行 health 与路由回归测试，避免重复 provider/middleware，限流状态不得进入 AI 平面。
- [X] T053 [US1] 在 `internal/observability/http_logging.go` 实现 HTTP completion/error 日志 hook；质量门控：使 T041 GREEN，日志写 JSONL 到配置文件且业务响应不等待 Loki。
- [X] T054 [US1] 在 `deploy/observability/collector/collector-grafana.yaml` 实现方案 C、metrics/filelog、完整 trace tail sampling 与三套独立持久队列配置；质量门控：使 T042 GREEN，公共 processor 只在 ingress，infra-only 数据不能通过 AI downstream filter，smoke/error/regression 不能被采样丢弃。
- [X] T055 [US1] 在 `manifest/config/glog-observability.yaml` 配置结构化 JSON 文件、rotation 与 shared-volume 路径；质量门控：应用侧 JSONL 契约完成；T043 的 Loki 侧断言待 T058 关闭。
- [X] T055A [US1] 在 composition root 显式加载 glog JSONL 配置，创建受控 completion writer 并接入 infra-smoke HTTP middleware；质量门控：只写 allowlist completion 字段，不直接连接 Loki。
- [X] T056 [US1] 在 `deploy/observability/compose.grafana.yaml` 实现 Collector+Prometheus+Loki+Tempo+Grafana+Langfuse 的主线 profile；质量门控：使 T044 GREEN，应用只连接 Collector，所有持久 volume 与应用数据库隔离，服务 healthcheck 不含 secret。
- [X] T057 [P] [US1] 在 `deploy/observability/tempo/tempo.yaml` 配置 OTLP 接收、7 天 retention 与本地 volume；质量门控：Tempo 配置独立校验通过；T044 Compose 侧验收待 T056 汇合。
- [X] T058 [P] [US1] 在 `deploy/observability/loki/loki.yaml` 配置 native OTLP、structured metadata 与 7 天 retention；质量门控：T043 GREEN，与 filelog attributes 一致，拒绝把高基数 request/trace ID 设为 index label。
- [X] T059 [P] [US1] 在 `deploy/observability/prometheus/prometheus.yaml` 配置应用指标和 Collector self-telemetry scrape；质量门控：Prometheus scrape 契约通过；15 天 retention 由 T056 service command 实施，不使用 remote write，不按 run/request ID 查询指标。
- [X] T060 [P] [US1] 在 `deploy/observability/grafana/provisioning/datasources.yaml` provision Prometheus/Loki/Tempo datasource 与 trace/log correlation；质量门控：datasource 契约通过，固定 datasource UID、使用 compose 内部 URL、不写 exporter credential。
- [X] T061 [US1] 在 `deploy/observability/grafana/dashboards/observability-overview.json` provision 基础 dashboard；质量门控：使 T045 GREEN，面板回答请求量、错误、延迟、出口失败、积压及日志/trace 关联问题，不使用 request ID metric label。
- [X] T062 [US1] 在 `deploy/observability/grafana/alerts/observability.rules.yaml` provision 基础告警；质量门控：使 T046 GREEN，四类告警都有可测试 firing/resolved 条件、for/window 和低敏 annotations。
- [X] T063 [US1] 在 `internal/observability/backend/grafana_query.go` 实现四类后端只读查询端口；质量门控：使 T047 GREEN，所有调用带 context timeout，response body 有大小上限且不进入错误日志。
- [X] T064A [US1] 在 `internal/observability/smoke/infra_runner_test.go` 扩展 infra runner RED 契约；质量门控：覆盖同一 bounded deadline 下的异步 Tempo/Loki 轮询、窗口外 marker、零 HTTP 增量、Langfuse/AI 非零、后端失败、失败 report 的 failure_stage/error_class/cleanup，以及 version-controlled schema 校验，禁止用历史时间或宽松临时 schema 掩盖生产语义。
- [X] T064B [US1] 在 `internal/observability/backend/grafana_smoke_adapter_test.go` 编写 backend result→smoke evidence adapter RED 测试；质量门控：覆盖 Tempo/Loki marker+窗口解码、Prometheus 低基数 route/status count、Grafana/Langfuse/AI negative count、`ErrStaleQueryWindow`/BackendQueryError 到报告允许 error_class 的稳定映射，以及原始 response 不得进入 DTO/report。
- [X] T064C [US1] 在 `internal/observability/backend/grafana_smoke_adapter.go` 实现窄 backend evidence adapter；质量门控：使 T064B GREEN，唯一允许解码 T063 `BackendQueryResult`，Tempo/Loki 必须先以 target 的精确 marker attribute 和当前窗口构造查询、再只投影低敏 marker observation/count/error class，禁止 command 或 runner 直接解析 Tempo/Loki/Prometheus 原始 JSON。
- [X] T064D [US1] 在 `internal/observability/smoke/infra_runner.go` 实现唯一 identity 的完整 infra runner；质量门控：使 T064A 与 T048 GREEN，固定执行 baseline→受保护 infra request→bounded Tempo/Loki poll→after count→Langfuse/AI negative→cleanup；marker 必须由 run identity 安全派生且不可复用；任一失败仍返回 schema-valid 低敏 report，并使用真实/注入 clock 的完成时间。
- [X] T064E [US1] 在 `cmd/obs-smoke/main_test.go` 编写 infra CLI 装配 RED 契约；质量门控：固定 T064 stable report/error 到退出码的映射、唯一 marker 只由 runner/identity factory 生成、stdout 只输出低敏 status/report path，缺配置在任何后端请求前失败。
- [X] T065A [US1] 在 `specs/003-real-observability-backends/contracts/runtime-configuration.md` 与 `docs/adr/` 固化 infra smoke 的 Langfuse/AI-plane 负向查询端口、认证引用、最小权限、固定查询窗口、响应上限与稳定 error class；质量门控：只记录环境变量名和 `credential_present`，不猜测 Langfuse 查询协议或把 credential/endpoint/raw response 写入 config、report 或 CLI 输出。
- [X] T065B [US1] 在 `internal/observability/backend/grafana_infrastructure_smoke_test.go`、`internal/observability/backend/langfuse_smoke_query_test.go` 编写 runner backend/negative-query RED 契约；质量门控：用 `httptest` 固定 Tempo/Loki 委托、Prometheus 低基数 baseline/after count、Langfuse 与 AI-plane 精确 marker 负向 count、timeout/认证/畸形响应稳定映射，禁止原始 JSON 跨出 backend 包。
- [X] T065C [US1] 在 `internal/observability/backend/grafana_infrastructure_smoke.go`、`internal/observability/backend/langfuse_smoke_query.go` 实现 `smoke.InfrastructureSmokeBackend` 的低敏证据适配器；质量门控：使 T065B GREEN，复用 T063/T064C 查询边界，runner/CLI 不解码原始 response，缺少端口或 credential reference 时不伪造零计数而 fail-fast。
- [X] T065D [US1] 在 `cmd/obs-smoke/main_test.go` 扩展默认装配与 protected HTTP trigger RED 契约；质量门控：固定 GET `/api/v1/observability/infra-smoke` 与 API 定义的 marker header、60 秒 bounded deadline、配置校验先于任何 client/请求、固定 ignored 报告目录及 path/symlink escape 拒绝。
- [X] T065 [US1] 在 `cmd/obs-smoke/main.go` 增加仅负责依赖装配的 `infra` scenario 命令入口；质量门控：使 T064E/T065D GREEN，行为委托给 T048 已测试 runner，缺配置时 fail-fast、报告写入 ignored 目录、退出码与 report status 一致，运行 `go test ./cmd/obs-smoke` 与 `go build ./cmd/obs-smoke`，stdout 不打印 credential/raw data。
- [X] T064 [US1] 汇合 T064A–T064E 与 T065A–T065D 的 infra smoke 闭环；质量门控：T048、T064A、T064B、T064E 与 T065D 全部 GREEN，runner 只消费 adapter 的低敏 evidence、所有失败都有 schema-valid report，随后才允许开发 T066。
- [X] T066 [US1] 在 `Makefile` 增加 `obs-grafana-up/down`、`obs-stack-health`、`obs-infra-smoke`、`obs-grafana-e2e`；质量门控：满足 T044 的 Make wiring 先行断言，up/down 幂等，E2E 不接受仅 healthy 为通过，失败时仍执行 cleanup 并保留低敏报告。
- [X] T066A [US1] 在 `deploy/observability/compose.langfuse.yaml`、`Makefile` 与部署运行手册拆分首次 Langfuse cold bootstrap 和常规 Grafana warm start；质量门控：bootstrap 不解析 Collector project-key 变量，创建项目 key 后 warm start 仍对这两个变量 fail-fast，两个路径复用显式 Compose project 与 Langfuse named volumes，且本地配置模板不含凭据。
- [X] T066B [US1] 在 Langfuse self-hosted Compose 与本地环境模板补齐稳定的 `ENCRYPTION_KEY`；质量门控：web/worker 共享同一个 fail-fast 环境变量，模板只说明本地生成方式，文档禁止在既有数据卷上随意轮换该密钥。
- [X] T066C [US1] 补齐 Langfuse v3 冷启动的 ClickHouse 身份与必需事件对象存储；质量门控：web/worker 显式配置 ClickHouse user/password、单节点 migration 模式与 MinIO S3 event upload，one-shot 初始化任务幂等建桶，MinIO 不发布宿主机端口，且不删除既有 Langfuse named volumes。
- [X] T066D [US1] 兼容已存在的失败冷启动 ClickHouse 卷；质量门控：在 web/worker 启动前以幂等 one-shot task 创建并授权专用 `langfuse` 用户，避免把删除 named volume 作为修复手段。
- [X] T066E [US1] 修复 Langfuse web 冷启动的 Node heap 耗尽；质量门控：web 容器分配 `2GiB`，Node old-space 限制为 `1536MiB` 并保留 native memory headroom，合并 profile 仍不超过 `12GiB` 声明上限。
- [X] T066F [US1] 移除 Langfuse worker 的伪 healthcheck；质量门控：不假设 `3.185.0` 镜像内存在未发布的脚本或 endpoint，worker 只依赖已完成初始化任务，web health 仍为冷启动服务就绪证据。
- [X] T066G [US1] 修复 Grafana profile 的 Collector 首次队列卷启动；质量门控：三套 `file_storage` 显式创建子目录，受限的一次性 root 初始化器仅为队列目录移交 `10001:<宿主机 GID>`，Collector 保持 non-root，不要求手工 chown 或删除 named volume。
- [X] T066H [US1] 修复 Grafana profile 的真实服务就绪检查；质量门控：Loki retention 配置有效，Prometheus 使用镜像内置探针，Collector/Loki/Tempo 由最小权限 sidecar 请求真实 HTTP readiness endpoint，`make obs-grafana-up` 在保留 named volumes 的 cold/warm restart 下通过。
- [X] T067 [US1] 在 `specs/003-real-observability-backends/checklists/grafana-infra.md` 编写主线基础设施验收清单；质量门控：本阶段只关闭 SC-001，SC-006 告警项明确标记为“已 provision、待 US3 firing/resolved 验证”，所有通过结论都要求机器可读证据，不以手工 UI 截图单独通过。

## Phase 4：User Story 2 - 追溯一次真实 AI 对话（P1）

**用户故事目标**：`POST /api/v1/chat` 调用服务端配置的真实 OpenAI-compatible 模型，并关联 HTTP/OTel/AI identity、generation、usage/cost-ready、低敏 eval evidence 与 Langfuse score。

**独立测试标准**：显式配置真实上游与 Langfuse 后运行 `make obs-chat-smoke obs-langfuse-score-smoke obs-privacy-platform-smoke`；60 秒内查到双平面 trace，120 秒内查到 score 或明确 projection failure，业务响应始终含 request_id/ai_trace_id，debug eval summary <=1KiB。

### Tests - RED

- [X] T068 [P] [US2] 在 `api/v1/chat/chat_test.go` 编写 chat API 契约 RED 测试；质量门控：覆盖 32KiB 边界、空白/超长/额外字段、成功/400/429/502/504 envelope；request_id 始终存在，成功及 AI usecase 已启动后的 502/504 必须保留 ai_trace_id，usecase 前的 400/429 不得伪造 AI identity，debug summary 仅按配置出现。
- [X] T069 [P] [US2] 在 `internal/cmd/llm_provider_test.go`、`pkg/ai/resilience/provider_wrapper_test.go` 与 `pkg/ai/resilience/provider_retry_test.go` 编写 OpenAI-compatible 装配及通用执行策略 RED 测试；质量门控：cmd 覆盖 chat disabled、缺 base URL/key/model fail-fast、secret 不进入快照/错误、注入 fake provider 时零外连；resilience 覆盖 60 秒总 deadline、最多两次指数退避且 4xx 不重试、仅首个 stream chunk 前可重试、取消感知转发与 terminal stream outcome。固定 `ProviderWrapper(adapter)` 在一次用户调用中只记录一次 breaker/outcome/总 latency，`pkg/ai/llm` 不导入 adapter 专属配置。
- [X] T070 [P] [US2] 在 `internal/logic/chat/chat_test.go` 编写 chat usecase RED 测试；质量门控：覆盖成功、upstream/rate-limit/timeout、AI ID 在 provider 调用前生成、模型实际身份/usage/finish reason、telemetry failure 不改写业务结果，usecase 覆盖率目标 >=90%。
- [X] T071 [P] [US2] 在 `pkg/ai/obs/otel_mapper_test.go` 扩展 AI plane/GenAI 映射 RED 测试；质量门控：root/bridge 与 generation/evaluator 有 marker，普通 HTTP/DB/Redis child 无 marker，缺语义字段不得被 adapter 猜测。
- [X] T072 [P] [US2] 在 `internal/observability/generation_test.go` 编写 generation span RED 测试；质量门控：覆盖 parent/SpanContext、requested/actual model、token 类型、latency/outcome/prompt identity/payload mode，非流式不得伪造 TTFT。
- [X] T073 [P] [US2] 在 `internal/eval/evidence_store_test.go` 编写本地 evidence 持久化 RED 测试；质量门控：覆盖先持久化后投影、防御性副本、进程重开可读取、并发追加、磁盘失败可诊断和 raw content 零命中。
- [X] T074 [P] [US2] 在 `internal/logic/chat/evaluator_test.go` 编写确定性 completion-contract evaluator 与 debug summary RED 测试；质量门控：定义第一阶段低敏 metric/threshold，覆盖 passed/warning/failed/not_run、evidence correlation、序列化 <=1024 bytes、非 debug 缺失和 reason_class 不含原文。
- [X] T075 [P] [US2] 在 `internal/controller/chat/chat_test.go` 编写 controller RED 测试；质量门控：覆盖 JSON 校验、错误码映射、header/meta 一致、upstream body/endpoint/credential 不回显，controller 只依赖 usecase 接口。
- [X] T076 [P] [US2] 在 `internal/cmd/routes_chat_test.go` 编写 chat 路由与 middleware RED 测试；质量门控：覆盖启用/关闭、request context 传播、配置化限流 429、真实 AI usecase 启动时才打 AI marker、infra route 不受影响。
- [X] T077 [P] [US2] 在 `internal/observability/langfuse/trace_mapper_test.go` 编写 Langfuse OTLP attribute mapper RED 测试；质量门控：覆盖 allowlist、平台属性仅在 adapter、OTel TraceID/SpanID 与 ai_trace_id 分离、metadata/redacted/raw 三模式，且 raw 本地工件不进入 mapper、所有平台属性 secret 零命中。
- [X] T078 [P] [US2] 在 `internal/observability/langfuse/projection_test.go` 编写 `ScoreProjection` 状态机 RED 测试；质量门控：覆盖稳定幂等 ID、真实 platform trace/observation ID 必填、queued/sending/retry/sent/drop/permanent/shutdown 状态且不修改 evidence。
- [X] T079 [P] [US2] 在 `internal/observability/langfuse/client_test.go` 编写 Langfuse score API client RED 测试；质量门控：httptest 覆盖 Basic Auth、稳定 score ID、timeout/429/5xx/4xx 分类、body 上限和日志不泄漏 secret/evidence 原文。
- [X] T080 [P] [US2] 在 `internal/observability/langfuse/worker_test.go` 编写有界异步 worker RED 测试；质量门控：覆盖非阻塞 enqueue、queue full、指数退避、幂等重试、优雅 shutdown timeout、race 与本地 evidence 不丢失，禁止 `time.Sleep()`。
- [X] T081 [P] [US2] 在 `internal/cmd/langfuse_score_lifecycle_test.go` 编写 score worker 装配 RED 测试；质量门控：覆盖未配置为 not_configured、配置完整启动一次、shutdown flush、有界队列指标和不阻塞 HTTP lifecycle。
- [X] T082 [P] [US2] 在 `hack/observability/langfuse_collector_test.sh` 编写 Langfuse downstream 静态 RED 测试；质量门控：断言 OTLP/HTTP protobuf、`/api/public/otel`、ingestion version header、secret env 注入、AI filter、独立 queue/retry/timeout/file_storage。
- [X] T083 [P] [US2] 在 `hack/observability/langfuse_compose_test.sh` 编写 self-hosted Langfuse 静态 RED 测试；质量门控：覆盖固定版本、依赖服务、healthcheck、14 天 retention、无 raw volume、loopback UI 和无默认 credential。
- [X] T084 [P] [US2] 在 `hack/observability/grafana_ai_dashboard_test.go` 编写 AI dashboard RED 测试；质量门控：断言 LLM request/duration/token/cost-ready、eval regression、score projection/queue 与 infra trace correlation 面板存在且 labels 合规。
- [X] T085 [P] [US2] 在 `internal/observability/smoke/chat_runner_test.go` 编写 chat smoke runner RED 测试；质量门控：fake API/backends 覆盖 60 秒双平面关联、marker/identity 匹配、模型失败与 telemetry 失败分离、schema-valid report。
- [X] T086 [P] [US2] 在 `internal/observability/smoke/score_runner_test.go` 编写 score smoke runner RED 测试；质量门控：覆盖 120 秒异步成功、not_configured、retry、稳定 ID 重试不重复、本地 evidence 先存在和 projection failure 可诊断。
- [X] T087 [P] [US2] 在 `internal/observability/smoke/privacy_runner_test.go` 编写真实平台隐私 runner RED 测试；质量门控：使用 fake query clients/counting transport 离线模拟 API/log/Collector queue report/Tempo/Loki/Langfuse trace+score/report，synthetic forbidden marker 未脱敏命中必须为 0，RED 测试不得启动 Docker 或访问真实平台。

### Implementation - GREEN/REFACTOR

- [X] T088 [US2] 在 `api/v1/chat/chat.go` 定义 Chat Req/Res、Usage、EvalSummary 与 envelope；质量门控：使 T068 GREEN，与 OpenAPI 完全一致，客户端不能提交 provider/model/base URL/key/debug。
- [X] T089 [US2] 在 `pkg/ai/resilience/provider_wrapper.go`、`pkg/ai/resilience/provider_retry.go` 与 `internal/cmd/llm_provider.go` 完成 OpenAI-compatible 装配和通用执行策略重构；质量门控：使 T069 GREEN，cmd 只处理 env 引用、production URL/redirect transport 防护、safe snapshot、offline fake 和 adapter 构建；resilience 以单一 `ProviderWrapper` 执行总 deadline、retry、stream forwarding/terminal 语义、错误归一化、breaker 与 outcome 观测，不能再保留 cmd retry wrapper。stream 必须以 terminal 而非首连成功结算 breaker/outcome；调用方取消不得计为 upstream failure，deadline 映射为 timeout outcome。adapter 继续拥有协议映射及其专属 `Config`/`NewProvider`，logic 只选择业务策略。
- [X] T090 [US2] 在 `internal/logic/chat/chat.go` 实现 chat usecase 的端口编排；质量门控：使 T070 GREEN，按“生成 AI identity -> 启动 root/bridge -> 调用 provider -> 调用 T094 evaluator -> 持久化 T093 evidence -> 非阻塞投影”的顺序组合已有职责，文件本身不重复实现 mapper/store/worker，观测/score 失败绝不覆盖模型业务结果。
- [X] T091 [US2] 在 `pkg/ai/obs/otel_mapper.go` 增加标准 GenAI 与 AI plane 显式映射；质量门控：使 T071 GREEN，只映射事实模型已有字段，Langfuse 专属属性不得进入核心 mapper。
- [X] T092 [US2] 在 `internal/observability/generation.go` 实现 generation/evaluator span adapter；质量门控：使 T072 GREEN，从活动 SpanContext 获取真实平台 identity，不把 domain AI ID 伪造成 OTel ID。
- [X] T093 [US2] 在 `internal/eval/evidence_store.go` 实现低敏本地 evidence JSONL 存储；质量门控：使 T073 GREEN，原子/并发安全、错误可诊断、保留 90 天配置边界，不成为 Langfuse 专属事实源，并运行 `go test -race ./internal/eval/...`。
- [X] T094 [US2] 在 `internal/logic/chat/evaluator.go` 实现确定性 completion-contract evaluator、泛型 evaluator 端口、evidence 输入与有界低敏摘要；质量门控：使 T074 GREEN，明确该实现只检查完成事实契约而非语义质量，评估事实不依赖 Langfuse，debug 只控制响应诊断，不改变 payload policy 或敏感扫描。
- [X] T095 [US2] 在 `internal/controller/chat/chat.go` 实现薄 controller；质量门控：使 T075 GREEN，使用统一错误 envelope 与稳定错误码，不记录 request body/provider body。
- [X] T096 [US2] 在 `internal/cmd/cmd.go` 在既有 T052 装配结果上注册 chat 路由、配置化限流与 usecase 依赖；质量门控：使 T076 GREEN，运行 health/infra/chat 路由回归，单进程仍只有一个 provider lifecycle，限流不暴露用户输入，不得重新解析或重新安装 telemetry provider。
- [X] T097 [US2] 在 `internal/observability/langfuse/trace_mapper.go` 实现平台 allowlist 映射；质量门控：使 T077 GREEN，平台 adapter 只投影，不反向决定 `pkg/ai/obs.Trace` 或 eval evidence。
- [X] T098 [US2] 在 `internal/observability/langfuse/projection.go` 实现不可变 score projection 与稳定幂等键；质量门控：使 T078 GREEN，缺真实 platform identity 时 fail-fast，不通过名称/时间窗口猜测。
- [X] T099 [US2] 在 `internal/observability/langfuse/client.go` 实现 score API client；质量门控：使 T079 GREEN，context timeout、分类重试、response body 上限、credential 只在 Authorization header。
- [X] T100 [US2] 在 `internal/observability/langfuse/worker.go` 实现有界异步队列与指标；质量门控：使 T080 GREEN，`go test -race` 通过，queue full/shutdown timeout 形成明确 projection 状态且 chat enqueue 非阻塞。
- [X] T101 [US2] 在 `internal/cmd/langfuse_score_lifecycle.go` 装配 score worker lifecycle；质量门控：使 T081 GREEN，evidence 必须先持久化，worker 未配置时返回 not_configured 而非丢弃事实。
- [X] T102 [US2] 在 `deploy/observability/collector/collector-grafana.yaml` 完成 Langfuse AI downstream exporter/transform；质量门控：使 T082 GREEN，只导出带 marker 的 root/bridge 与 semantic spans，infra-only marker 查询为 0。
- [X] T103 [US2] 在 `deploy/observability/compose.langfuse.yaml` 完成固定版本 Langfuse self-hosted 服务与 retention/volume；质量门控：使 T083 GREEN，不创建 raw debug volume，凭据只经 env/secret 注入。
- [X] T104 [US2] 在 `deploy/observability/grafana/dashboards/observability-overview.json` 增加 AI/eval/score 面板；质量门控：使 T084 GREEN，同步覆盖 token/cost/eval，不能用高基数 ID 做 metrics labels。
- [X] T105 [US2] 在 `internal/observability/smoke/chat_runner.go` 实现真实 chat 双平面查询；质量门控：使 T085 GREEN，按响应 identity+run window 查询 Tempo/Loki/Prometheus/Langfuse，业务失败保留原始错误域。
- [X] T106 [US2] 在 `internal/observability/smoke/score_runner.go` 实现 evidence/score 投影查询；质量门控：使 T086 GREEN，120 秒边界内确认 sent 或稳定 failure，不把 delayed/duplicate score 误判为新成功。
- [X] T107 [US2] 在 `internal/observability/smoke/privacy_runner.go` 实现平台 canary 扫描；质量门控：使 T087 GREEN，扫描发生在真实 backend-visible 数据与本地 queue/report，任何未脱敏命中使报告失败。
- [ ] T108 [US2] 在 `cmd/obs-smoke/main.go` 增加 `chat`、`score`、`privacy` scenarios；质量门控：真实调用必须显式 opt-in，缺 credential 在发送前失败，输出仅含低敏 report path/status/identity。
- [ ] T109 [US2] 在 `Makefile` 增加 `obs-chat-smoke`、`obs-langfuse-score-smoke`、`obs-privacy-platform-smoke`、`obs-direct-langfuse-smoke` 并将 `obs-grafana-e2e` 扩展为 infra+chat+score+privacy 聚合门禁；质量门控：direct smoke 仅诊断 ingestion/header 且不得成为正式 E2E 依赖，默认 `make verify` 不调用真实目标。

## Phase 5：User Story 3 - 在观测故障中保持业务可用（P1）

**用户故事目标**：按真实失败域验证 exporter、Prometheus pull、Grafana query、score worker、storage 与模型上游故障，证明业务结果不被观测故障改写，并验证 persistent queue 跨重启恢复。

**独立测试标准**：`make obs-resilience-e2e` 自动运行至少 8 类故障/恢复场景；每个场景有 component-scoped evidence、业务结果断言、120 秒恢复窗口和 cleanup 结果，退出时无残留 paused container。

### Tests - RED

- [ ] T110 [P] [US3] 在 `internal/observability/failure/catalog_test.go` 编写失败域目录 RED 测试；质量门控：覆盖 Tempo/Loki/Langfuse exporter、Prometheus scrape、Grafana query、score worker、queue full、storage unwritable、Collector restart/shutdown 与 model upstream，证据源不得混用。
- [ ] T111 [P] [US3] 在 `internal/observability/failure/docker_control_test.go` 编写受控容器操作 RED 测试；质量门控：使用 fake command runner 覆盖 pause/unpause/restart、project label scope、context timeout、defer cleanup，禁止拼接未校验 shell 参数。
- [ ] T112 [P] [US3] 在 `internal/observability/smoke/exporter_failure_runner_test.go` 编写独立 exporter 故障 RED 测试；质量门控：覆盖单出口失败、其它出口继续、send_failed/enqueue/queue component 归因和 HTTP 结果不变。
- [ ] T113 [P] [US3] 在 `internal/observability/smoke/persistent_queue_runner_test.go` 编写 queue 恢复 RED 测试；质量门控：覆盖 backend pause、产生 marker、Collector restart、backend resume、120 秒 drain、at-least-once duplicate 识别和迟到 marker 隔离。
- [ ] T114 [P] [US3] 在 `internal/observability/smoke/storage_failure_runner_test.go` 编写 storage/queue 极限 RED 测试；质量门控：覆盖 queue exhaustion、磁盘不可写、permanent error、shutdown timeout 与 dropped/failed 证据；另覆盖 `obs-config-check` 对无效 Collector pipeline、缺失/非目录/不可写 storage path 的启动前拒绝和稳定错误类别，不要求业务失败。
- [ ] T115 [P] [US3] 在 `internal/observability/smoke/score_failure_runner_test.go` 编写 score worker 故障 RED 测试；质量门控：覆盖 Langfuse API 失败、queue full、process shutdown、重试幂等，本地 evidence 完整且 chat 响应不变。
- [ ] T116 [P] [US3] 在 `internal/logic/chat/failure_classification_test.go` 编写模型与观测故障分类 RED 测试；质量门控：模型 429/5xx/timeout 映射业务状态，telemetry/score/exporter failure 只记录观测错误，二者不得互相归类。
- [ ] T117 [P] [US3] 在 `internal/observability/smoke/alert_runner_test.go` 编写告警 firing/resolved RED 测试；质量门控：fake Prometheus/Grafana API 覆盖四类告警触发、恢复、超时和 stale alert，不能只验证 rule 文件存在。
- [ ] T118 [P] [US3] 在 `hack/observability/reset_test.sh` 编写安全 reset RED 测试；质量门控：覆盖无确认拒绝、dry-run 预览、仅当前 compose project/observability label、排除应用 DB/无关 volume、被中断测试残留清理。
- [ ] T119 [P] [US3] 在 `internal/observability/smoke/retention_runner_test.go` 编写 retention RED 测试；质量门控：覆盖 Prometheus metrics 15 天、Loki/Tempo 7 天、Langfuse 14 天、evidence/report 90 天和 persistent queue 仅积压保留；普通原文不得作为可观测 payload 保留，local raw 调试工件必须在运行结束时清理。

### Implementation - GREEN/REFACTOR

- [ ] T120 [US3] 在 `internal/observability/failure/catalog.go` 实现失败域到真实证据源的静态映射；质量门控：使 T110 GREEN，Prometheus/Grafana/score worker 不得伪造 `otelcol_exporter_send_failed`。
- [ ] T121 [US3] 在 `internal/observability/failure/docker_control.go` 实现 scoped pause/restart/restore；质量门控：使 T111 GREEN，只调用参数化 command runner，任何退出路径都尝试恢复服务并记录 residual resources，并运行 `go test -race ./internal/observability/failure`。
- [ ] T122 [US3] 在 `internal/observability/smoke/exporter_failure_runner.go` 实现 Tempo/Loki/Langfuse 单出口故障场景；质量门控：使 T112 GREEN，报告分别记录 component sent/failed/enqueue/queue delta 和其它出口成功证据。
- [ ] T123 [US3] 在 `internal/observability/smoke/persistent_queue_runner.go` 实现跨 Collector 重启恢复场景；质量门控：使 T113 GREEN，声明 at-least-once、不宣称 exactly-once，超时或 duplicate 不得静默忽略。
- [ ] T124 [US3] 在 `internal/observability/smoke/storage_failure_runner.go` 实现 queue/storage/shutdown 故障场景；质量门控：使 T114 GREEN，故障注入限定当前 compose project，恢复后验证 Collector writable/healthy，并在报告中保留 `preflight` 与 runtime storage failure 的区别。
- [ ] T125 [US3] 在 `internal/observability/smoke/score_failure_runner.go` 实现 score worker failure 场景；质量门控：使 T115 GREEN，先确认本地 evidence，再注入平台失败，业务状态与 eval 事实保持不变。
- [ ] T126 [US3] 在 `internal/logic/chat/chat.go` 完成模型/观测失败域分离；质量门控：使 T116 GREEN，保留 ai_trace_id 与稳定业务错误类，禁止把 exporter failure 返回为 5xx。
- [ ] T127 [US3] 在 `internal/observability/smoke/alert_runner.go` 实现告警触发与恢复查询；质量门控：使 T117 GREEN，每类告警生成 firing/resolved 时间证据且查询窗口受限。
- [ ] T128 [US3] 在 `hack/observability/reset.sh` 实现显式确认、dry-run 预览和 label-scoped reset；质量门控：使 T118 GREEN，不使用全局 prune，不删除应用数据或无关 volume，输出不含 secret。
- [ ] T129 [US3] 在 `internal/observability/smoke/retention_runner.go` 实现 retention/volume 边界检查；质量门控：使 T119 GREEN，验证普通原文不进入任何可观测 retention unit，local raw 调试工件在运行结束时清理。
- [ ] T130 [US3] 在 `cmd/obs-smoke/main.go` 增加 exporter-failure、persistent-queue、score-worker-failure 与 full resilience scenarios；质量门控：每个 scenario 强制唯一 marker、cleanup trap、schema report 和稳定退出码。
- [ ] T131 [US3] 在 `Makefile` 增加 `obs-exporter-failure-smoke`、`obs-persistent-queue-smoke`、`obs-score-worker-failure-smoke`、`obs-resilience-e2e`、`obs-reset`；质量门控：破坏性目标要求 `CONFIRM_RESET=1`，中断后仍恢复 paused services。
- [ ] T132 [US3] 在 `deploy/observability/grafana/alerts/observability.rules.yaml` 校准实际 Collector/HTTP/storage 指标名与阈值；质量门控：通过 T117 实际 firing/resolved 验证，禁止仅凭静态语法宣布告警可用。
- [ ] T133 [US3] 在 `deploy/observability/grafana/dashboards/observability-overview.json` 增加 per-exporter queue age/capacity、storage 与 score worker failure 诊断视图；质量门控：面板查询与 T120 证据源映射一致，不混淆 push/pull/query failure。
- [ ] T134 [US3] 在 `specs/003-real-observability-backends/checklists/resilience.md` 编写 8+ 故障场景与告警验收矩阵；质量门控：每行包含注入方式、业务断言、证据源、恢复窗口、cleanup 和关联 SC-003/SC-004/SC-006，SC-006 只有在四类告警均取得 firing/resolved 报告后才能关闭。
- [ ] T135 [US3] 在 `docs/journal/0008-real-observability-recovery.md` 记录一次真实恢复实验；质量门控：必须基于实际 report，记录故障、根因、恢复、at-least-once 重复与后续预防，不写 credential 或用户原文。

## Phase 6：User Story 4 - 在两种基础设施方案间验证（P2）

**用户故事目标**：在 Grafana 主线完成后提供 SigNoz 备选 profile；它替换基础设施 logs/metrics/traces 后端，但仍保留 Langfuse AI trace/score 与相同应用契约。

**独立测试标准**：`make obs-signoz-e2e` 自动查询 SigNoz 三信号和 Langfuse AI trace/score，输出 schema-valid passed report；应用配置、HTTP 契约、AI marker 与 Grafana profile 相同。

### Tests - RED

- [ ] T136 [P] [US4] 在 `hack/observability/compose_signoz_test.sh` 编写 SigNoz profile 静态 RED 测试；质量门控：覆盖固定版本、healthcheck/resource limits/retention/loopback UI、Collector 边界、Langfuse 保留和 12GiB/8vCPU/20GiB 预算。
- [ ] T137 [P] [US4] 在 `hack/observability/collector_signoz_config_test.sh` 编写 SigNoz Collector RED 测试；质量门控：断言应用 OTLP 契约不变、infra 三信号路由到 SigNoz、AI marker 分支仍 OTLP/HTTP 到 Langfuse、无业务直连。
- [ ] T138 [P] [US4] 在 `internal/observability/backend/signoz_query_test.go` 编写 SigNoz 查询客户端 RED 测试；质量门控：httptest 覆盖 logs/metrics/traces、timeout/认证/畸形响应/旧 marker，错误不得回显 ingestion key。
- [ ] T139 [P] [US4] 在 `internal/observability/smoke/signoz_runner_test.go` 编写备选 E2E runner RED 测试；质量门控：使用 fake SigNoz/Langfuse query clients 离线覆盖 infra-only AI negative、chat 三信号、Langfuse trace/score、限定窗口和 schema report，RED 测试不得启动 Docker，真实 Make E2E 禁止以 compose healthy 代替查询。
- [ ] T140 [P] [US4] 在 `hack/observability/signoz_dashboard_test.go` 编写备选 dashboard/checklist RED 测试；质量门控：覆盖 request/error/latency、export failure、token/cost/eval correlation 的专门资产，不要求复刻 Grafana JSON。

### Implementation - GREEN/REFACTOR

- [ ] T141 [US4] 在 `deploy/observability/compose.signoz.yaml` 实现 SigNoz+Langfuse 备选 profile；质量门控：使 T136 GREEN，独立 volume/project labels、固定镜像、无默认 secret，不能修改应用 API 或 provider 配置。
- [ ] T142 [US4] 在 `deploy/observability/collector/collector-signoz.yaml` 实现 SigNoz 三信号与 Langfuse AI fan-out；质量门控：使 T137 GREEN，纯 infra 请求不进入 Langfuse，push exporter 失败证据仍可分出口归因。
- [ ] T143 [US4] 在 `internal/observability/backend/signoz_query.go` 实现只读查询客户端；质量门控：使 T138 GREEN，context timeout、response limit、稳定 error class 与 low-sensitive evidence。
- [ ] T144 [US4] 在 `internal/observability/smoke/signoz_runner.go` 实现备选 profile 查询闭环；质量门控：使 T139 GREEN，复用公共 report/poller/privacy，不复制或放宽 Grafana 主线的 identity/隐私断言。
- [ ] T145 [US4] 在 `deploy/observability/signoz/dashboard.json` 维护 SigNoz 专用 dashboard 资产；质量门控：使 T140 GREEN，面板回答与主线等价的运营问题，并保留 AI/score 到 Langfuse 的跳转说明。
- [ ] T146 [US4] 在 `specs/003-real-observability-backends/checklists/signoz.md` 编写独立兼容性清单；质量门控：逐项要求 SigNoz logs/metrics/traces 与 Langfuse trace/score 查询证据，明确优先级低于 Grafana 主线。
- [ ] T147 [US4] 在 `cmd/obs-smoke/main.go` 增加 `--profile=signoz` 调度；质量门控：profile 只改变 backend query/deploy adapter，不改变请求 payload、应用 endpoint、AI marker 或 report schema。
- [ ] T148 [US4] 在 `Makefile` 增加 `obs-signoz-up/down`、`obs-signoz-e2e`；质量门控：仅在 Grafana 主线验收后纳入支持声明，失败清理限定 SigNoz compose project。
- [ ] T149 [US4] 在 `docs/observability/10-grafana-vs-signoz-runbook.md` 记录两种 profile 的能力、成本、故障证据和选择边界；质量门控：结论引用实际 E2E report，不宣称功能完全等同，不记录 credential。

## Phase 7：User Story 5 - 快速保护平台接入契约（P2）

**用户故事目标**：强化已有 `obs-platform-smoke`，使其在无 Docker/凭据时 30 秒内验证最小双平面 payload、identity 与隐私边界，同时明确它不是后端验收。

**独立测试标准**：清空外部服务配置后运行 `make obs-platform-smoke`，外部连接数为 0、耗时 <=30 秒、敏感 canary 命中为 0，并输出 local profile 的 schema-valid report 或稳定测试证据。

### Tests - RED

- [ ] T150 [P] [US5] 在 `internal/eval/smoke/platform_config_test.go` 扩展显式启用 RED 测试；质量门控：覆盖默认 disabled、完整 local test config、缺字段 fail-fast、任何真实 endpoint/credential 默认不加载。
- [ ] T151 [P] [US5] 在 `internal/eval/smoke/platform_smoke_test.go` 扩展零外连与 identity RED 测试；质量门控：注入 counting transport，覆盖 infra payload 无 AI marker、AI payload 有 root/semantic marker、request/service/AI/eval identity 分离。
- [ ] T152 [P] [US5] 在 `internal/eval/smoke/platform_privacy_test.go` 编写平台 payload 隐私 RED 测试；质量门控：覆盖三种 payload policy、baggage allowlist、synthetic secret/PII、报告/错误输出零原文且 debug 不绕过 scanner。
- [ ] T153 [P] [US5] 在 `internal/eval/smoke/platform_report_test.go` 编写 local `platform_contract` smoke report RED 测试；质量门控：覆盖 local profile、30 秒 deadline、checks/cleanup/schema 与“未验证真实后端”的明确 skipped evidence。

### Implementation - GREEN/REFACTOR

- [ ] T154 [US5] 在 `internal/eval/smoke/platform_config.go` 收紧 local controlled-sender 配置；质量门控：使 T150 GREEN，默认不从生产 env 隐式启用，不保存或打印 credential。
- [ ] T155 [US5] 在 `internal/eval/smoke/platform_smoke.go` 增加双平面 marker/identity 和零外连断言；质量门控：使 T151 GREEN，复用生产 mapper/payload policy，禁止维护第二套语义。
- [ ] T156 [US5] 在 `internal/eval/smoke/platform_privacy.go` 实现 controlled payload canary 扫描；质量门控：使 T152 GREEN，敏感命中立即失败，输出只含类别/计数。
- [ ] T157 [US5] 在 `internal/eval/smoke/platform_report.go` 生成 local schema report；质量门控：使 T153 GREEN，真实 backend checks 必须标记 skipped 且说明范围，不得伪造 passed。
- [ ] T158 [US5] 在 `Makefile` 收紧 `obs-platform-smoke` 目标；质量门控：无 Docker/credential 可运行、30 秒内完成、强制 `-count=1` 防止缓存假阳性并保持默认零网络。
- [ ] T159 [US5] 在 `specs/003-real-observability-backends/quickstart.md` 更新 Level 0 平台 smoke 证据边界；质量门控：明确 controlled sender 保护的是 payload/identity/privacy contract，真实接收/查询只由 Grafana/SigNoz E2E 证明。

## Final Phase：Polish & Cross-Cutting Concerns

**目标**：统一契约、文档、门禁和真实验收证据，完成安全审查与可重复发布路径。

**独立验收**：Level 0 全离线门禁通过；显式配置环境后 Grafana Level 1-3 全部通过；SigNoz Level 4 独立通过；checklist、quickstart、ADR/journal 与机器报告互相可追溯且无敏感泄漏。

- [ ] T160 [P] 在 `specs/003-real-observability-backends/contracts/http-api.yaml` 根据最终 Go contract 做 OpenAPI 回归校准；质量门控：使用 OpenAPI parser 校验，所有示例无 secret/raw payload，禁止为迁就实现删除已决策的错误/identity/debug 约束。
- [ ] T161 [P] 在 `specs/003-real-observability-backends/contracts/telemetry-contract.md` 根据最终 instruments/component IDs 更新遥测契约；质量门控：逐项引用实现测试，保留高基数禁区、AI marker 边界和真实失败证据源。
- [ ] T162 [P] 在 `specs/003-real-observability-backends/contracts/runtime-configuration.md` 更新最终配置/端口/资源/retention 矩阵；质量门控：应用配置仍只包含 Collector endpoint，所有 secret 只记录 env 名与 present 状态。
- [ ] T163 [P] 在 `specs/003-real-observability-backends/contracts/smoke-report.schema.json` 校准最终 scenario/backend/error fields；质量门控：运行正反 fixture 校验，保持 schema version 兼容或显式升级，不允许任意嵌套 payload 泄漏。
- [ ] T164 在 `specs/003-real-observability-backends/quickstart.md` 完成环境准备、Level 0-4、故障注入、诊断、cleanup、credential rotation 与门禁频率 runbook；质量门控：明确 PR、观测配置变更、阶段里程碑、release candidate、scheduled canary 分别运行哪些命令及其外部依赖/费用；smoke 自建凭据必须撤销/删除、自建临时文件和数据必须清零，而外部注入的长期凭据不得被 smoke 撤销；每个命令必须真实存在，不能把未执行命令写成已通过。
- [ ] T165 在 `docs/adr/0008-real-observability-backends-and-minimal-http-loop.md` 添加实施结果、偏差与 score worker 演进阈值附录；质量门控：定义何时因多进程部署、持久化需求、shutdown 丢失窗口、queue age/吞吐或重试积压迁移到 outbox/外部 worker，只记录已验证事实与 revisit condition，架构变化需新 ADR 而非静默改写 accepted 决策。
- [ ] T166 在 `specs/003-real-observability-backends/checklists/real-backend-acceptance.md` 逐项关闭 CHK001-CHK041；质量门控：每个完成项引用具体 task/test/report，缺证据保持未勾选，不以代码存在替代实际 backend 查询；SmokeReport 必须含 marker、每个 backend check 的 failure_stage，以及 smoke 自建临时凭据/数据的 cleanup 证据。
- [ ] T167 在 `Makefile` 增加最终 `obs-status`、`obs-release-gate` 与 `obs-signoz-compat-gate`；质量门控：status 仅输出低敏健康/版本信息，release gate 顺序执行 verify/config/Grafana/resilience 并 fail-fast，SigNoz gate 独立且不成为默认 PR 付费/外部门禁。
- [ ] T168 在 `docs/ROADMAP.md` 与 `README.md` 更新 003 实施状态和验证入口；质量门控：区分 generated/planned/in-progress/verified，保留 002 学习资产历史归属，不提前宣告 SigNoz 或真实后端完成。
- [ ] T169 执行最终质量与安全审查并把低敏结果写入 `specs/003-real-observability-backends/checklists/final-verification.md`；质量门控：至少运行 `git diff --check`、`make verify`、`go test -race ./...`、覆盖率门禁、secret scan、`make obs-config-check`，有真实配置时再运行 Grafana/resilience/SigNoz gates，任何未运行项必须写明原因和剩余风险。

## Phase 3A：US1 本地日志出口去 bind-mount 化（P1，插入真实后端验收前）

> 本阶段以 append-only 方式取代早期 T053-T055/T058 中的 glog JSONL、shared-volume 与
> filelog 生产路径；这些完成项保留为历史实施记录，不再代表当前运行架构。

**用户故事目标**：本机运行的应用以 OTLP logs 将受控 completion 事实发送给 Collector，使 Loki 证据不依赖 Docker Desktop 对宿主机 JSONL bind mount 的可见性；JSONL 仅可作为本地诊断工件，不能再成为验收数据路径。

**独立验收标准**：应用、Collector、Loki 均已启动时，触发唯一 smoke marker 后，Loki 在同一 bounded window 内查询到该 marker；停止或删除宿主机 JSONL bind mount 不改变此结论；raw body、request/trace identity 不成为 Loki index label。

### Tests - RED

- [X] T170 [P] [US1] 在 `internal/observability/http_logging_test.go` 与 `internal/observability/logging_test.go` 编写 completion log → OTLP log record 映射 RED 契约；质量门控：仅允许现有低敏 allowlist、保留 `smoke_run_id` 作为 structured metadata、拒绝 raw payload/credential，发送失败不得改变 HTTP 响应。
- [X] T171 [P] [US1] 在 `deploy/observability/collector/collector-grafana.yaml`、`hack/observability/collector_grafana_config_test.sh` 与 `hack/observability/compose_grafana_test.sh` 编写 OTLP logs pipeline RED 配置断言；质量门控：logs 只由 OTLP receiver 接收，Loki exporter 保留 queue/retry/redaction，移除应用 JSONL bind mount/filelog 依赖，Collector self-telemetry 可由 Prometheus 查询。
- [X] T172 [P] [US1] 在 `internal/observability/backend/grafana_smoke_adapter_test.go` 与 `internal/observability/smoke/infra_runner_test.go` 增加真实 Tempo search response、可恢复查询失败重试、OTLP-log Loki marker 的 RED 契约；质量门控：Tempo/Loki 任一短暂查询错误只在 deadline 耗尽后失败，且不会将 raw response 写入 report。

### Implementation - GREEN/REFACTOR

- [X] T173 [US1] 在 `internal/observability/http_logging.go`、`internal/cmd/cmd.go` 与 `internal/logic/observability/infra_smoke.go` 实现受控 completion OTLP log emitter 的 composition-root 装配；质量门控：使 T170 GREEN，应用只连接 Collector，日志写入与 trace/metrics 使用同一 provider lifecycle，JSONL writer 改为显式本地诊断 opt-in 而非 smoke 必经路径。
- [X] T174 [US1] 在 `deploy/observability/collector/collector-grafana.yaml`、`deploy/observability/compose.grafana.yaml`、`deploy/observability/prometheus/prometheus.yaml` 实现 OTLP logs→Loki 和 Collector self-telemetry 可查询配置；质量门控：使 T171 GREEN，不发布额外公网端口、不删除 named volumes、不把 request/trace/run identity 升格为 Loki index label。
- [X] T175 [US1] 在 `internal/observability/backend/grafana_smoke_adapter.go`、`internal/observability/smoke/poller.go` 完成 Tempo/Loki transient-query retry 与真实响应解码；质量门控：使 T172 GREEN，任何最终失败仍产生 schema-valid、低敏 report，并准确区分 timeout、authentication、malformed response 与 marker missing。
- [ ] T176 [US1] 在 `cmd/obs-smoke/main.go`、`Makefile`、`deploy/observability/README.md` 与 `specs/003-real-observability-backends/checklists/real-backend-acceptance.md` 完成去 bind-mount 后的真实 infra smoke 验收与运行手册；质量门控：以新的 passed report 关闭对应未完成项，明确 `obs-infra-smoke` 只查询已运行服务，删除已失效的宿主机 JSONL 同步说明，未取得报告不得勾选验收项。

## 依赖关系

### Phase 依赖图

```text
Phase 1 Setup
    -> Phase 2 Foundational
        -> US1 Grafana infra-only
            -> US2 real chat + Langfuse
                -> US3 failure/recovery
                    -> Final release evidence
        -> US5 local platform smoke (可在 US1-US3 期间独立推进)

US1 + US2 + US3 验收完成
    -> US4 SigNoz compatibility
        -> Final alternate-profile evidence
```

### 用户故事依赖

- **US1（P1）**：依赖 Phase 1-2；自身构成首个真实后端 MVP，不依赖真实 LLM。
- **US2（P1）**：依赖 US1 的 Collector/Grafana/Langfuse 基础与 Phase 2；真实 LLM/score 只在显式 opt-in 环境运行。
- **US3（P1）**：依赖 US1/US2 的真实出口、score worker、dashboard/alert 资产；不能在正常路径尚未通过时做恢复验收。
- **US4（P2）**：依赖 Grafana 主线 US1-US3 已完成，避免备选 profile 阻塞首个闭环；不改变应用或 AI 契约。
- **US5（P2）**：只依赖 Phase 2 的 mapper/payload/report 公共能力，可与 US1-US3 大部分实现并行；它永远不能替代真实 E2E。

## 并行执行机会

- Phase 1 中版本矩阵、ignore/local example 和部署索引可并行，完成后再统一运行 config check。
- Phase 2 的 config、payload、propagation、metrics、logging、report、poller、scanner RED 测试位于不同文件，可并行编写；每个 GREEN 实现仍必须等待对应 RED 证据。
- US1 的 Go API/usecase/controller 测试可与 Collector/Compose/dashboard/alert 静态测试并行；真实 infra E2E 必须等两组都 GREEN。
- US2 的 chat usecase、OTel mapper、evidence store、score client/worker 和部署静态测试可分工并行；score runner 必须等待 evidence、worker、Langfuse ingestion 全部完成。
- US3 的 exporter、persistent queue、storage、score worker、alert 与 reset 测试可并行设计；故障注入执行必须串行或使用隔离 compose project，避免相互污染证据。
- US4 的 Compose、Collector、query client 与 dashboard RED 测试可并行；实现和 E2E 仍在 Grafana 主线验收后开始。
- US5 的 config、payload privacy 和 report tests 可与 US1-US3 并行，但必须复用最终生产 mapper，禁止复制实现。

## 实施策略

1. **MVP**：完成 Phase 1-2 + US1。此时已有真实 Grafana 经典栈三信号、纯基础设施 API、AI negative routing 和机器可读报告，可以独立验收 SC-001 的核心价值。
2. **AI 闭环**：完成 US2。接入真实 OpenAI-compatible chat、Langfuse AI trace/score 和本地 eval evidence，形成项目首个观测与评估驱动的真实业务闭环。
3. **生产韧性**：完成 US3。先证明正常投递，再逐个注入失败并验证恢复；不得跳过正常基线直接宣布 persistent queue 可用。
4. **备选后端**：Grafana 主线验收后完成 US4；SigNoz 只替换基础设施后端，Langfuse 与应用契约保持不变。
5. **持续快反馈**：US5 作为日常 PR 的轻量契约门禁；真实 E2E 在配置变更、阶段验收、release candidate 和计划 canary 中显式执行。
6. **每个任务的完成定义**：RED 证据 -> 最小 GREEN -> REFACTOR -> 目标测试/race/格式检查 -> 低敏证据或文档更新。任一步缺失，任务保持未完成。

## Phase 8：Convergence - T108 真实 AI smoke 前置闭环

**目标**：补齐 T105-T107 runner core 与 T108 CLI 之间遗漏的受控触发、真实 backend adapter、projection 查询事实源和装配契约。T177-T185 均为 T108/T109 的硬前置；在这些任务完成前，T108 必须保持未完成，禁止用 fake、默认 identity、`skipped` 或零计数伪装真实 E2E。

**独立验收**：离线 adapter/CLI 契约在 counting transport 下证明缺少显式 live opt-in 或任一 credential 时外部请求数为 0；显式配置真实 Grafana/Langfuse 环境后，chat marker 可从受控触发贯穿 Tempo/Loki/Langfuse，score 可从本地 evidence 定位唯一 projection，privacy 可在八个有界 surface 上执行精确 canary 查询；所有 adapter 只向 runner 返回低敏 DTO/计数。

### Tests - RED

- [X] T177 [P] [US2] 在 `specs/003-real-observability-backends/contracts/http-api.yaml`、`specs/003-real-observability-backends/contracts/runtime-configuration.md`、`specs/003-real-observability-backends/contracts/telemetry-contract.md`、`api/v1/chat/chat_test.go`、`internal/controller/chat/chat_test.go`、`internal/logic/chat/chat_test.go`、`internal/cmd/routes_chat_test.go`、`internal/observability/chat_boundary_test.go`、`internal/observability/generation_test.go`、`internal/observability/evaluator_test.go` 与 `internal/observability/http_logging_test.go` 定义受控 chat smoke marker、鉴权和真实 OTel identity 交接 RED 契约 per FR-004/FR-012/SC-002（missing）；质量门控：固定唯一设计为显式 `smoke.enabled` 时由 loopback CLI 携带独立短期共享 smoke auth header+runner-owned one-time marker，middleware 先恒时校验 auth 再原子消费 marker，disabled/remote/缺失/错误/replay 均拒绝且 secret 零输出；普通 Chat Meta 仍只公开 request_id/ai_trace_id，service trace/span 仅从活动 SpanContext 写入受控本地 run manifest 并由 runner读取，禁止经公共响应、metrics label、caller correlation identity 或 domain AI ID 猜测。
- [X] T178 [P] [US2] 在 `internal/observability/backend/grafana_chat_smoke_test.go` 与 `internal/observability/backend/langfuse_chat_smoke_test.go` 编写 `smoke.ChatSmokeBackend` 真实查询 adapter RED 契约 per FR-002/SC-002（missing）；质量门控：Tempo/Loki/Langfuse 仅按 runner-owned marker、完整 correlation identity 与 `[started_at, deadline]` 构造参数化查询，Prometheus 只查询低基数 LLM request counter；服务端 limit<=100、response body 有字节上限，raw query/body/endpoint/credential 不进入 DTO/error/report。
- [X] T179 [P] [US2] 在 `internal/eval/score_projection_store_test.go`、`internal/observability/langfuse/worker_test.go`、`internal/cmd/langfuse_score_lifecycle_test.go` 与 `internal/observability/backend/langfuse_score_smoke_test.go` 编写 score smoke 本地事实索引、状态写入和平台查询 RED 契约 per FR-015/SC-002（missing）；质量门控：evidence 持久化成功后保存 run/eval/projection/platform identity 初始快照，再由 worker 每次不可变 transition 更新同一 ProjectionID，支持重开恢复并按唯一 run ID 查询；Langfuse 查询按稳定 ProjectionID+trace/observation identity+120 秒窗口返回有界状态 DTO，缺失/重复/延迟结果 fail-closed，不从名称或时间猜测 identity。
- [ ] T180 [US2] 在 `internal/observability/backend/privacy_smoke_test.go` 与 `internal/observability/smoke/privacy_fixture_test.go` 编写 privacy 受控 fixture trigger、run manifest 与八 surface 查询 RED 契约 per FR-006/SC-005（missing）；质量门控：fixture 复用 T177/T182 的受保护 live chat 边界，把 synthetic canary 作为受控输入送入一次已证明成功的真实请求，并在内存中扫描 API response 原文、只持久化低敏扫描摘要、chat fixture 机器报告与同一 run/window/artifact manifest，禁止把 raw response 写入普通 JSON/磁盘；八个被扫描 surface 明确为 API response、application log、Collector queue/report、Tempo、Loki、Langfuse trace、Langfuse score、manifest 登记的既有 fixture report，全部对 canary 执行同一零命中/失败判定；当前尚未生成的 privacy report 不属于 backend report surface，继续由 runner自身 serialization guard 独立保护；查询必须 exact canary+run identity+窗口、每 surface 独立短超时和服务端 limit，返回仅非负计数或稳定 error class，禁止以“未发送任何请求”的全零结果通过。
- [ ] T181 [US2] 在 `cmd/obs-smoke/main_test.go` 编写 chat/score/privacy CLI composition RED 契约 per FR-011/FR-012/T108（partial）；质量门控：只接受 `--live` 显式 opt-in、固定 grafana profile 与 runner-owned identity；缺 opt-in 或任一 endpoint/credential/evidence/run-manifest reference 时在 runner/client/transport 前退出且请求数为 0；passed=0、failed/skipped/runtime=1、usage/config=2，报告先安全持久化，stdout 严格只含 scenario/status/可信 report path/允许的低敏 identity；本任务保持 RED，由 T108 在 T182-T185 后实现 GREEN。

### Implementation - GREEN/REFACTOR

- [ ] T182 [US2] 在 `api/v1/chat/chat.go`、`internal/controller/chat/chat.go`、`internal/logic/chat/chat.go`、`internal/cmd/routes_chat.go`、`internal/cmd/observability_runtime_config.go`、`internal/observability/chat_boundary.go`、`internal/observability/generation.go`、`internal/observability/evaluator.go`、`internal/observability/http_logging.go` 与 `internal/observability/smoke/run_manifest.go` 实现受控 live-smoke auth/marker 传播和真实 OTel identity manifest bridge per FR-004/FR-012/SC-002（missing）；质量门控：使 T177 GREEN，只有 loopback CLI+显式 smoke gate+独立短期共享 auth 可注入并原子消费一次性 marker，auth 不进入日志/span/report；marker 进入 root/bridge/generation/evaluator/受控 completion log metadata，普通 chat Meta/API 行为不变；活动 SpanContext 的 service trace/span 与 request/AI identity 只原子写入权限 0600、路径受限、一次性消费的本地 run manifest。
- [ ] T183 [US2] 在 `internal/observability/backend/grafana_chat_smoke.go` 与 `internal/observability/backend/langfuse_chat_smoke.go` 实现 `smoke.ChatSmokeBackend` per FR-002/SC-002（missing）；质量门控：使 T178 GREEN，复用现有受保护 HTTP transport、Grafana 查询边界和 Langfuse 最小权限 credential，四类查询均有 context deadline/结果上限/低敏错误映射，runner 与 CLI 不解析平台原始响应。
- [ ] T184 [US2] 在 `internal/eval/score_projection_store.go`、`internal/observability/langfuse/projection.go`、`internal/observability/langfuse/worker.go`、`internal/cmd/langfuse_score_lifecycle.go`、`internal/cmd/chat_runtime.go` 与 `internal/observability/backend/langfuse_score_smoke.go` 实现可恢复的 projection 状态写入、按 run lookup 和 `smoke.ScoreSmokeBackend` per FR-015/SC-002（missing）；质量门控：使 T179 GREEN，evidence 成功后写初始 projection snapshot，再 enqueue 并由 worker transition recorder 原子更新同一记录；应用重开可恢复，按 run ID 恰好返回一个不可变快照，真实 Langfuse score 查询保留稳定 ID/attempt/status，外部失败不修改或删除本地 evidence。
- [ ] T185 [US2] 在 `internal/observability/smoke/privacy_fixture.go` 与 `internal/observability/backend/privacy_smoke.go` 实现受控 canary fixture、manifest 绑定和 `smoke.PrivacySmokeBackend`，并完成 T108 前置接口证明 per FR-006/FR-011/SC-005（missing）；质量门控：使 T180 GREEN，fixture 必须先取得受保护 chat 成功证据，在有界内存中扫描 API response 后仅持久化低敏摘要、chat fixture report 与 manifest 才允许后续扫描，raw response 不落盘；report surface 只读取 manifest 登记的同一次既有 fixture report，当前 privacy report 继续由 runner serialization guard 保护；八 surface adapter 复用统一 scanner/受保护 query clients/contained artifact reader，网络解码前限制 body、每 surface 有短预算且全部 fail-closed；接口满足性测试证明 chat/score/privacy constructors 可供 T108 注入，缺 credential 时任何真实发送为 0，本任务不得提前实现或宣称 T181 GREEN。

### Dependency Gate

- T170-T175（真实 OTLP logs/Loki 链路）是本 Phase 的共同硬前置；T176 最终真实 infra 验收可与本 Phase 汇合，但 T108 前至少 T170-T175 必须 GREEN。
- T177 -> T182 -> T183；T178 -> T183。
- T179 -> T184。
- T177/T182 -> T180 -> T185；privacy fixture 必须复用同一受保护 trigger/run manifest，不能另造无事实 marker。
- T181 保持 CLI RED，不依赖 T185 变 GREEN。
- **T170-T175、T182、T183、T184、T185 全部完成且 T181 的 CLI RED 已被实际运行确认后，才允许继续 T108 并使 T181 GREEN；T108 完成后才允许 T109。**
