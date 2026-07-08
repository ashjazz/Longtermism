# ADR-0007：双平面观测与评估体系 v1

**日期**：2026-07-03
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

项目已经完成现代化 Harness Agent 范式的基础抽象层，包括 provider、prompt、obs、tool、llm、基础 RAG、基础 Agent executor、resilience、ratelimit、fallback cache 和可替换 adapter 契约。下一阶段的核心目标不是继续堆叠单点能力，而是建立能解释“每一次用户请求到底发生了什么”的观测与评估体系，为后续 prompt 版本管理、RAG 策略对比、真实模型接入、loop 策略、上下文压缩和 MCP/tool 调用评估提供证据链。

ADR-0006 已经确定 `pkg/ai/obs.Tracer` 是 AI 内核唯一依赖的可观测边界，LangFuse、OpenTelemetry 和本地日志都只能作为 adapter。当前需要进一步决定：OpenTelemetry、GoFrame tracing、Langfuse 与 `obs.Trace` 在下一阶段如何分工，如何关联一次 HTTP 请求与 AI 语义事件，以及 Observability v1 的边界到哪里为止。

## Decision（决策）

Observability v1 采用“双平面观测与评估体系”：OpenTelemetry 作为基础设施观测平面和统一上下文传播标准，Langfuse 作为 AI 语义观测、错误分析与后续评估平台，`pkg/ai/obs.Trace` 继续作为框架内部稳定 AI 语义模型。GoFrame HTTP、数据库、外部调用等基础链路优先复用 GoFrame contrib trace 与 OpenTelemetry；LLM generation、RAG retrieval、tool/MCP 调用、Agent step、eval score 等 AI 语义事件由 `obs.Trace` 映射为 OTel spans/events/attributes，并通过 Langfuse 的 OpenTelemetry/OTLP 接入形成可分析的 observations。

下一阶段优先实现最小生产观测切片，而不是一次性建设完整 LLMOps 平台。v1 的验收目标是：同一次请求能够关联 HTTP request span、AI trace、LLM generation、retriever/tool/agent/evaluator 事件、失败/降级状态和评估分数；默认测试仍可离线运行，真实 Langfuse/OTel collector 只作为显式 opt-in smoke。

## Boundary（边界）

### OpenTelemetry 平面负责

- 作为跨服务上下文传播、trace/span、resource、exporter 和 collector 的标准层。
- 接入 GoFrame HTTP 服务、GoFrame 客户端、数据库或外部调用等基础设施链路。
- 生成和传播 `trace_id`、`span_id`、request 级上下文，并通过 `context.Context` 贯穿 handler、logic 和 `pkg/ai` 调用。
- 承载可被平台处理的低风险关联字段，例如 service name、environment、feature、tenant hash、session id、request id 和 outcome status。
- 通过 adapter 接收 AI 语义 span，但不取代 `obs.Trace` 的领域模型。

### Langfuse 平面负责

- 作为 AI 语义观测、trace inspection、session/user 过滤、score、prompt 版本关联、人工标注和错误分析平台。
- 接收 LLM generation、retriever、tool、agent、evaluator 等 observation 类型，保留模型名、token usage、latency、cost-ready 字段、prompt hash/version、outcome 和评估分数。
- 支撑后续 Langfuse dataset experiment、LLM-as-Judge calibration、user feedback score、annotation queue 和 CI/CD experiment gate。
- v1 优先通过 OpenTelemetry/OTLP 或最小 ingestion adapter 接入；不把 Langfuse SDK、Langfuse API 类型或社区 Go SDK 作为核心依赖。

### `pkg/ai/obs.Trace` 负责

- 保持框架内部稳定 AI 语义模型，继续描述 trace id、tenant/user/session、feature、prompt version/hash、model、token、latency、retrieval summary、cost-ready 字段、outcome、user rating 和 auto eval score。
- 作为 OTEL adapter、Langfuse adapter、本地日志和 eval report 的共同输入。
- 维持普通 trace 的隐私边界：不记录原始 query、完整 prompt、tool args、API key、token 或其它敏感原文。

### 关联层负责

- 定义并维护 `request_id`、OTel `trace_id/span_id`、`ai_trace_id`、`session_id`、`user_id`、`langfuse_trace_id/observation_id` 的映射关系。
- 保证一次 HTTP 请求下的多个 AI 子步骤可以被嵌套或关联，而不是形成无法串联的孤立记录。
- 明确哪些字段可以通过 OTel baggage 传播，哪些只能作为本地 trace 字段或 Langfuse metadata，哪些必须进入加密审计链路。

## V1 Scope（v1 范围）

v1 做以下事情：

- 为 GoFrame HTTP 服务建立 OpenTelemetry tracing 初始化、shutdown 和配置约定，优先复用 `github.com/gogf/gf/contrib/trace/*/v2`。
- 定义 `obs.Trace` 到 OTel span attributes/events 的字段映射规范。
- 定义 AI observation 命名和类型约定：LLM 调用映射为 `generation`，RAG 检索映射为 `retriever`，tool/MCP 调用映射为 `tool`，Agent orchestration 映射为 `agent`，评估写入映射为 `evaluator` 或 score。
- 建立同一请求内 HTTP span、AI trace 和 eval report 的关联规则。
- 补充 contract/privacy 测试，证明 adapter 保留关键字段、记录顺序、隐私边界和防御性拷贝语义。
- 提供 opt-in 的本地/开发 smoke，让真实 OTel collector 或 Langfuse endpoint 可验证接入，但不进入默认门禁。

v1 暂不做以下事情：

- 不迁移 prompt 模板到 Langfuse prompt registry；当前 prompt-as-code 仍以本地模板和 prompt hash/version 为准。
- 不把 Langfuse dataset experiment、annotation queue、user feedback、LLM-as-Judge 校准或 CI gate 作为 v1 必做项。
- 不保存原始 prompt、原始 query 或完整 tool arguments 到普通 trace；如需原文留存，另行设计加密审计链路。
- 不把 OpenTelemetry span/attribute 类型或 Langfuse SDK 类型暴露给 `pkg/ai` 核心接口。
- 不为了平台 UI 便利而修改 `obs.Trace` 成平台专属 schema。

## Field Mapping Principles（字段映射原则）

- **稳定身份字段**：`trace_id`、`request_id`、`session_id`、`user_id`、`tenant_id/hash` 必须可关联，但跨服务传播时优先使用低敏或 hash 后字段。
- **AI 诊断字段**：model、provider、prompt version/hash、input/output/reasoning/cache tokens、TTFT、total latency、cost-ready fields、finish reason 和 outcome status 必须保留。
- **RAG 字段**：chunks retrieved、top scores、retrieval latency、query rewritten hash 和 retrieval miss 状态必须可在 trace 中定位。
- **Agent/tool 字段**：step index、tool name、tool call id、tool status、loop detected、budget exceeded、max steps exceeded 等必须可追踪；普通 trace 只记录工具名、结构化状态和参数摘要/hash。
- **Eval 字段**：dataset name/version、sample id、metric name、score、threshold 和 regression status 应能与产生该输出的 trace 关联。
- **敏感字段**：原始 query、完整 prompt、tool args、用户隐私、密钥、认证 token、外部 API 响应原文默认禁止进入普通 OTel/Langfuse trace。

## Privacy Implementation Review（隐私实现回顾）

Phase 6 落地后，v1 隐私边界从“字段原则”推进为“可测试出口约束”。普通观测链路的敏感内容防护不依赖单个 adapter 自觉，而是由统一扫描 helper、logger 出口、OTel mapper 出口、baggage policy 和端到端 smoke 共同约束。这样做的目标不是构建完整 DLP 系统，而是在框架早期先守住普通 trace/log/span 不携带原文和密钥的底线。

本阶段确认以下取舍：

- **统一规则优先于分散判断**：敏感 key/value 的扫描逻辑集中在 `pkg/ai/obs`，logger、OTel mapper 和 baggage 复用同一组 value 检测规则，避免不同出口各自维护一套容易漂移的判断。
- **baggage 保持更窄边界**：baggage 不只是观测输出，还是跨进程传播面，因此继续采用 allowlist key 策略；即使 value 通过低敏检测，key 不在白名单内也不允许传播。
- **普通观测只保留摘要**：query、prompt、tool 参数和外部响应只能以 hash、长度、分类、数量、状态或错误分类等摘要进入普通观测面；如果未来需要原文用于审计、人工标注或 replay，应新增加密审计链路 ADR，而不是放宽当前普通 trace 边界。
- **泄露检测结果也要低敏**：privacy smoke 可以报告泄露发生在哪个 surface 和字段类别，但不能在失败结果中回显被扫描到的原始内容。否则“检测泄露”的工具本身会变成新的泄露出口。
- **隐私测试覆盖所有主要出口**：v1 smoke 覆盖 logger、span sink、OTel mapper、baggage 和端到端输出，确保双平面关联字段仍可回查，同时普通输出不会携带原始敏感 payload。

这次实现也暴露出一个长期边界：当前规则是框架级防线，适合阻止已知高风险字段和常见密钥/隐私形态进入普通观测；它不能替代业务侧数据分类、审计加密、权限控制和生产级 DLP。后续如果引入 Langfuse 真实平台、OTel collector 或外部日志平台，adapter 必须继续把这套隐私契约作为进入平台前的门禁。

## Alternatives Considered（备选方案）

### 方案 1：只接 OpenTelemetry，不接 Langfuse

- **优点**：标准化强，Go/GoFrame 生态接入自然，适合基础设施链路、日志关联、指标和 tracing。
- **缺点**：OTel 原生语义偏通用基础设施，不能直接提供 LLM trace UI、generation 类型、prompt 版本分析、score、dataset experiment、annotation queue 和错误分析工作流。
- **未采用原因**：本项目目标是生产级 AI Agent 框架，需要 AI 专属观测和评估闭环；只做 OTel 会让后续 RAG/Agent/prompt 评估仍然缺少产品化分析面。

### 方案 2：只接 Langfuse，不建立 OTel 基础设施平面

- **优点**：AI 观测能力集中，能快速看到 generations、scores、sessions 和 prompts。
- **缺点**：HTTP 请求、数据库、缓存、外部 API、GoFrame middleware、跨服务上下文和传统 SLO 难以用统一标准关联；Langfuse 不应承担全部基础设施 tracing 职责。
- **未采用原因**：一次用户请求不仅包含 LLM，还包含 HTTP、检索、缓存、限流、断路器和工具调用。没有 OTel 平面，无法形成真正的全链路诊断。

### 方案 3：直接采用 Langfuse Go/社区 SDK 作为核心实现

- **优点**：可能更快调用 Langfuse API，部分平台功能封装更直接。
- **缺点**：Go 生态下 Langfuse 官方主线更偏 OTel/跨语言接入，社区 SDK 会带来版本、维护和类型污染风险；默认门禁也更容易依赖外部服务。
- **未采用原因**：v1 优先走 OTel/OTLP 与 adapter 映射，保持核心无 SDK 绑定。若后续需要 Langfuse API 的 prompt、dataset 或 score 管理，可在 app-layer 或独立 adapter 中评估引入。

### 方案 4：自建完整观测和评估平台

- **优点**：可完全贴合本框架语义，隐私和数据模型可控。
- **缺点**：建设成本极高，会分散当前学习和框架能力建设重点；也无法复用 OTel/Langfuse 已有生态、UI 和评估工作流。
- **未采用原因**：本项目目标是构建高生产价值的 Agent 框架，而不是从零造 LLMOps 平台。自建只保留为本地日志、契约测试和必要 fallback。

## Consequences（影响）

### 正面影响

- HTTP 服务与 AI 内核可以在同一条链路上排障，避免“后端正常但 AI 不可解释”或“AI trace 有了但请求上下文丢了”。
- 后续 prompt 版本管理、RAG 策略 A/B、真实 provider 性能对比、Agent loop 策略、上下文压缩和 MCP/tool 调用都能围绕同一套 trace/eval 证据增长。
- 核心代码继续只依赖 `obs.Trace` 和 `obs.Tracer`，不被 OTel 或 Langfuse SDK 反向牵引。
- GoFrame 侧复用官方 contrib trace，减少自研基础设施 tracing 的成本和维护风险。
- Langfuse 侧保留未来接入 scores、datasets、experiments、annotation queues 和 user feedback 的演进路径。

### 负面影响

- 需要维护 `obs.Trace -> OTel attributes -> Langfuse observations` 的映射规范，短期设计成本高于单平台接入。
- 同时存在 OTel trace id、AI trace id 和 Langfuse observation id，需要明确关联规则，否则会产生重复或割裂记录。
- v1 不直接迁移 Langfuse prompt/dataset/CI 能力，短期看起来不如“一步到位”完整 LLMOps。

### 风险

- **风险**：OTel baggage 被滥用，跨服务传播敏感信息。
  **缓解**：只允许低敏关联字段进入 baggage；原始 query、prompt、tool args、token 和 PII 禁止传播。
- **风险**：为了 Langfuse UI 可读性而记录过多原文，突破 ADR-0006 的隐私边界。
  **缓解**：contract/privacy 测试继续禁止普通 trace 输出敏感字段；需要原文时另建加密审计 ADR。
- **风险**：AI 语义字段被拆散到多个平台，eval 无法回链到产生输出的请求。
  **缓解**：v1 必须先定义 request/trace/session/observation/eval 的关联层，再实现 adapter。
- **风险**：真实 collector 或 Langfuse 不可用影响主流程。
  **缓解**：生产 adapter 必须异步、可降级、可 flush；上报失败不得让用户请求失败，并应记录内部错误指标或 fallback 到本地 sink。

## References（参考）

- ADR-0006：可观测平台 Adapter 边界。
- OpenTelemetry Go：TracerProvider、resource、context propagation、span 和 OTLP exporter 是 Go 侧标准接入模型。
- GoFrame tracing：GoFrame contrib trace 提供 `otlphttp` 与 `otlpgrpc` 初始化、shutdown 和 tracing 接入方式。
- Langfuse docs：Langfuse SDK/observability 体系基于 OpenTelemetry，并支持通过 OTLP/OpenTelemetry 进行跨语言接入。
- Langfuse skill references：instrumentation、error-analysis、judge-calibration、user-feedback 和 CI/CD references 对 trace baseline、observation 类型、score、annotation 和 experiment gate 提供后续扩展约束。

## Revisit Conditions（重新审视条件）

满足以下任一条件时重新审视本决策：

- 首个 OTel/Langfuse adapter 落地后，发现 Langfuse OTLP 接入无法表达必要 observation 类型或 score 语义。
- 官方 Langfuse Go SDK 成熟并覆盖 tracing、prompts、datasets、scores 和 experiments，且引入不会污染核心契约。
- 项目进入真实多服务部署，需要统一 OTel collector、日志平台、metrics、Langfuse 和告警体系。
- 需要保存原始 prompt/query/tool args 做审计、回放或人工标注，必须新增加密审计链路 ADR。
- Observability v1 完成后，进入 v2 的 Langfuse dataset experiment、LLM-as-Judge calibration、user feedback score 或 CI/CD gate 建设。
- Observability v1 完成后，进入 v2 的 OpenTelemetry backend、collector 部署、trace/metrics/logs 可视化平台、告警和数据保留策略选型，并视需要新增独立 ADR。
