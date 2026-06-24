# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## 项目定位

本项目是一个**学习 + 实践载体**：用 **Go 语言**从零逐步打造一个**具有生产价值的 AI Agent 框架**。
作者目标是借此从「基础 AI Agent 开发工程师」成长为「资深 AI Agent 工程师」，并直接对标目标岗位。

唯一的现成核心文档是 `准备清单.md`——它是基于目标岗位 JD 解析出的、覆盖面广的面试指南，
**同时也是本框架的需求规格与技能路线图**。任何模块的设计深度都应以 `准备清单.md` 对应章节为标尺。

> 全局工程规范（数据库、安全、可观测性、AI 集成、OAuth、编码风格等）已在
> `~/.Codex/rules/**` 中定义并自动加载，本文件**不重复**这些内容，仅补充本项目专属约定。

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

## 学习型代码注释约定

本项目是边学习边构建的工程载体。后续实现任何代码时，注释需要服务于学习与复盘，而不仅是最低限度的生产注释。

- 对核心接口、契约测试、错误分类、并发控制、降级策略、trace/eval 字段、provider adapter 映射等关键代码，必须补充足够的中文注释，说明“为什么这样设计”和“生产环境要防什么问题”。
- 对非显而易见的实现步骤，应在代码块前写短注释，帮助学习者按路径阅读；特别是涉及 AI Agent、RAG、LLM provider、流式输出、tool calling、评估指标和可观测性时。
- 注释不应复述显而易见的语法或变量赋值；避免把噪声当解释。好的注释应解释意图、边界、取舍、失败模式或与 `准备清单.md`/ROADMAP 的对应关系。
- 测试代码也要有学习型注释：说明该测试覆盖的生产风险、契约语义或后续真实 adapter 需要保持的行为。
- 当为了教学添加注释时，优先保持代码本身简洁；如果需要长篇解释，应放到 `docs/`、ADR 或 journal，而不是塞进函数内部。

## 技术栈

- **语言**：Go 1.24+。选 Go 是为了借高并发 goroutine、低延迟服务、SSE 流式能力体现 §2.3「Go 加分项」与 §14.2 分布式/低延迟经验。
- **Module path**：`github.com/jazzash/ashjazz-aiagent`
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
当前 spec-kit 规格：`specs/001-agent-framework-spec/spec.md`
当前 spec-kit 实施计划：`specs/001-agent-framework-spec/plan.md`
当前 spec-kit 任务拆解：`specs/001-agent-framework-spec/tasks.md`
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
