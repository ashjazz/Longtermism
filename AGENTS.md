# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## 项目定位

本项目是一个**学习 + 实践载体**：用 **Go 语言**从零逐步打造一个**具有生产价值的 AI Agent 框架**。
作者目标是借此从「基础 AI Agent 开发工程师」成长为「资深 AI Agent 工程师」，并直接对标目标岗位。

项目北极星：

> **Longtermism 是一个观测与评估驱动的生产级 Go AI Agent Harness。它相信模型能力会持续变强，模型、工具和 Agent 范式会持续变化，但可观测事实、可评估证据和可回归改进，才是生产级 AI Agent 长期不变的底层基础。因此框架的核心价值不是替模型伪造智能，而是为模型、提示词、工具、RAG、上下文、loop 策略和业务结果建立可观测、可评估、可回归、可迁移的工程闭环。**

这一定义是后续架构、任务拆分、文档和代码取舍的最高约束。模型负责智能，Harness 负责约束、观测、评估和持续改进。
如果某个功能无法进入观测事实、评估证据或回归闭环，就必须重新审视它是否属于当前阶段的核心建设内容。
`Longtermism` 不代表慢，而代表从第一性原理出发，优先投资那些在模型、工具协议和 Agent 指导思想不断变化时仍然长期有效的工程基础。

唯一的现成核心文档是 `准备清单.md`——它是基于目标岗位 JD 解析出的、覆盖面广的面试指南，
**同时也是本框架的需求规格与技能路线图**。任何模块的设计深度都应以 `准备清单.md` 对应章节为标尺。

> 全局工程规范（数据库、安全、可观测性、AI 集成、OAuth、编码风格等）已在
> `~/.Codex/rules/**` 中定义并自动加载，本文件**不重复**这些内容，仅补充本项目专属约定。

## 不可妥协的设计原则

### 1. 观测事实是一等公民

所有核心行为都必须能留下可解释事实：模型调用、提示词版本、工具调用、RAG 检索、上下文压缩、loop 决策、降级、失败、评估结果。
Observability 不是日志增强，也不是平台 adapter，而是本框架领域模型的一部分。

### 2. 评估证据驱动演进

任何 Agent 能力的改进都不能只靠主观感觉证明。模型切换、prompt A/B、工具数量变化、RAG 策略调整、上下文压缩策略调整，
都应最终落到 dataset、experiment、score、evidence、regression gate 上。没有 evidence 的优化，只能算猜测。

### 3. 双平面观测，职责分离，身份关联

基础设施观测平面负责服务事实：HTTP、DB、cache、latency、error、export failure、trace/span。
AI 语义观测平面负责智能事实：LLM、prompt、retrieval、tool、agent step、eval evidence、cost、token、quality。
两者通过明确的 correlation identity 关联，而不是混成一个大而全的 trace 对象。

### 4. 平台可插拔，事实模型不可外包

Langfuse、Phoenix、LangSmith、Braintrust、OpenTelemetry Collector 等都可以接入，也可以替换。
但本框架自己的核心事实模型不能被任何外部平台 schema 绑架。平台是出口，不是领域事实源。

### 5. 显式语义优先于便利默认

缺失的 observation type、dataset identity、prompt identity、model identity、tool invocation identity、eval run identity，不应该被 adapter 猜出来。
可以默认 no-op，可以默认不外连，可以默认安全降级；但不能默认业务事实。

### 6. 隐私边界默认收紧

Trace 和 eval 很容易变成敏感数据泄露管道。普通观测默认只允许低敏摘要、hash、长度、分类、状态、分数、错误类别。
raw query、完整 prompt、tool args、外部响应、密钥、身份信息必须通过明确的安全边界和 opt-in 策略处理。

### 7. 学习价值与生产价值同时成立

当前阶段，本项目仍然是作者边做边学、沉淀 AI Agent 工程能力的载体。每个关键设计都要能回答：
为什么这样设计？生产环境会怎么坏？怎么观测？怎么评估？怎么回归？怎么迁移到另一个业务领域？

这条原则是现阶段的内部开发原则。未来进入开源与生产阶段时，对外表达可收敛为：
**设计决策必须可解释、可追溯、可复盘**。

## 核心哲学（贯穿所有决策）

来自 `准备清单.md` 的岗位信号：**「30% 模型化 + 70% 工程化」**——要的是能 delivery 的人，不是读论文的人。

落地到本框架，意味着每个模块都要回答的不是"我会调 API"，而是：

1. **它在生产环境如何失效？**（`准备清单.md` §3.4 RAG 故障模式、§4.4 Agent 陷阱）
2. **怎么证明它有效？**（§6 评估体系——每个能力都必须可评估、可回归）
3. **延迟 / 成本 / 可靠性的 trade-off 在哪？**（§9–§13）
4. **降级路径是什么？**（§10 降级容错 + 断路器）

因此本项目的代码风格基调：**显式优于隐式，可控优于便利，可观测优于"跑得起来"**。
`准备清单.md` §7 已将"编排框架对比"重定位为 **Agent Harness 与框架取舍**：本项目默认自建 lightweight harness，
框架只作为可借鉴组件或 app-layer adapter。核心目标是让每一层都可直接 trace、可直接替换、可直接测试。

## 语义优先约束（防止测试兼容污染设计）

本项目的测试是用来保护领域语义的，不是让实现为了“测试通过”而伪造默认行为。后续修改代码或测试时必须遵守：

- **不得为了兼容既有测试而引入不真实的领域默认值**。例如 AI 语义观测里的 `ObservationType`、失败状态、provider capability、工具权限、token budget、trace/eval 关联身份，都必须由调用链显式给出或由明确的边界层派生；adapter 不能擅自猜测。
- **发现旧测试缺少必要语义时，优先修正测试数据和契约描述**，而不是在实现里增加“看似无害”的兜底逻辑。兜底逻辑一旦进入核心包，会训练后续 adapter 和业务代码依赖错误语义。
- **默认值只允许用于工程安全，不允许改写业务事实**。可以默认 no-op sink、空 exporter、超时保护或本地 fallback；不可以默认某次 AI 观测“就是 agent/generation”，不可以把未知结果默认为 success，也不可以把缺失关联 ID 自动编造成有效链路。
- **每次新增默认、兼容或降级逻辑时，必须回答三件事**：这个默认值是否代表真实事实？如果事实缺失，是否应该 fail fast 或丢弃记录？测试是否覆盖了“缺失时不能被猜测”的路径？
- **契约测试失败时先审查契约本身**。如果失败暴露的是测试 fixture 语义不完整，应补齐 fixture；如果失败暴露的是实现偏离契约，才修改实现。严禁用隐藏的 adapter 补丁同时掩盖测试缺口和设计缺口。

## 领域建模优先约束（早期框架演进）

本项目仍处于框架早期建设阶段，旧接口、旧测试和早期实现不天然代表正确领域模型。后续扩展功能时，必须优先判断新增字段、配置或行为是否暴露了新的领域概念，而不是默认把它们继续塞进旧结构。

- **早期兼容不优先于正确建模**。如果旧测试表达的是不完整语义，应调整测试和契约；不要为了保持旧测试不变而让实现迁就错误模型。
- **KISS 不等于保留原始模型**。真正简单的设计应减少非法状态、明确职责边界，并让观测、评估、平台接入和回归分析更容易推理。
- **出现独立职责和不变量时，应优先建模为值对象或领域对象**。例如 dataset identity、prompt identity、model identity、provider outcome、trace identity、tool invocation 等，一旦被多个模块共同依赖，就不应只作为零散字符串字段扩散。
- **新增抽象必须服务单一职责原则**。判断是否抽象时，先问：它是否有独立生命周期？是否和其它字段形成不可分割的身份？放进旧对象是否会制造非法字段组合？是否会让后续实现更清晰？
- **事实身份字段不能靠部分字段表达完整语义**。例如评估数据集不能只靠 `version` 区分；如果 `name + version` 才构成身份，就应该显式建模为同一个概念，而不是让调用方在多个字段之间自行拼装。

## 学习型代码注释约定

本项目是边学习边构建的工程载体。后续实现任何代码时，注释需要服务于学习与复盘，而不仅是最低限度的生产注释。

- 对核心接口、契约测试、错误分类、并发控制、降级策略、trace/eval 字段、provider adapter 映射等关键代码，必须补充足够的中文注释，说明“为什么这样设计”和“生产环境要防什么问题”。
- 对非显而易见的实现步骤，应在代码块前写短注释，帮助学习者按路径阅读；特别是涉及 AI Agent、RAG、LLM provider、流式输出、tool calling、评估指标和可观测性时。
- 注释不应复述显而易见的语法或变量赋值；避免把噪声当解释。好的注释应解释意图、边界、取舍、失败模式或与 `准备清单.md`/ROADMAP 的对应关系。
- 测试代码也要有学习型注释：说明该测试覆盖的生产风险、契约语义或后续真实 adapter 需要保持的行为。
- 当为了教学添加注释时，优先保持代码本身简洁；如果需要长篇解释，应放到 `docs/`、ADR 或 journal，而不是塞进函数内部。

## 技术栈

- **语言**：Go 1.24+。选 Go 是为了借高并发 goroutine、低延迟服务、SSE 流式能力体现 §2.3「Go 加分项」与 §14.2 分布式/低延迟经验。
- **Module path**：`github.com/ashjazz/Longtermism`
- **Web 框架**：**GoFrame v2**（`github.com/gogf/gf/v2`，当前 v2.10.x）。CLI 工具 `gf`（代码生成 `gf gen ctrl/dao/service`）。约定见 https://goframe.org 。项目刻意把 **AI 内核放在 `pkg/ai/`，与 GoFrame 解耦**；`internal/` 与 `api/` 才是 GoFrame 应用层，仅做装配与 HTTP 暴露。这样内核可独立测试、未来可抽成独立库，也呼应 §7「自建 Agent Harness，框架作为适配层」的克制。
- **数据库**：遵循全局 `rules/database/**`——PostgreSQL + UUID 主键、无 DB 层外键、软删除、ORM 优先（GoFrame `gdb` + `gf gen dao`）。
- **缓存/会话**：Redis（限流计数、语义缓存、会话状态）。
- **配置**：GoFrame 配置（`manifest/config/config.yaml`），生产用环境变量覆盖；启动时校验必需密钥（§10）。
- **可观测性**：GoFrame `glog` + `pkg/ai/obs` 结构化 Trace；§8.3 定义的 trace 字段是每个 LLM 调用的强制上下文。

## 模块蓝图（与 `准备清单.md` 章节一一对应）

分层原则：**AI 内核在 `pkg/ai/`（与 GoFrame 解耦、可独立测试），GoFrame 应用层在 `internal/`+`api/`（仅做装配与 HTTP 暴露）**。新增模块前先确认它对应指南的哪一章，避免无目的造轮子：

```
pkg/ai/                   AI Agent 框架内核（不依赖 GoFrame）
  llm/        §2          模型抽象层：多 provider、流式、token 计数、超时/重试
  prompt/    §9.1         Prompt as Code：模板、版本、hash 追踪、A/B
  rag/        §3          检索增强：chunking、embedding、混合检索、RRF、re-rank、HyDE
  vectordb/   §5          向量库抽象：HNSW/IVF 选型、metadata 过滤
  agent/      §4/§7       Agent Harness：原生 tool calling loop、工具契约、步数上限、循环检测、token 预算
  cache/      §12         exact/stale fallback cache + 语义缓存
  resilience/ §10         断路器、多 provider failover、降级层级
  ratelimit/  §11         token bucket 多维限流、模型路由、成本控制
  obs/        §8          可观测性：trace 结构、TTFT/幻觉率/成本指标
  eval/       §6★         评估体系：golden dataset、RAGAS 指标、LLM-as-Judge、CI 门禁
api/v1/                   对外接口契约（GoFrame api，Req/Res 定义）
internal/
  cmd/                    命令入口（HTTP server 装配、路由注册）
  controller/             薄控制器（转发 logic，不写业务逻辑）
  logic/                  业务逻辑实现（消费 pkg/ai）
  service/                服务接口（gf gen service 生成）
  dao/  model/            数据访问与实体（gf gen dao 生成）
  consts/                 常量、错误码、枚举
manifest/config/          应用配置（config.yaml）
hack/config.yaml          gf CLI 代码生成配置
docs/ adr/ journal/       设计文档、架构决策、开发日志
```

### HTTP 传输边界（GoFrame）

`api/v1/` 只声明 GoFrame 路由元数据与公开 Req/Res DTO：字段、类型、JSON tag、`v` 校验 tag、`g.Meta` 和稳定的公开枚举。`v` tag 是契约元数据，由 controller 显式调用 `gvalid` 执行；它不得包含 HTTP body 解码、输入校验实现、JSON 自定义序列化、HTTP 状态码分支、错误 envelope 构造、provider/LLM 映射或安全策略。每个 `api/v1/<feature>` 都必须有 DTO-only 架构测试，禁止行为函数和除 GoFrame DTO 元数据外的 JSON/HTTP/validation/provider 依赖回流。

`internal/controller/` 是唯一的 HTTP adapter：负责严格 JSON 解码（包括未知字段、尾随 JSON、UTF-8 和 body 限制）、请求边界校验、`X-Request-ID`、稳定 HTTP 错误、以及 DTO 与应用 command/result 的映射。controller 可以依赖 `api/v1/` 和本地定义的窄 usecase 接口，但不得直接调用 `llm.Provider`、平台 backend、DAO/存储或密钥配置。

`internal/logic/` 只处理应用/领域事实：它可以依赖 `pkg/ai/` 来编排模型、评估、evidence 和观测，但**绝不能导入 `api/v1/`、HTTP 状态码、JSON decoder 或 GoFrame HTTP 类型**。LLM provider 的原始响应先在 `pkg/ai/llm` adapter 归一化；logic 再生成领域 `ChatResult`；controller 最后投影为公开 DTO。每个 logic 包必须有架构测试守护这一方向，避免 DTO 便利性反向污染领域语义。

身份和错误时机同样属于分层契约：transport 创建或传递 `request_id`，logic 在首次模型调用前创建 `ai_trace_id`，controller 只能投影既有 identity。错误响应只输出稳定分类和低敏元数据，禁止回显请求、prompt、provider body、endpoint 或凭据。

> `§6 评估体系` 是指南点名的"面试最大考点"。`pkg/ai/eval/` 不应是事后补丁——
> **每交付一个能力，必须同时交付评估它的一组 case**（见下方"完成定义"）。

## Agent Harness 取舍

- 默认路线：自建 lightweight harness，而不是默认采用 LangGraph / CrewAI / AutoGen / LangChain 等第三方编排框架。
- Harness 必备控制面：`ProviderCapabilities`、native tool calling executor、tool schema/permission/validation、step trace/replay、trajectory eval、max steps/timeout/token budget/loop detection、fallback/cache/failover/rate limit。
- 第三方框架只在确实减少复杂度时引入，例如 durable checkpoint、human-in-the-loop 状态图、平台 trace UI、连接器；引入形态必须是 adapter 或 app-layer integration，不得成为 `pkg/ai` 核心依赖。
- 任何框架引入前必须写 ADR，证明它不会隐藏 prompt、token、tool 参数、cost、trace 和降级行为。

## 完成定义（Definition of Done）——本项目最重要的约定

一个模块"做完了"必须同时满足（缺一项视为未完成）：

1. **有失败用例先行**：遵循全局 `rules/common/testing.md` 的 TDD（RED→GREEN→REFACTOR），覆盖率 ≥ 80%。
2. **有评估 / 回归手段**：对应 `internal/eval/` 的 golden case 或在线指标（§6）。AI 能力不允许"感觉变好了"。
3. **有故障与降级设计**：列出该模块的生产失效模式 + 降级路径（§3.4 / §4.4 / §10）。
4. **有可观测性**：关键路径打 trace（§8.3 字段：trace_id、prompt_template_version、token 用量、latency、cost）。
5. **有文档**：在 `docs/` 下记录选型理由与 trade-off（决策需可追溯，对应 §3.5 架构决策清单）。

涉及密钥、用户输入、外部调用时，强制走全局 `rules/common/security.md` + `security-reviewer` agent。

## 开发流程约定

- **调研优先**（全局 `rules/common/development-workflow.md` 第 0 步）：写新模块前先 `gh search code` / Context7 查已有实现与库用法，能移植/适配就不从零造。
- **架构决策落 ADR**：重大选型（向量库、编排方式、限流策略）写入 `docs/adr/`，复用 architecture-decision-records skill。
- **推进顺序参见 `docs/ROADMAP.md`**：分阶段、带依赖关系，先立 §2 LLM 抽象层与 §6 评估骨架，其余能力才能"被证明有效"地增长。

<!-- SPECKIT START -->
当前 spec-kit 规格：`specs/003-real-observability-backends/spec.md`
当前 spec-kit 实施计划：`specs/003-real-observability-backends/plan.md`
当前 spec-kit 任务拆解与状态源：`specs/003-real-observability-backends/tasks.md`
<!-- SPECKIT END -->

## 常用命令

```bash
go mod tidy                          # 同步依赖
go run .                             # 启动 HTTP server（默认 :8000）
go build ./...                       # 全量编译
go vet ./...                         # 静态检查
go test ./...                        # 全量测试
go test ./pkg/ai/llm/ -run TestX -v  # 单个测试
gf gen ctrl                          # 按 api/ 生成 controller（改接口后必跑）
gf gen dao                           # 按数据库表生成 dao/entity/do
gf gen service                       # 按 logic/ 生成 service 接口
```

## 文档与学习记录

- `准备清单.md`：规格与技能地图（只读参考，不在此改动）。
- `docs/ROADMAP.md`：推进顺序指导（分阶段 + 依赖关系）。
- `docs/adr/`：架构决策记录。
- `docs/journal/`：开发日志——记录"出过事 → 修好了 → 学到了"的真实案例（对应 JD 灵魂句，也是面试故事库 §15 的素材源）。

## 仓库现状

- GoFrame v2 应用骨架与 `pkg/ai` 内核模块骨架已就位（接口先行，实现待填充）。
- 基础设施（CI、Makefile、首批测试）尚未建立——落地它们属于 ROADMAP 第一阶段。
- 进展详见 `docs/ROADMAP.md`。
