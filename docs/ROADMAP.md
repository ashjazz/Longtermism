# 推进顺序指导（ROADMAP）

> 本文档把《准备清单.md》的 18 章技能地图，翻译成一套**架构合理、依赖清晰、可被评估驱动**的开发推进顺序。
> 目标不是"按章节顺序照抄"，而是按**依赖关系**与**评估闭环**组织——每一步都必须可运行、可测试、可证明有效。
>
> 配套阅读：`准备清单.md`（规格）、`AGENTS.md`（本项目完成定义与工程规范的 canonical source）、`docs/adr/`（每步的选型决策）。`CLAUDE.md` 如存在，仅视为历史/镜像入口，不能覆盖 `AGENTS.md`。

---

## 当前 spec-kit 入口

当前版本规划已标准化到 spec-kit 文档中。新会话或新执行者应优先从以下静态文件恢复上下文：

- 规格文档：`specs/001-agent-framework-spec/spec.md`
- 技术计划：`specs/001-agent-framework-spec/plan.md`
- 任务拆解：`specs/001-agent-framework-spec/tasks.md`
- 调研决策：`specs/001-agent-framework-spec/research.md`
- 数据模型：`specs/001-agent-framework-spec/data-model.md`
- 验证指南：`specs/001-agent-framework-spec/quickstart.md`
- 契约目录：`specs/001-agent-framework-spec/contracts/`

---

## 0. 推进的底层逻辑（先理解方法论，再看路线）

### 0.1 三条铁律

1. **评估先行（TDD + Eval-Driven）**
   `准备清单.md §6` 是 JD 对单一技能要求最高者。任何 AI 能力都先写 golden case（红灯），再写实现（绿灯）。
   没有评估的能力 = 盲飞，不允许进入"完成"。

2. **地基优先，能力按依赖生长**
   不要先做 RAG 或 Agent。它们都依赖 `llm`（模型抽象）与 `obs`（可观测）这两个地基。
   地基不稳，上层能力无法被信任、无法被 trace、无法被降级。

3. **每个能力都要回答四个生产问题**（AGENTS.md 完成定义）
   ①如何失效 ②怎么证明有效 ③延迟/成本/可靠性 trade-off ④降级路径。

### 0.2 为什么不照搬章节顺序

照章节顺序（§2→§3→§4…）会有两个陷阱：
- **评估骨架（§6）被推迟**：做完 RAG 才发现无法证明它有效，返工。
- **横切关注点（§8 可观测 / §10 容错）被当成事后补丁**：这正是 JD 批评的"只读过论文的人"。

因此本路线把 §6（eval）、§8（obs）、§10（resilience）的**骨架**提前到与第一个能力并行，把它们的**深化**放在后面。

### 0.3 依赖关系图

```
                    ┌─────────────────────────────────────────────┐
                    │                                             │
   llm (§2) ●───────┤  一切 AI 能力的地基：provider/流式/token/超时 │
        │           └─────────────────────────────────────────────┘
        │
        ├── prompt (§9.1)   模板/版本/hash —— 几乎所有能力都消费
        │
        ├── obs (§8)        trace 骨架 —— 每个能力都要打点
        │
        ├── eval (§6) ★     golden dataset 骨架 —— 每个能力都要交付 case
        │
        ▼
   ┌────────────┐   ┌────────────┐
   │ rag (§3)   │   │ agent (§4) │   两条主线，并行推进
   └─────┬──────┘   └─────┬──────┘
         │ 依赖            │ 依赖
         ▼                 ▼
   vectordb (§5)     tools + llm
   embedder
         │
         ▼
   cache (§12) ←── P1 exact/stale cache 接口，P4 语义缓存增强
        │
        ▼
   resilience(§10) + ratelimit(§11) ← 横切，包裹所有对外调用
        │
        ▼
   生产化（§9/§13）：API 网关、SSE 流式、异步队列、CI 门禁
```

---

## 1. 阶段总览

| 阶段 | 主题 | 对应准备清单章节 | 产出 | 完成判据 |
|------|------|-----------------|------|---------|
| **P0** | 地基与评估骨架 | §2 §6 §8 §9.1 | llm 抽象 + prompt + obs trace + eval runner 骨架 + 最小 CI | 能跑通「prompt→llm→trace→打分」最小闭环，并在 CI 中跑测试/golden case |
| **P1** | 生产化横切能力 | §10 §11 | 断路器、多 provider failover、exact/stale cache、多维限流、模型路由 | llm 调用具备重试/熔断/限流/降级 |
| **P2** | RAG 主线 | §3 §5 | chunking→embedding→混合检索→RRF→re-rank | eval 用 Recall@k/MRR/NDCG 量化，达标 |
| **P3** | Agent 主线 | §4 | 原生 tool calling executor + 工具系统 + 安全边界 | 任务完成率/工具调用/步数/token 被评估 |
| **P4** | 性能与成本 | §12 §13 | 语义缓存、SSE 流式、并行检索、延迟 SLO | 缓存命中率 + 延迟 P95 达标 |
| **P5** | 工程化收口 | §7 §9 | CI 评估门禁、ADR、可观测大盘、灾难演练 | 全链路可上线，有 post-mortem |

> P2 与 P3 可并行（一个走检索线、一个走推理线），但都依赖 P0/P1 完成。

---

## 2. 各阶段详述

### P0 — 地基与评估骨架（最高优先级）

> **理念**：先把"调用模型 + 知道它表现如何 + 能追踪它"三件事立起来。后面所有能力都站在这三者上。

**P0.1 `pkg/ai/llm`（§2）—— 模型抽象层**
- 任务入口：`specs/001-agent-framework-spec/tasks.md` Phase 4 / P0-A（T023-T034B，随后 T034 接入公共契约测试）。
- 实现 `Provider` 接口的首个适配器（建议 OpenAI 兼容协议起步，可同时对接 DeepSeek/本地 vLLM；但核心接口不能被 Chat Completions 风格绑死）。
- 必备：`Chat`、`ChatStream`、`ToolCall`/`ToolResult` 抽象、`Usage`（input/output/reasoning/cache tokens）、`TokenCounter`、`ctx` 超时（默认 60s）、对 429/5xx/timeout 返回 `ErrUpstream`。
- 定义 `ProviderCapabilities`：是否支持 tool calling、strict structured output、streaming tool call、reasoning effort、prompt caching、vision。P1 failover 和 P3 agent 都必须基于 capability 选择候选模型。
- TDD：用 httptest mock provider，覆盖正常/超时/429/4xx 不重试。
- ⚠️ 面试点：能讲清楚为什么 4xx 不重试、为什么流式要单独接口（§2.3）。

**P0.2 `pkg/ai/prompt`（§9.1）—— Prompt as Code**
- 任务入口：`specs/001-agent-framework-spec/tasks.md` Phase 4 / P0-B（T035-T039）。
- 文件系统模板 + `Render` 产出带 `Hash`/`Version` 的 `Rendered`。
- 模板放 `resource/prompt/<feature>/vN.tmpl`；首版使用 Go 标准库 `text/template` + `missingkey=error`，需要更强过滤/继承能力时再 ADR 评估 `pongo2` 等 Jinja-like 引擎。
- ⚠️ 关键：每次渲染的 `Hash` 必须进 trace（§8.3），这是后续 A/B 分析的锚点。

**P0.3 `pkg/ai/obs`（§8）—— Trace 骨架**
- 任务入口：`specs/001-agent-framework-spec/tasks.md` Phase 4 / P0-C（T040-T043）。
- 定义 `Trace` 结构（字段集严格对齐 §8.3：trace_id、tenant_id、model、prompt_template_version、token、ttft、cost…）。
- 隐私边界第一天就内置：普通 trace/log 只记录 query hash、脱敏摘要、长度、语言、分类标签；原始 query/prompt/tool 参数只能进入加密审计存储并设置 retention。
- 先实现一个日志型 `Tracer`（写 glog），后续接 OTEL/LangFuse 不改业务代码。
- ⚠️ 区分 AI 特有指标 vs 传统后端（§8.1）：TTFT、幻觉率、token/请求、单请求成本。

**P0.4 `pkg/ai/eval`（§6）—— 评估骨架（本阶段灵魂）**
- 任务入口：`specs/001-agent-framework-spec/tasks.md` Phase 4 / P0-D（T044-T050）。
- `Dataset`/`Sample`/`Metric`/`Runner`/`Report` 接口落地。
- 先建一个**最小 golden dataset（20-30 条）**，覆盖简单/中等/边界三类。
- 实现一个确定性 `Metric`（如格式/引用校验）作为 CI 可跑的基线。
- LLM-as-Judge（§6.4）此阶段先留接口 + 一个 prompt，bias 处理留到 P5 深化。
- ⚠️ 面试最大考点：能就 §6.5（golden dataset 构建五步法）讲 10 分钟。

**P0.5 最小 CI / 本地门禁**
- 任务入口：`specs/001-agent-framework-spec/tasks.md` Phase 4 / P0-E（T051-T055）。
- `Makefile` 或等价脚本：`test`、`test-race`、`lint/vet`、`eval-smoke`。
- GitHub Actions（或当前仓库选定 CI）先跑 `go test ./...` + 最小 golden dataset；P5 再升级为 baseline 对比和 PR comment。

**P0 验收**：一个端到端 demo——`prompt 模板渲染 → llm.Chat → obs 记录 trace → eval 给出分数`，全程有测试覆盖，golden case 跑在最小 CI。

---

### P1 — 生产化横切能力

> **理念**：在 llm 真正承担线上流量前，先把"它会挂、会被打爆、会很贵"三件事防护好。对应 JD「降级容错、限流、成本控制」。

**P1.1 `pkg/ai/resilience`（§10）**
- 断路器：`CircuitBreaker`（CLOSED/OPEN/HALF_OPEN），参数对齐 §10.2。
- 多 provider failover：`FailoverPolicy`（权重 + 延迟/错误率感知 + 健康检查摘除）。
- 降级层次（§10.1）：模型降级 → exact/stale cache 兜底 → 规则兜底 → 优雅降级。语义缓存命中作为 P4 增强，不阻塞 P1。
- 与 llm 集成：`Provider` 装饰器模式包一层断路器 + failover。
- TDD：注入故障 provider，验证熔断打开/恢复、failover 切换。
- ⚠️ 面试点：能讲「OpenAI 挂了怎么办」（§16.13）。

**P1.2 `pkg/ai/cache`：降级缓存接口（§10/§12 的最小切片）**
- 只定义 `FallbackCache` 接口和 Redis exact/stale 实现：按 prompt/model/hash 精确命中，用于上游全挂时返回明确标注的旧结果。
- 不做向量相似度检索，不做语义阈值调优；这些留给 P4。
- TDD：上游全挂时命中 stale cache；缓存 miss 时走规则兜底；返回结果必须带 `source=cache` 标记。

**P1.3 `pkg/ai/ratelimit`（§11）**
- `Limiter`：token bucket，多维度 key（global / user:<id> / provider:<p>）。
- `ModelRouter`：复杂度分级 → 小/中/大模型路由（§11.2），核心降本手段。
- Redis 后端持久化计数器。
- ⚠️ 面试点：能讲「怎么降低 LLM 成本」（§16.14）四件套。

**P1 验收**：llm 调用被 [限流 → 断路器 → failover] 包裹；超限返回 429，上游全挂走 exact/stale cache 或规则兜底。每个行为有测试。

---

### P2 — RAG 主线

> **理念**：检索增强是生产 RAG 的标准范式。按 §3.1 决策树选 chunking，按 §5.2 决策矩阵选向量库，全程用 §6 指标证明有效。

**P2.1 `pkg/ai/rag`：chunker（§3.1）**
- 先落地 Recursive Character Splitter（默认首选），参数 chunk_size=400-512、overlap=10-20%。
- 为代码/markdown 设计不同分隔符层级。
- ⚠️ 面试区分点：能讲清不同 chunking 策略如何影响召回，并用自有 golden set 做 A/B benchmark；不要脱离语料背固定提升数字（§3.1）。

**P2.2 `pkg/ai/rag`：embedder（§3.2）**
- 对接一个 embedding API，记录维度（供向量库 schema 校验）。
- 选型记录进 ADR（为什么选这个模型、是否 Matryoshka 降维）。

**P2.3 `pkg/ai/vectordb`（§5）**
- 选首个实现（建议 pgvector，与业务库一体、复用 PG 运维）。
- `Store` 接口落地：Upsert/Search/Delete/Health。
- HNSW vs IVF 参数取舍写进 ADR（§5.2 是面试深度点）。

**P2.4 `pkg/ai/rag`：retriever（§3.3）**
- 进阶路径：纯向量 → 混合（向量+BM25）+ RRF 融合 → + re-rank。
- RRF 公式实现（§3.3）。
- 可选：query rewriting / HyDE（§3.3）。

**P2.5 RAG 评估闭环（§3.5 + §6）**
- 检索质量：Recall@k、MRR、NDCG。
- 生成质量（接 P0.4 eval）：Faithfulness、Answer Relevancy、Context Precision/Recall。
- 把 RAG golden case 纳入 CI 门禁。
- ⚠️ 必须能列出 §3.4 的故障模式与对应修复手段。

**P2 验收**：完整 `文档→切分→embedding→入库→检索→生成→评估` 闭环；评估指标达标，每项故障有降级。

---

### P3 — Agent 主线

> **理念**：Agent = llm + 工具 + 原生 tool calling 循环 + 安全边界。2026 的实现不再解析 `Thought/Action/Observation` 文本；ReAct 只作为"推理-行动交替"的思想背景，执行层必须直接消费 provider 返回的结构化 tool call（§4.1/§4.2）。安全边界必须**内置**（§4.8）。

**P3.1 `pkg/ai/agent`：Tool 系统（§4.7）**
- `Tool` 接口 + 注册中心。
- 工具 schema 设计规范（JSON Schema/strict mode、明确 IO、description 写清何时使用、合理默认、有用错误、读操作幂等）。
- 先实现 2-3 个示范工具（如 `search_docs` 复用 P2 retriever、`get_time`）。

**P3.2 原生 tool calling executor（§4.1/§4.8）**
- `Executor` 实现：调用 LLM → 消费 `tool_calls` / `function_call` item → 执行工具 → 回传 `tool_result` / `function_call_output` → 继续直到最终答案。
- 抽象 provider 差异：OpenAI Responses API 的 `call_id` / `function_call_output`，Claude Messages 的 `tool_use_id` / `tool_result`。
- **安全边界（Limit）必须第一天就有**：maxSteps、stepTimeout、tokenBudget、循环检测。
- 禁止新增旧式文本 action 解析器；结构校验失败应作为 provider/tool schema 错误处理。
- TDD：构造会死循环的 tool 调用序列，验证被 maxSteps/loop_detected 终止。

**P3.3 Agent 评估（§4.9 + §6）**
- 评估维度与传统 LLM 不同：任务完成率、效率（步数/工具调用数）、路径质量、自我纠错能力。
- 记录 Tool Selection Accuracy、Argument Correctness、Step Efficiency、`pass^k`。
- 把 agent golden case 纳入 eval。
- ⚠️ 能讲「怎么防止无限循环」（§16.7）。

**P3 验收**：native tool-calling agent 能多步调用工具完成任务；越界行为被安全边界拦截；工具调用轨迹、效率指标与 token 成本被记录与评估。

---

### P4 — 性能与成本

> **理念**：P0–P3 让系统"对"，P4 让它"快且省"。优化金字塔见 §13.2（从最有效到最精细）。

**P4.1 `pkg/ai/cache`（§12）—— 语义缓存**
- 在 P1 `FallbackCache` 基础上增加 Redis 向量搜索实现 `SemanticCache`。
- 落地 §12.3 十法中的关键几项：metadata 过滤防跨用户污染、阈值调优、命中率监控。
- 命中结果必须可被 eval 标记（避免缓存错误答案）。

**P4.2 流式与并行（§13.1/§13.2）**
- HTTP 层暴露 SSE 流式端点（GoFrame `ghttp` 支持流式响应）。
- 独立操作并行化（多 query 检索、embedding 与分类并行）。
- ⚠️ TTFT 是用户感知延迟核心（§13.2 Level 1）。

**P4.3 延迟 SLO（§13.3）**
- 落地 `SLOAI`：TTFT P95、Total P95、错误率，配告警阈值（§8.1）。
- 超时分级配置（embedding/向量检索/rerank/简单 llm/复杂 agent step/总请求）。

**P4 验收**：语义缓存命中率达标；TTFT P95 满足 SLO；冷热路径有性能基线。

---

### P5 — 工程化收口

> **理念**：把前序能力变成"能在真实用户面前存活"的系统。对应 JD「看似平淡却至关重要的决策」。

**P5.1 Agent Harness ADR（§7）**
- 写 ADR：本框架默认自建 lightweight Agent Harness，第三方框架仅作为 adapter / app-layer integration 的理由（§7.3/§7.6）。
- 明确 harness 控制面：provider capability、native tool calling loop、tool contract、state/replay、limits、obs、eval、fallback。
- 能讲清"什么时候引入框架或平台组件"（§7.4），以及引入前必须验证哪些可观测、成本、数据隔离条件。

**P5.2 CI 评估门禁（§6.6）**
- 在 P0 最小 CI 基础上升级为质量门禁：PR 自动跑 eval，对比 baseline；指标持平/提升→通过；Faithfulness 下降>2%→拦截（阈值见 config）。
- 评估报告作为 PR comment。

**P5.3 可观测大盘（§8.2）**
- 质量指标（幻觉率、空回率、踩率）+ 性能指标（TTFT、P95）+ 成本指标（单请求成本、日成本）。
- 告警分级 P0–P3（§监控告警规范）。

**P5.4 灾难演练与 post-mortem（§9 + §15）**
- 模拟上游全挂、向量库挂、缓存击穿，验证降级链路。
- 每次演练写 `docs/journal/`，沉淀为面试故事库（§15 STAR）。

**P5.5 meta-evaluation（§6.7）**
- 校准 LLM-as-Judge 与人工评分相关性（Spearman/Kendall），<0.7 调 judge prompt。
- 评估集代表性检查、污染检测。

**P5 验收**：全链路可上线；CI 门禁生效；有完整 post-mortem；meta-evaluation 有数据支撑。

---

## 3. 每一步的工作流（AGENTS.md 完成定义的落地版）

落地任何一个子能力时，严格走这 6 步（对应全局 `rules/common/development-workflow.md` + `testing.md`）：

1. **调研**：`gh search code` / Context7 查已有实现与最佳实践。
2. **写失败测试 + golden case（RED）**：先证明需求存在。
3. **最小实现（GREEN）**：最简代码让测试过，不过度设计。
4. **重构（REFACTOR）**：在测试保护下优化。
5. **补横切**：trace 打点（obs）、安全边界/降级（resilience）、限流（ratelimit）按需接入。
6. **文档**：ADR 记选型理由，`docs/journal/` 记踩坑与修复。

> 缺任一步 = 该能力未完成。

---

## 4. P0 冷启动执行计划

> 本节用于后续会话冷启动。任何新会话如果要继续 P0，只需先阅读 `AGENTS.md`、`准备清单.md` 的相关章节、本文件本节，以及当前涉及包的代码。
> P0 的目标不是做完整产品功能，而是打通一个最小但真实的 AI 工程闭环：`prompt -> llm -> obs -> eval -> local gate`。

### 4.1 P0 总原则

- **核心抽象先于真实后端**：先稳定 `pkg/ai` 接口语义，再接入 OpenAI-compatible provider、Milvus/pgvector、LangFuse/OTEL 等实现。
- **真实服务通过 adapter 接入**：外部组件不得污染核心包。比如 RAG 只依赖 `vectordb.Store`，观测只依赖 `obs.Tracer`，评估只依赖 `eval.Dataset/Metric/Runner`。
- **测试默认使用 fake/in-memory 实现**：P0 不应被 Docker、网络或第三方服务阻塞；真实 provider 的集成测试可用 build tag 或环境变量控制。
- **错误边界要第一天明确**：4xx 参数错误、认证错误等不应包装成可重试错误；429/5xx/timeout 才进入 `llm.ErrUpstream`/resilience 路径。
- **trace 与 eval 不后补**：每个 P0 子能力都必须能被 trace，且至少有确定性测试证明行为。

### 4.2 P0-A：`pkg/ai/llm` 首个 Provider 实现

**目标**

实现一个 OpenAI-compatible provider adapter，作为 `llm.Provider` 的首个真实实现。它用于打通真实 LLM 调用路径，但核心接口不能被某一家 API 形态锁死。

**推荐包结构**

```text
pkg/ai/llm/
  llm.go                    # 已有核心契约
  llm_test.go               # 契约级测试/fake provider 测试
  openai/
    provider.go             # OpenAI-compatible adapter
    provider_test.go        # httptest 单元测试
    stream.go               # 流式解析，若 provider.go 过长再拆
    errors.go               # 错误映射，若逻辑复杂再拆
```

> 包名可以叫 `openai`，但实现必须支持 configurable `baseURL`，因此可兼容 DeepSeek、OpenRouter、本地 vLLM/Ollama OpenAI-compatible endpoint。具体生产供应商选择留到配置层。

**配置输入**

- `base_url`：默认 OpenAI API，可被环境变量/GoFrame config 覆盖。
- `api_key`：必须来自环境变量或密钥管理器，禁止硬编码。
- `default_timeout`：默认 60s。
- `organization/project`：可选，只有 provider 需要时才暴露。
- `default_model`：应用层可配置，但 `ChatRequest.Model` 优先。

**必须实现的行为**

- `Name()` 返回稳定 provider 名，例如 `openai-compatible` 或配置中的 provider id。
- `Capabilities(model string)` 返回模型能力声明，先用保守静态表或配置表，不做网络探测。
- `Chat(ctx, req)`：
  - 校验 `req != nil`、`Model`、`Messages`。
  - 使用 `ctx` 控制超时，不在函数内吞掉取消原因。
  - 映射 messages、tools、structured output、reasoning effort、temperature、max tokens。
  - 解析 content、tool calls、finish reason、usage。
- `ChatStream(ctx, req)`：
  - 返回只读 channel。
  - 首 token/usage 统计由消费端和 `obs` 记录，provider 只负责完整转发 chunk。
  - 流中错误放入 `ChatChunk.Err`，随后关闭 channel。
  - 流式 tool call 需要单独聚合：按 `index` 隔离多路 `delta.tool_calls`，拼接 `function.arguments` 分片，并在 JSON 完整后产出结构化 `ToolCall`。
- 错误映射：
  - timeout/context deadline、429、5xx -> 包装/归一为 `llm.ErrUpstream`。
  - 400/401/403/404 -> 返回明确错误，不标记为 `ErrUpstream`。
  - 响应 JSON 解析失败 -> 明确标记 provider protocol error。

**TDD 测试清单**

- RED 1：正常 `Chat` 从 mock server 返回 content、model、usage、finish reason。
- RED 2：tool call 响应能解析为 `[]llm.ToolCall`。
- RED 3：429/5xx/timeout 返回 `errors.Is(err, llm.ErrUpstream) == true`。
- RED 4：400/401 不返回 `ErrUpstream`，避免被上层重试。
- RED 5：`ChatStream` 能按顺序产出 delta，最终 chunk 携带 finish/usage。
- RED 6：ctx canceled 后请求尽快返回，不能泄漏 goroutine。
- RED 7：缺少 model/messages 时 fail fast，错误信息可读。
- RED 8：流式 `delta.tool_calls` 能按 index 聚合，并在 `finish_reason=tool_calls` 后产出结构化 tool call。
- RED 9：流式 tool call 的 arguments JSON 未完整前不得提前解析失败，多 tool call 并行分片不得串线。

**不在 P0-A 做的事**

- 不实现 provider failover；那是 P1 `resilience`。
- 不实现复杂模型路由；那是 P1 `ratelimit.ModelRouter`。
- 不实现 LangFuse/OTEL；这里只暴露可被 `obs` 包装的调用结果。
- 不在核心接口里加入某家 provider 独有字段；独有能力先放 adapter 内部或 `ProviderCapabilities`。

**验收标准**

- `go test ./pkg/ai/llm/...` 通过。
- mock provider 覆盖成功、错误、流式文本、非流式 tool call、流式 tool call、usage。
- 没有硬编码 API key、真实网络依赖或生产 `console/log` 调试输出。
- 能用一个小 demo 调起 `llm.Provider.Chat`，但 demo 不能成为测试的必需外部依赖。

**生产失效模式与降级预留**

- 上游超时/限流/5xx：P0-A 只返回 `ErrUpstream`，P1 负责重试、熔断、failover。
- 认证/参数错误：快速失败并暴露可诊断错误，避免无意义重试。
- 流式中途断开：通过 `ChatChunk.Err` 暴露给上层，后续由 API 层决定是否给用户友好提示。
- 流式 tool call 分片乱序或 JSON 不完整：adapter 不提前执行工具，必须等待聚合完成；协议无法恢复时通过 `ChatChunk.Err` 暴露。
- usage 缺失：允许返回零值，但 trace 中必须能看出 provider 未返回 usage，避免成本统计误判。

### 4.3 P0-B：`pkg/ai/prompt` 文件模板 Registry

**目标**

实现最小 Prompt as Code：从文件系统读取模板，使用 Go `text/template` 渲染，产出 `Rendered{Content, Version, Hash}`。

**推荐包结构**

```text
pkg/ai/prompt/
  prompt.go
  filesystem.go
  filesystem_test.go
resource/prompt/
  demo/
    v1.tmpl
```

**关键约束**

- `missingkey=error`，模板变量缺失必须失败。
- hash 基于渲染后 content，建议短 SHA-256，写入 trace。
- `name/version` 映射到文件路径时必须防路径穿越。
- P0 不引入远端 prompt 平台；LangFuse prompt management 可在 P5 作为 adapter 候选。

### 4.4 P0-C：`pkg/ai/obs` 日志型 Tracer

**目标**

实现一个本地 `Tracer`，先输出结构化 trace，后续可替换为 LangFuse/OTEL exporter。

**关键约束**

- 普通 trace 不记录原始 query、完整 prompt、tool 参数原文。
- 必须支持 `TraceID`、`PromptTemplateVer`、`PromptHash`、tokens、latency、model。
- P0 先同步记录即可；若接入真实热路径，再改为异步 batch。

**LangFuse 决策**

LangFuse 是强候选，但 P0-C 不直接绑定。后续实现应是：

```text
pkg/ai/obs/langfuse
  Tracer adapter -> LangFuse trace/generation/span
```

### 4.5 P0-D：`pkg/ai/eval` Runner 与首批 Golden Case

**目标**

实现最小离线评估 runner：读取本地 JSON dataset，调用 `PredictFn`，运行确定性 metrics，输出 `Report`。

**推荐包结构**

```text
pkg/ai/eval/
  eval.go
  runner.go
  dataset_json.go
  metrics.go
  runner_test.go
internal/eval/golden/
  p0_smoke.json
```

**首批 metric**

- `ExactMatch`：适合确定性格式或短答案。
- `ContainsAll`：检查答案是否包含必需关键词。
- `ContextHit`：预测 context 是否命中 relevant context id/text。

**不在 P0-D 做的事**

- 不做 LLM-as-Judge 的完整 bias 校准。
- 不依赖 LangFuse dataset；后续可做 dataset sync adapter。
- 不追求大规模 case，先让 eval 成为日常门禁。

### 4.6 P0-E：最小本地门禁

**目标**

提供后续每次开发都能跑的本地命令。

**建议命令**

```bash
go test ./...
go test -race ./pkg/ai/...
go vet ./...
```

如果增加 `Makefile`：

```text
make test
make test-race
make vet
make eval-smoke
```

**验收**

- P0 的测试不依赖真实 API key。
- 有真实 API key 时可以手动跑 provider smoke，但不进入默认 CI。
- `eval-smoke` 能输出稳定 report。

### 4.7 暂缓决策记录

以下选型先不做最终决定，只设计隔离边界：

- **向量数据库**：pgvector vs Milvus。P2 先依赖 `vectordb.Store`，真实实现可并行 spike。
- **可观测/评估平台**：本地日志/OTEL vs LangFuse。P0 先依赖 `obs.Tracer` 和 `eval` 抽象，LangFuse 作为 adapter 候选。

后续需要写 ADR 时，优先记录“为什么暂缓决策、当前抽象如何保护可替换性”，而不是急着选唯一答案。

---

## 5. 当前进度

- [x] P0.0 GoFrame v2 应用骨架 + `pkg/ai` 内核接口骨架（本次完成）
- [x] P0-A llm 首个 OpenAI-compatible provider 实现
- [x] P0-B prompt 文件模板渲染
- [x] P0-C obs 日志型 trace 骨架
- [x] P0-D eval runner + 首批 golden case
- [x] P0-E 最小 CI / 本地门禁
- [x] US3 检索与 Agent 能力证明基础：recursive chunker、memory vector store、retriever、检索指标、tool registry、native tool calling executor、agent smoke case。
- [x] US4 生产式故障诊断基础：circuit breaker、provider wrapper、失败 trace、连续 refill token bucket、exact/stale fallback cache。
- [x] US5 后端可替换边界：provider、vectordb、obs、eval dataset、fallback cache 契约测试，向量库和可观测 adapter ADR。
- [ ] P1 / P2 / P3 / P4 / P5 …

当前默认验证命令：

```bash
make test
make test-race
make vet
make eval-smoke
```

关键局部契约验证：

```bash
go test ./pkg/ai/llm -run TestProviderAdaptersAreReplaceable -count=1
go test ./pkg/ai/vectordb -run TestMemoryStoreContract -count=1
go test ./pkg/ai/obs -run TestLoggerTracerContract -count=1
go test ./pkg/ai/eval -run TestJSONDatasetContract -count=1
go test ./pkg/ai/cache -run TestMemoryFallbackCacheContract -count=1
```

> 进度更新规则：每完成一个子项，勾选并在 `docs/journal/` 留一条记录。
