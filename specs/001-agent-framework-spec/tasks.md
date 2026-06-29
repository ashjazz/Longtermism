# 任务拆解：生产级 AI Agent 框架

**输入文档**：`specs/001-agent-framework-spec/spec.md`、`specs/001-agent-framework-spec/plan.md`、`docs/ROADMAP.md`、`准备清单.md`  
**可用设计文档**：`research.md`、`data-model.md`、`contracts/`、`quickstart.md`、`.specify/memory/constitution.md`  
**生成日期**：2026-06-22  
**技术组长视角**：任务按用户故事组织，每个任务尽量只触达一个组件或一个高内聚能力；执行时优先实时阅读相关代码，其它任务描述只作为依赖参考。

## 任务格式与质量约束

每个任务必须符合：

示例格式：`- [ ] T001 [P] [US1] 在 path/to/file.go 执行具体任务；质量门控：说明验收要求。`

通用质量门控：

1. 所有代码任务必须先写失败测试，再写实现，再重构。
2. 默认测试不得强制依赖真实 API key、真实向量库或实时外部平台。
3. 涉及外部调用、用户输入、文件路径、日志或敏感内容时，必须显式处理错误与安全边界。
4. 生产代码不得包含 `console.log`、调试输出、硬编码密钥或静默吞错。
5. 每个完成的 AI 能力必须有测试、评估或回归证据、可观测性、失败模式、降级路径和文档。

## 任务状态维护规则

任务状态是后续会话恢复上下文的事实来源。执行任务时必须遵守以下维护规则，避免代码、文档和路线图逐步分叉。

### 开始任务前

1. 在本文件中找到第一个未完成任务，确认它的编号、阶段、用户故事和质量门控。
2. 阅读该任务直接涉及的文件；如果任务依赖架构决策，先阅读 `docs/adr/README.md` 和相关 ADR。
3. 如果任务是代码任务，先补失败测试，再实现，再重构；如果任务是文档任务，先确认它要服务的恢复、审查或学习场景。

### 完成任务时

1. 只有在质量门控满足后，才把任务从 `[ ]` 改为 `[X]`。
2. 本轮实际修改了哪些文件，就在最终响应中说明；纯文档任务不需要强行运行 Go 测试。
3. 代码任务必须记录验证命令和结果；如果命令因环境限制未运行或失败，必须保留原因和后续动作。
4. 不得提前勾选依赖任务、后续任务或只完成了设计但未完成验收的任务。

### 同步文档规则

根据任务性质同步以下文档：

| 触发条件 | 必须同步的位置 | 同步内容 |
|----------|----------------|----------|
| 新增、修改或废弃架构决策 | `docs/adr/<编号>-<标题>.md`、`docs/adr/README.md` | 决策背景、备选方案、影响、状态和索引项。 |
| 出现真实失败、修复、调试经验或学习沉淀 | `docs/journal/<编号>-<主题>.md` | 发生了什么、根因、修复、学到什么、后续预防。 |
| 改变阶段边界、推进顺序、P0 子阶段范围或验收命令 | `docs/ROADMAP.md` | 新的导航、阶段说明、验证命令或进度说明。 |
| 改变本地启动、验证、恢复上下文或 smoke 流程 | `specs/001-agent-framework-spec/quickstart.md` | 可执行命令、期望输出、恢复步骤或验证清单。 |
| 改变核心规格、用户故事、成功标准或范围边界 | `specs/001-agent-framework-spec/spec.md` | 需求、成功标准、范围说明或验收条件。 |
| 改变技术方案、约束、风险或项目结构 | `specs/001-agent-framework-spec/plan.md` | 技术上下文、风险缓解、阶段计划或结构说明。 |
| 改变任务拆解、依赖或质量门控 | `specs/001-agent-framework-spec/tasks.md` | 任务状态、任务说明、依赖关系或维护规则。 |

### 最小收口检查

每个任务结束前至少检查：

1. `tasks.md` 中当前任务状态是否准确。
2. 任务要求的 ADR、journal、ROADMAP 或 quickstart 是否已经同步。
3. 验证命令是否与任务性质匹配，且结果可被最终响应复述。
4. 新增文档是否使用中文，新增代码是否保留足够的学习型注释。

## Phase 1：Setup（项目初始化与执行护栏）

**目标**：补齐后续任务执行所需的目录、文档索引、本地命令入口和规范入口。  
**独立验收**：阅读 `AGENTS.md`、`docs/ROADMAP.md` 和 `specs/001-agent-framework-spec/plan.md` 后，执行者可以明确当前计划、质量门控、文档位置和默认验证命令。

- [X] T001 在 `docs/adr/README.md` 建立 ADR 索引与状态说明；质量门控：必须说明 accepted/proposed/deferred/superseded 四类状态，并链接当前 spec-kit 计划。
- [X] T002 在 `docs/journal/README.md` 建立学习日志模板；质量门控：模板必须包含“发生了什么、根因、修复、学到什么、后续预防”五项。
- [X] T003 在 `.gitignore` 增补 `.DS_Store` 与本地临时产物忽略规则；质量门控：不得忽略源码、规格、计划、测试或配置模板。
- [X] T004 在 `Makefile` 增加 `test`、`test-race`、`vet`、`eval-smoke` 占位命令；质量门控：命令失败必须返回非零退出码，且 `eval-smoke` 在实现前给出清晰 TODO 提示。
- [X] T005 在 `internal/eval/README.md` 说明 golden case 与 eval smoke 的目录职责；质量门控：必须声明默认评估不依赖真实模型服务。
- [X] T006 在 `resource/prompt/README.md` 说明 prompt 模板命名、版本和变量约定；质量门控：必须要求模板缺失变量时失败。
- [X] T007 在 `docs/adr/0001-defer-vector-and-observability-backends.md` 记录 pgvector/Milvus 与日志/OTEL/LangFuse 暂缓决策；质量门控：必须包含背景、决策、备选方案、影响和重新审视条件。
- [X] T008 在 `docs/ROADMAP.md` 更新 spec-kit 规格、计划、任务文件链接；质量门控：不得改变既有 P0-P5 阶段语义，只补充导航。

## Phase 2：Foundational（阻塞所有用户故事的共同基础）

**目标**：建立共享测试工具、错误处理约定和质量门禁脚手架。  
**独立验收**：`go test ./...` 可运行；共享测试工具不依赖外部服务；错误分类与隐私边界在文档和测试中可被引用。

- [X] T009 [P] 在 `pkg/ai/llm/llm_contract_test.go` 添加 provider 契约测试骨架；质量门控：测试必须能被任意 fake provider 复用，不调用真实网络。
- [X] T010 [P] 在 `pkg/ai/llm/testutil/fake_provider.go` 添加 fake provider 测试工具；质量门控：fake provider 必须支持成功响应、tool call、流式 chunk、上游错误和取消场景。
- [X] T011 [P] 在 `pkg/ai/obs/testutil/recorder.go` 添加内存 trace recorder；质量门控：recorder 必须可断言 trace 数量和字段，且不暴露并发数据竞争。
- [X] T012 [P] 在 `pkg/ai/eval/testutil/static_dataset.go` 添加静态 dataset 测试工具；质量门控：工具必须返回不可变样本副本，避免测试间共享可变状态。
- [X] T013 在 `pkg/ai/internal/apperror/errors.go` 定义内部错误分类辅助；质量门控：必须支持 errors.Is/errors.As，不得替代已有公开契约错误。
- [X] T014 在 `docs/adr/0002-p0-error-classification.md` 记录 4xx/429/5xx/timeout 的分类策略；质量门控：必须说明为什么 4xx 不进入重试/熔断路径。
- [X] T015 在 `docs/adr/0003-p0-local-validation-without-live-services.md` 记录默认离线验证策略；质量门控：必须说明真实服务 smoke 与默认门禁的边界。
- [X] T016 在 `specs/001-agent-framework-spec/quickstart.md` 补充任务执行顺序和 P0 验证清单；质量门控：必须能让新会话从文档恢复上下文。

## Phase 3：US1 - 遵循生产级学习路径（优先级：P1）

**用户故事目标**：让学习者和后续 AI 执行者理解项目目标、阶段、完成证据和范围边界。  
**独立测试标准**：新执行者只阅读规格、计划、任务、ROADMAP 和 AGENTS，即可说明下一步应该执行哪个 P0 任务，以及完成标准是什么。

- [X] T017 [P] [US1] 在 `specs/001-agent-framework-spec/tasks.md` 增补任务状态维护规则；质量门控：必须说明任务完成后如何同步 ROADMAP、ADR 或 journal。
- [X] T018 [P] [US1] 在 `docs/ROADMAP.md` 为 P0-A 到 P0-E 添加任务文件引用；质量门控：每个 P0 子阶段都必须能链接回 `tasks.md` 的对应 phase。
- [X] T019 [P] [US1] 在 `AGENTS.md` 的 SPECKIT 区补充 `spec.md`、`plan.md`、`tasks.md` 三个入口；质量门控：保留 `<!-- SPECKIT START -->` 与 `<!-- SPECKIT END -->` 标记。
- [X] T020 [US1] 在 `docs/journal/0001-spec-kit-planning.md` 记录本轮规格/计划/任务拆解过程；质量门控：必须包含“为什么先做 P0 最小闭环”的工程判断。
- [X] T021 [US1] 在 `specs/001-agent-framework-spec/checklists/requirements.md` 追加任务就绪检查项；质量门控：必须覆盖自解释性、独立执行性、质量门控和文件路径完整性。
- [X] T022 [US1] 在 `docs/adr/0004-lightweight-harness-first.md` 记录自建 lightweight harness 优先原则；质量门控：必须说明第三方框架只能作为 adapter/app-layer integration。

## Phase 4：US2 - 建立最小 AI 工程闭环（优先级：P1）

**用户故事目标**：打通 `prompt -> llm -> obs -> eval -> local gate` 的 P0 最小闭环。  
**独立测试标准**：默认本地命令可以验证 prompt 渲染、fake/model interaction、trace recorder、eval runner 和 eval smoke，不要求真实外部服务。

### P0-A：模型供应商 Adapter

- [X] T023 [P] [US2] 在 `pkg/ai/llm/openai/provider_test.go` 编写 provider 初始化与必填配置失败测试；质量门控：缺少 baseURL/model/messages 时必须 fail fast 且错误可读。
- [X] T024 [P] [US2] 在 `pkg/ai/llm/openai/provider_test.go` 编写 Chat 正常响应解析测试；质量门控：必须断言 content、model、finish reason、usage。
- [X] T025 [P] [US2] 在 `pkg/ai/llm/openai/provider_test.go` 编写 tool call 解析测试；质量门控：必须断言 tool call id、name、arguments，且 arguments 不用字符串拼接解析。
- [X] T026 [P] [US2] 在 `pkg/ai/llm/openai/provider_test.go` 编写错误映射测试；质量门控：429/5xx/timeout 必须 errors.Is 为 `llm.ErrUpstream`，400/401 不得为 `llm.ErrUpstream`。
- [X] T027 [P] [US2] 在 `pkg/ai/llm/openai/stream_test.go` 编写流式响应测试；质量门控：必须断言 delta 顺序、最终 finish reason、最终 usage 和流中错误。
- [X] T028 [P] [US2] 在 `pkg/ai/llm/openai/provider_test.go` 编写 context cancel 测试；质量门控：取消后请求必须尽快返回，测试不得使用裸 `time.Sleep` 等待。
- [X] T029 [US2] 在 `pkg/ai/llm/openai/provider.go` 实现 provider 配置、构造函数、Name 和 Capabilities；质量门控：不得硬编码 API key，capabilities 使用保守静态表或配置表。
- [X] T030 [US2] 在 `pkg/ai/llm/openai/provider.go` 实现 Chat 请求映射；质量门控：messages、tools、structured output、reasoning effort、temperature、max tokens 必须映射清晰。
- [X] T031 [US2] 在 `pkg/ai/llm/openai/provider.go` 实现 Chat 响应解析；质量门控：content、tool calls、finish reason、usage 都必须处理缺省值。
- [X] T032 [US2] 在 `pkg/ai/llm/openai/errors.go` 实现 HTTP 与上下文错误分类；质量门控：错误包装必须支持 errors.Is，且保留可诊断上下文。
- [X] T033 [US2] 在 `pkg/ai/llm/openai/stream.go` 实现 SSE/流式 chunk 解析；质量门控：流结束必须关闭 channel，流中错误必须通过 chunk 暴露。
- [X] T034A [US2] 在 `pkg/ai/llm/openai/stream_tool_call_test.go` 编写流式 tool call 聚合测试；质量门控：必须覆盖 `delta.tool_calls` 分片、按 index 隔离聚合、arguments 延迟 JSON 解析、`finish_reason=tool_calls` 后产出结构化 `ToolCall`。
- [X] T034B [US2] 在 `pkg/ai/llm/openai/stream_tool_call.go` 实现流式 tool call 聚合器并接入 `ChatStream`；质量门控：JSON arguments 不完整时不得提前失败，多 tool call 并行分片不得串线，能力声明必须在支持模型上启用 `StreamingToolCall`。
- [X] T034 [US2] 在 `pkg/ai/llm/llm_contract_test.go` 接入 openai provider mock server 契约测试；质量门控：契约测试必须覆盖 fake 与真实 adapter 的共同语义。

### P0-B：Prompt as Code

- [X] T035 [P] [US2] 在 `pkg/ai/prompt/filesystem_test.go` 编写文件模板 registry 测试；质量门控：覆盖正常加载、模板不存在、版本不存在、路径穿越拒绝。
- [X] T036 [P] [US2] 在 `pkg/ai/prompt/render_test.go` 编写渲染测试；质量门控：缺失变量必须失败，hash 对同一内容稳定、对不同内容变化。
- [X] T037 [US2] 在 `pkg/ai/prompt/filesystem.go` 实现文件系统 registry；质量门控：路径必须通过 clean/join 后限制在根目录内。
- [X] T038 [US2] 在 `pkg/ai/prompt/render.go` 实现 text/template 渲染与 SHA-256 短 hash；质量门控：使用 `missingkey=error`，不允许静默空值。
- [X] T039 [P] [US2] 在 `resource/prompt/p0_smoke/v1.tmpl` 添加 P0 冒烟 prompt 模板；质量门控：模板变量必须与测试样例一致。

### P0-C：本地 Trace

- [X] T040 [P] [US2] 在 `pkg/ai/obs/logger_test.go` 编写日志型 tracer 测试；质量门控：必须断言 trace id、model、prompt hash、tokens、latency、status。
- [X] T041 [P] [US2] 在 `pkg/ai/obs/privacy_test.go` 编写隐私边界测试；质量门控：普通 trace 输出不得包含原始 query、完整 prompt 或 tool 参数。
- [X] T042 [US2] 在 `pkg/ai/obs/logger.go` 实现日志型 Tracer；质量门控：记录失败不得影响主流程，字段名保持稳定。
- [X] T043 [US2] 在 `pkg/ai/obs/trace_helpers.go` 实现 trace 构建辅助函数；质量门控：辅助函数必须使用不可变输入，不修改调用方对象。

### P0-D：Eval Runner

- [X] T044 [P] [US2] 在 `pkg/ai/eval/dataset_json_test.go` 编写 JSON dataset 加载测试；质量门控：覆盖正常、空文件、非法 JSON、缺少样例 ID。
- [X] T045 [P] [US2] 在 `pkg/ai/eval/metrics_test.go` 编写 ExactMatch、ContainsAll、ContextHit 指标测试；质量门控：每个指标覆盖 happy path、边界、错误输入。
- [X] T046 [P] [US2] 在 `pkg/ai/eval/runner_test.go` 编写 runner/report 测试；质量门控：必须断言 sample count、metric averages、prediction error 处理。
- [X] T047 [US2] 在 `pkg/ai/eval/dataset_json.go` 实现 JSON dataset；质量门控：Load 返回样本副本，避免调用方修改内部状态。
- [X] T048 [US2] 在 `pkg/ai/eval/metrics.go` 实现确定性 metrics；质量门控：分数范围必须稳定在 0 到 1。
- [X] T049 [US2] 在 `pkg/ai/eval/runner.go` 实现评估 runner；质量门控：单样本错误必须可诊断，不得静默吞掉。
- [X] T050 [P] [US2] 在 `internal/eval/golden/p0_smoke.json` 添加 P0 冒烟 golden case；质量门控：至少包含正常、边界、错误或缺失上下文三类样例。

### P0-E：本地门禁与闭环

- [X] T051 [US2] 在 `cmd/eval-smoke/main.go` 实现 eval smoke 命令；质量门控：默认使用 fake predict，不需要真实 API key。
- [X] T052 [US2] 在 `Makefile` 接入 `eval-smoke` 实际命令；质量门控：失败时必须返回非零退出码并打印报告路径或失败原因。
- [X] T053 [US2] 在 `internal/eval/smoke/p0.go` 组合 prompt、fake llm、trace recorder、eval runner；质量门控：必须形成完整 `prompt -> llm -> obs -> eval` 路径。
- [X] T054 [US2] 在 `specs/001-agent-framework-spec/quickstart.md` 更新 P0 实际命令与期望输出；质量门控：文档命令必须与 Makefile 保持一致。
- [X] T055 [US2] 在 `docs/journal/0002-p0-minimum-loop.md` 记录 P0 闭环实现经验；质量门控：必须包含至少一个失败模式和对应修复。

## Phase 5：US3 - 证明检索与 Agent 能力改进（优先级：P2）

**用户故事目标**：为后续 RAG 与 Agent 能力建立可评估、可回归的任务基础。  
**独立测试标准**：检索和 Agent 的初始实现可以通过独立单元测试与 eval case 证明行为，不依赖完整生产后端。

- [X] T056 [P] [US3] 在 `pkg/ai/rag/chunker_test.go` 编写 recursive chunker 测试；质量门控：覆盖空文档、短文档、长文档、overlap、metadata 保留。
- [X] T057 [US3] 在 `pkg/ai/rag/chunker_recursive.go` 实现 recursive chunker；质量门控：不得修改输入 Document，chunk ID 必须稳定可复现。
- [X] T058 [P] [US3] 在 `pkg/ai/vectordb/memory_store_test.go` 编写内存向量库测试；质量门控：覆盖 Upsert、Search、Delete、Health、metadata filter。
- [X] T059 [US3] 在 `pkg/ai/vectordb/memory_store.go` 实现内存 Store；质量门控：仅用于测试和本地 demo，不作为生产向量库决策。
- [X] T060 [P] [US3] 在 `pkg/ai/rag/retriever_test.go` 编写基础 retriever 测试；质量门控：检索为空不得 panic，filter 必须传递到 Store。
- [X] T061 [US3] 在 `pkg/ai/rag/retriever.go` 实现基础 retriever；质量门控：错误必须带查询上下文但不得记录原始敏感内容。
- [X] T062 [P] [US3] 在 `pkg/ai/eval/retrieval_metrics_test.go` 编写 Recall@k、MRR、NDCG 初始测试；质量门控：指标必须可解释且分数范围稳定。
- [X] T063 [US3] 在 `pkg/ai/eval/retrieval_metrics.go` 实现检索指标；质量门控：空结果、空相关集必须有明确返回语义。
- [X] T064 [P] [US3] 在 `pkg/ai/agent/registry_test.go` 编写工具注册中心测试；质量门控：重复注册、未知工具、schema 缺失必须有明确错误。
- [X] T065 [US3] 在 `pkg/ai/agent/registry.go` 实现工具注册中心；质量门控：读操作必须并发安全，注册后不得暴露可变内部 map。
- [X] T066 [P] [US3] 在 `pkg/ai/agent/executor_limits_test.go` 编写 Agent executor 限制测试；质量门控：覆盖 max steps、loop detected、step timeout、token budget。
- [X] T067 [US3] 在 `pkg/ai/agent/executor.go` 实现最小 native tool calling executor；质量门控：不得解析旧式 Thought/Action 文本，只消费结构化 tool call。
- [X] T068 [P] [US3] 在 `internal/eval/golden/agent_smoke.json` 添加 Agent 评估样例；质量门控：至少覆盖成功、工具错误、自我纠错或步数限制。

## Phase 6：US4 - 诊断生产式故障（优先级：P2）

**用户故事目标**：让上游故障、检索失败、Agent 循环、超时和预算耗尽都有可诊断输出。  
**独立测试标准**：模拟失败时，trace、错误和降级结果能明确说明发生了什么，不泄露敏感原文。

- [X] T069 [P] [US4] 在 `pkg/ai/resilience/circuit_breaker_test.go` 编写断路器状态测试；质量门控：覆盖 closed/open/half-open/恢复和快速失败。
- [X] T070 [US4] 在 `pkg/ai/resilience/circuit_breaker.go` 实现断路器；质量门控：状态转换必须可测试，错误必须保留 cause。
- [X] T071 [P] [US4] 在 `pkg/ai/resilience/provider_wrapper_test.go` 编写 provider wrapper 测试；质量门控：ErrUpstream 触发熔断记录，4xx 不触发上游熔断。
- [X] T072 [US4] 在 `pkg/ai/resilience/provider_wrapper.go` 实现 llm.Provider 包装器；质量门控：不得改变原始 Provider 接口语义。
- [X] T073 [P] [US4] 在 `pkg/ai/obs/failure_trace_test.go` 编写失败 trace 测试；质量门控：覆盖 timeout、rate limit、retrieval miss、loop detected、budget exceeded。
- [X] T074 [US4] 在 `pkg/ai/obs/failure_trace.go` 实现失败状态 trace helper；质量门控：状态枚举必须稳定并可被 eval/journal 引用。
- [X] T075 [P] [US4] 在 `pkg/ai/ratelimit/memory_limiter_test.go` 编写内存限流测试；质量门控：覆盖全局、用户、provider key，不使用真实 Redis。
- [X] T076 [US4] 在 `pkg/ai/ratelimit/memory_limiter.go` 实现内存 token bucket；质量门控：并发访问不得产生 data race。
- [X] T077 [P] [US4] 在 `pkg/ai/cache/memory_fallback_test.go` 编写 exact/stale cache 测试；质量门控：必须验证 tenant/user scope 隔离。
- [X] T078 [US4] 在 `pkg/ai/cache/memory_fallback.go` 实现内存 fallback cache；质量门控：返回结果必须标记 exact 或 stale。
- [X] T079 [US4] 在 `docs/journal/0003-failure-diagnostics.md` 记录故障诊断演练；质量门控：至少包含一个模拟上游失败和一个模拟 Agent 循环。

## Phase 7：US5 - 替换后端而不重写核心能力（优先级：P3）

**用户故事目标**：保持模型、向量库、可观测、评估和缓存后端可替换，避免早期锁定。  
**独立测试标准**：替换 fake/in-memory 与未来真实 adapter 时，核心契约测试仍可运行，能力预期不改变。

- [X] T080 [P] [US5] 在 `pkg/ai/vectordb/store_contract_test.go` 编写 Store 契约测试；质量门控：任何 pgvector/Milvus/memory 实现都必须通过同一套契约测试。
- [X] T081 [P] [US5] 在 `pkg/ai/obs/tracer_contract_test.go` 编写 Tracer 契约测试；质量门控：日志、LangFuse、OTEL adapter 均应能复用。
- [X] T082 [P] [US5] 在 `pkg/ai/eval/dataset_contract_test.go` 编写 Dataset 契约测试；质量门控：local JSON 和未来平台同步实现都应能复用。
- [X] T083 [US5] 在 `docs/adr/0005-vector-store-adapter-boundary.md` 记录向量库 adapter 边界；质量门控：必须比较 pgvector、Milvus、memory fake 的职责差异。
- [X] T084 [US5] 在 `docs/adr/0006-observability-adapter-boundary.md` 记录观测平台 adapter 边界；质量门控：必须说明 LangFuse/OTEL/本地日志不进入核心契约。
- [ ] T085 [US5] 在 `pkg/ai/cache/cache_contract_test.go` 编写 cache 契约测试；质量门控：必须覆盖 scope 隔离、ttl、miss、stale 行为。
- [ ] T086 [US5] 在 `pkg/ai/llm/provider_contract_test.go` 编写 provider adapter 替换测试；质量门控：fake 与 openai-compatible adapter 的契约语义必须一致。
- [ ] T087 [US5] 在 `specs/001-agent-framework-spec/contracts/core-framework-contract.md` 更新后端替换验收条件；质量门控：必须明确“替换后不改变用户可见能力预期”。

## Final Phase：Polish & Cross-Cutting Concerns

**目标**：收口文档、门禁、覆盖率、审查和路线图状态。  
**独立验收**：所有 P0 默认命令通过；ROADMAP 与任务状态一致；关键 ADR 和 journal 已补齐。

- [ ] T088 [P] 在 `docs/ROADMAP.md` 勾选已完成的 P0 子项并补充验证命令；质量门控：只能勾选真实完成项，不得提前标记。
- [ ] T089 [P] 在 `specs/001-agent-framework-spec/quickstart.md` 更新最终 P0 验证输出示例；质量门控：示例必须来自真实命令输出或明确标记为示意。
- [ ] T090 [P] 在 `docs/adr/README.md` 更新 ADR 索引状态；质量门控：所有新增 ADR 都必须出现在索引中。
- [ ] T091 在 `Makefile` 确认 `test`、`test-race`、`vet`、`eval-smoke` 全部可运行；质量门控：任一失败必须阻止完成。
- [ ] T092 在 `specs/001-agent-framework-spec/checklists/requirements.md` 追加任务执行完成审查结果；质量门控：必须检查测试、评估、可观测、降级、文档五项。
- [ ] T093 在 `docs/journal/0004-p0-retrospective.md` 记录 P0 阶段复盘；质量门控：必须包含至少三条“出过事 -> 修好了 -> 学到了”的候选故事素材。
- [ ] T094 在 `AGENTS.md` 更新 SPECKIT 区包含 `tasks.md`；质量门控：保留现有 spec-kit 标记，并确保路径为项目相对路径。

## 依赖关系

```text
Phase 1 Setup
  -> Phase 2 Foundational
    -> US1 学习路径治理
    -> US2 P0 最小 AI 工程闭环
      -> US3 检索与 Agent 评估基础
      -> US4 生产式故障诊断
        -> US5 后端可替换边界
          -> Final Polish
```

关键依赖：

- T009-T016 是所有代码任务的共同基础。
- US2 是 P0 MVP 的核心，US3/US4/US5 可以在 US2 的相关契约稳定后并行推进。
- US3 的 Agent executor 依赖 US2 的 `llm.Provider` 语义稳定。
- US4 的 resilience provider wrapper 依赖 US2 的错误映射稳定。
- US5 的契约测试依赖对应抽象和至少一个 fake/in-memory 实现存在。

## 并行执行机会

### Setup 并行

```text
T001, T002, T003, T005, T006, T007 可并行
T004 可能与 T051/T052 后续联动，建议单独执行
```

### Foundational 并行

```text
T009, T010, T011, T012 可并行
T014, T015, T016 可与测试工具任务并行
```

### US2 并行

```text
P0-A 测试任务 T023-T028、T034A 可并行
P0-B 测试任务 T035-T036 可并行
P0-C 测试任务 T040-T041 可并行
P0-D 测试任务 T044-T046 可并行
实现任务 T029-T034B、T034、T037-T039、T042-T043、T047-T050 应按各组件内部顺序执行
```

### US3/US4/US5 并行

```text
US3 中 chunker、memory vectordb、agent registry 的测试可并行
US4 中 circuit breaker、failure trace、memory limiter、fallback cache 的测试可并行
US5 中 vectordb/tracer/dataset/cache/provider 契约测试可并行
```

## 实施策略

### MVP 优先

最小可交付范围建议为：

```text
Phase 1 + Phase 2 + US1 + US2
```

这会产生一个可解释、可运行、可观测、可评估的 P0 最小闭环。

### 增量推进

1. 先完成文档和测试基础，避免后续任务读不懂上下文。
2. 按 P0-A 到 P0-E 执行 US2，严格遵守 RED-GREEN-REFACTOR。
3. 每完成一个组件，立即运行该包测试和相关 quickstart 步骤。
4. 完成 US2 后再进入 US3/US4/US5，避免在地基不稳时扩展复杂能力。
5. 最后执行 Polish，统一更新 ROADMAP、ADR、journal 和 spec-kit checklist。

### 审查要求

完成任一用户故事后，必须进行以下检查：

- 是否所有任务都包含文件级变更或明确文档产出。
- 是否默认测试不依赖真实外部服务。
- 是否存在硬编码密钥、敏感日志或静默吞错。
- 是否有评估或回归证据。
- 是否更新相关 ADR、journal 或 ROADMAP。
