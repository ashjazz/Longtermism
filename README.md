# Longtermism

Longtermism 是一个**观测与评估驱动的生产级 Go AI Agent Harness**。

它相信模型能力会持续变强，模型、工具协议、MCP 服务、RAG 策略、上下文技术和 Agent 范式都会持续变化。但生产级 AI Agent 长期不变的工程基础，是可观测事实、可评估证据和可回归改进。

因此，本项目的核心价值不是替模型伪造智能，而是为模型、提示词、工具、RAG、上下文、loop 策略和业务结果建立可观测、可评估、可回归、可迁移的工程闭环。

> 说明：项目已经在定位、远程仓库和 Go module path 上对齐到 **Longtermism**。当前本地工作区目录名仍可能保留历史名称，这不影响模块身份。

## 项目状态

本仓库目前是一个主动演进中的学习型工程项目，还不是已经封版的开源框架发行版。

当前重点：

- 自建 lightweight Go AI Agent Harness，而不是把核心编排语义外包给第三方框架。
- 保持 `pkg/ai` 与 GoFrame 解耦，让 AI 内核可以独立测试、复用和演进。
- 建立双平面观测与评估体系，作为框架的差异化工程基础。
- 把 ADR、journal、学习资产、smoke test 和回归门禁作为一等项目产出。

当前活跃 spec-kit 路线：

- 规格：`specs/003-real-observability-backends/spec.md`
- 计划：`specs/003-real-observability-backends/plan.md`
- 任务：`specs/003-real-observability-backends/tasks.md`
- Quickstart：`specs/003-real-observability-backends/quickstart.md`
- ADR：`docs/adr/0008-real-observability-backends-and-minimal-http-loop.md`

## 003 实施与验证状态

截至 2026-08-25，003 仍处于收口阶段。这里使用四个互不替代的状态，避免把文件存在、测试通过和真实后端验收混成同一件事：

- `generated`：设计、代码、配置、测试或运维资产已经进入仓库，但不代表它已经在真实环境执行。
- `planned`：目标或验证入口已经定义，但尚未取得所需证据。
- `in-progress`：实现或验收正在推进，仍有任务、门禁或 evidence blocker 未关闭。
- `verified`：只有与声明范围匹配的可复验证据，才能在该范围内使用此状态；它不会从 `generated` 自动晋级。

| 范围 | 当前状态 | 证据边界 |
| --- | --- | --- |
| 003 整体 | `in-progress` | T160-T168 已收口；任务状态源仍保留 T035 与 T169，最终质量/安全审查尚未执行。 |
| 应用观测、Collector、Grafana/SigNoz profile、smoke runner、dashboard、门禁入口 | `generated` | 资产与离线契约已进入仓库；资产存在不等于真实平台支持声明。 |
| T160-T168 的局部契约 | `verified` | 仅表示对应 schema、配置、runbook、ADR、checklist、Make 聚合与项目状态文档契约的针对性测试已经通过；不替代 T169 的全量门禁。 |
| Grafana infra 历史运行 | `verified`（仅 schema v2 范围） | 2026-08-14 曾取得 infra passed report；该本地 ignored 报告不能关闭当前 schema v3 live acceptance。 |
| Grafana release/resilience | `planned` | 入口已生成，但尚无当前 schema v3 passed report，不能宣称主线 release 或恢复能力完成。 |
| SigNoz 兼容性 | `generated`（资产）/`planned`（live 验收） | Compose、查询 adapter 和独立门禁已存在；尚无当前 schema v3 passed report，不能宣称 SigNoz 已获得端到端支持。 |

`docs/observability/01-07` 是 `specs/002-dual-plane-observability` 的 T072-T078 学习资产，当前文档头中的状态仍为 `drafted`。003 复用并深化这些知识，但不改写为 003 产物；003 新增的真实后端决策与运行资产从 08 起单独追踪。

### 验证入口

| 目的 | 命令 | 当前边界 |
| --- | --- | --- |
| 默认离线检查 | `make verify` | 不启动 Docker；是否全绿由本次运行决定，最终审查记录由 T169 生成。 |
| 观测静态契约 | `make obs-config-check` | 校验版本、Compose、Collector 与配置保护，不证明真实查询成功。 |
| 低敏状态诊断 | `OBS_PROFILE=grafana make obs-status` | 输出 `diagnostic_only`；容器健康与版本信息不能作为 release evidence。 |
| Grafana release candidate | `make obs-release-gate` | 显式 opt-in，串行运行离线、覆盖率、配置、Grafana 和 resilience 门禁；需要 Docker/凭据，本地真实模型 API 可能计费。 |
| SigNoz 独立兼容门禁 | `make obs-signoz-compat-gate` | 显式 opt-in，独立于 Grafana 主线且不进入默认 PR 门禁；需要 Docker/凭据，真实模型 API 可能计费。 |

真实运行的完整环境、报告、cleanup 与 credential 规则以 `specs/003-real-observability-backends/quickstart.md` 为准。

## 为什么做这个项目

很多 Agent 框架关注“如何更容易组合 Agent”。Longtermism 更关注“如何让 Agent 在生产环境中更容易被理解、评估、回归和改进”。

本项目持续追问这些问题：

- 一次用户请求到底发生了什么？
- 哪个服务 span、AI observation、tool call、retrieval step、model generation 或 eval sample 解释了最终 outcome？
- prompt、模型、RAG 策略、工具集合、loop 策略或上下文压缩策略的变化，是否真的改善了质量、延迟、成本或可靠性？
- 线上失败能否沉淀为可重复执行的评估样例？
- 从编程领域迁移到金融、医疗等领域时，是否可以主要通过 dataset、tool、policy、rubric 和 risk taxonomy 调整，而不是重写框架核心？

## 核心原则

- **观测事实是一等公民**：trace、span、generation、retrieval、tool call、agent step、failure、degradation 和 eval evidence 都是领域事实。
- **评估证据驱动演进**：模型、prompt、工具、RAG 和 loop 策略变化应进入 dataset、experiment、score、evidence 和 regression gate。
- **双平面观测**：基础设施事实和 AI 语义事实分属两个平面，通过明确的 correlation identity 关联。
- **平台可插拔**：Langfuse、Phoenix、LangSmith、Braintrust、OpenTelemetry Collector 等是出口，不是领域事实源。
- **显式语义优先**：缺失的 observation type、dataset identity、model identity 或 trace identity 不应由 adapter 猜测。
- **隐私默认收紧**：普通观测只携带安全摘要、hash、长度、分类、状态、分数和错误类别，不携带敏感原文。
- **学习价值与生产价值同时成立**：关键设计必须能解释为什么这样做、生产中会怎么坏、如何观测、如何评估、如何回归。

## 架构地图

```text
pkg/ai/                   Go AI Agent Harness 内核，不依赖 GoFrame
  llm/        模型 provider 抽象、流式、usage、capability
  prompt/     Prompt as Code、模板渲染、版本与 hash
  rag/        检索原语与 retriever observation
  vectordb/   向量库抽象
  agent/      tool calling loop、executor 契约、step observation
  cache/      exact/stale fallback cache 基础
  resilience/ failover、降级、失败分类
  ratelimit/  token bucket 与模型路由基础
  obs/        trace 事实、安全摘要、correlation、双平面关联
  eval/       dataset identity、metric、report、evidence、regression status

api/v1/                   GoFrame API 请求与响应契约
internal/                 应用装配、controller、smoke runner
  cmd/                    命令入口与观测配置边界
  controller/             薄 HTTP controller
  eval/                   本地 golden case 与离线 smoke 编排

docs/                     ADR、journal、observability 学习资产
specs/                    spec-kit 路线、计划、任务、契约、quickstart
resource/                 prompt、public resource、log
manifest/config/          GoFrame 应用配置
```

## 双平面观测与评估 v1

当前版本正在建设第一条具备生产形状的观测与评估切片。

### 基础设施观测平面

基础设施平面记录传统服务事实：

- HTTP/service span；
- service resource 与部署身份；
- exporter lifecycle；
- context propagation；
- latency、status、error class 和 export failure protection；
- 默认 no-op/local 行为，不依赖外部平台。

### AI 语义观测平面

AI 语义平面记录智能相关事实：

- LLM generation；
- retriever observation；
- tool invocation summary；
- agent step 和 termination state；
- token、cost、latency 和 failure classification；
- evaluation evidence 与 trace 回链。

### 关联层

两个平面通过稳定、低敏的身份字段关联：

- `request_id`
- `service_trace_id`
- `span_id`
- `ai_trace_id`
- `session_id`
- `eval_run_id`
- `sample_id`

这样可以从一次请求、一个 AI trace 或一个 eval sample 出发，回查完整事实链，而不依赖某个具体平台的 schema。

## 环境要求

当前 `go.mod` 声明：

```bash
go 1.25.0
```

建议使用兼容 Go 1.25 的工具链，并确保本地可运行：

```bash
go version
go test ./...
```

项目在应用层使用 GoFrame v2；`pkg/ai` 下的 AI Harness 内核刻意保持 GoFrame-independent。

## 快速开始

```bash
go mod download
make test
make vet
make eval-smoke
make obs-smoke
```

启动 HTTP server：

```bash
go run .
```

默认评估和观测 smoke 都是离线验证，不要求真实模型 API key、Langfuse 凭据、OpenTelemetry Collector endpoint、向量数据库或付费服务。

## 常用命令

```bash
make test        # go test ./...
make test-race   # go test -race ./...
make vet         # go vet ./...
make eval-smoke  # 本地 eval smoke
make obs-smoke   # 本地 observability smoke
```

常用聚焦测试：

```bash
go test ./pkg/ai/obs -count=1
go test ./pkg/ai/eval -count=1
go test ./internal/eval/smoke -count=1
go test ./internal/cmd -count=1
```

## 文档导航

- `AGENTS.md`：项目专属工程规范、设计原则和完成定义。
- `docs/ROADMAP.md`：项目北极星、阶段路线和长期推进顺序。
- `docs/adr/`：架构决策记录。
- `docs/journal/`：开发复盘、经验教训和学习记录。
- `docs/observability/`：观测与评估学习资产。
- `internal/eval/README.md`：本地 eval 与 smoke 边界。
- `specs/001-agent-framework-spec/`：已完成的框架地基路线。
- `specs/003-real-observability-backends/`：当前真实可观测后端与最小 HTTP 闭环路线。

## 完成定义

一个能力不能因为“能跑起来”就算完成。它还必须具备：

- 测试保护，优先遵循 TDD；
- 必要的评估或回归证据；
- 明确的故障模式与降级路径；
- 可观测 trace fact 或安全摘要；
- 对非平凡决策的文档、ADR、journal 或学习资产记录。

对于 AI 能力，“感觉变好了”不是证据。期望路径是：

```text
change -> trace facts -> eval evidence -> regression decision -> documented learning
```

## 隐私边界

普通观测记录不得包含：

- 原始用户 query；
- 完整 prompt；
- 完整 tool arguments；
- API key、JWT、password 或 token；
- 个人身份信息；
- 外部服务响应原文。

普通观测默认使用安全摘要：

- hash；
- length；
- category；
- count；
- score；
- status；
- stable error class。

如果未来确实需要保存原文用于审计或人工标注，必须设计独立的安全审计链路，不能混入普通 trace。

## 当前非目标

当前路线不试图一次性建设完整 hosted observability platform、prompt management platform、human labeling queue 或生产 SaaS console。

真实平台导出成功也不等价于框架契约完成。当前核心目标是：即使在默认离线环境中，也能确定性证明一次请求发生了什么、为什么失败或降级、关联了哪些 AI 语义事实，以及是否形成可回归的评估证据。

## License

当前尚未声明 license。
