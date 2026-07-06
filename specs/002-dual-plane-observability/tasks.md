# 任务拆解：双平面观测与评估体系 v1

**输入**：`specs/002-dual-plane-observability/spec.md`、`plan.md`、`research.md`、`data-model.md`、`contracts/observability-v1-contract.md`、`quickstart.md`
**前置文档**：`docs/adr/0006-observability-adapter-boundary.md`、`docs/adr/0007-dual-plane-observability-evaluation-v1.md`、`.specify/memory/constitution.md`
**生成日期**：2026-07-03

## 任务格式规则

所有任务必须使用 markdown checkbox、顺序任务编号、可选并行标记、用户故事标记和明确文件路径，例如：`- [ ] T001 [P] [US1] 在 path/to/file.go 执行具体任务；质量门控：说明验收要求。`

任务约束：

1. 代码任务必须遵循 TDD：先写失败测试，再写实现，再重构。
2. 默认验证不得依赖真实外部观测平台、真实 API key 或付费服务。
3. 涉及用户输入、prompt、tool args、token、密钥、外部响应或跨服务传播字段时，必须显式验证隐私边界。
4. 所有观测能力必须可诊断、可降级、可回归，并保留学习型注释或学习资产入口。
5. 每个任务只聚焦一个文件、一个测试面、一个实现面或一个文档面，避免大而全。

## Phase 1：Setup

**目标**：补齐 Observability v1 后续实施需要的目录、文档索引、本地命令入口和配置占位。该阶段不实现核心能力，只让后续任务有稳定落点。

**独立验收**：新执行者阅读 `spec.md`、`plan.md`、`tasks.md` 和 `docs/observability/README.md` 后，可以理解阶段顺序、学习资产位置、默认验证命令和真实平台 smoke 的边界。

- [X] T001 在 `docs/observability/README.md` 建立观测学习资产索引；质量门控：必须列出系统可观测性、分布式追踪、AI Agent 可观测性、评估证据关联、隐私边界、平台接入取舍六类主题入口。
- [X] T002 在 `docs/observability/template.md` 建立学习资产模板；质量门控：模板必须包含“理论概念、关键问题、工程实验、最佳实践、失败模式、降级路径、复盘问题、关联任务/测试”八项。
- [X] T003 在 `docs/journal/README.md` 补充 Observability v1 复盘分类说明；质量门控：不得删除既有 journal 模板，必须说明观测链路断裂、字段泄露、eval 回链失败、平台上报失败四类记录场景。
- [X] T004 在 `manifest/config/config.yaml` 增加 observability opt-in 配置占位；质量门控：不得包含真实 endpoint、密钥或 token，必须默认关闭真实平台 smoke。
- [X] T005 在 `Makefile` 增加 `obs-smoke` 占位命令；质量门控：命令默认运行离线 smoke 或输出清晰 TODO，不得要求真实平台配置。
- [X] T006 在 `specs/002-dual-plane-observability/quickstart.md` 同步 `obs-smoke` 本地验证入口；质量门控：文档必须区分默认离线验证与真实平台 opt-in smoke。

## Phase 2：Foundational Correlation & Contracts

**目标**：建立两个观测平面共享的关联身份、字段契约、安全摘要、测试 sink 和失败状态。该阶段是后续基础设施平面、AI 语义平面和 eval 回链的共同地基。

**独立验收**：`go test ./pkg/ai/obs/...` 可以验证 request/span/AI/eval 关联身份、observation type、安全摘要、契约记录、本地 span sink 和上报失败状态，不访问真实外部平台。

- [X] T007 [P] 在 `pkg/ai/obs/correlation_test.go` 编写关联身份测试；质量门控：先写失败测试，覆盖 request_id、service_trace_id、span_id、ai_trace_id、session_id、eval_run_id 的构造与不可变复制。
- [X] T008 在 `pkg/ai/obs/correlation.go` 实现 CorrelationIdentity 与上下文辅助；质量门控：必须通过 T007，不得从 context 序列化任意敏感值。
- [X] T009 [P] 在 `pkg/ai/obs/baggage_policy_test.go` 编写 baggage 白名单测试；质量门控：先写失败测试，覆盖允许传播字段、禁止传播字段和敏感值拒绝。
- [X] T010 在 `pkg/ai/obs/baggage_policy.go` 实现 baggage 白名单策略；质量门控：必须通过 T009，只允许低敏关联字段跨服务传播。
- [X] T011 [P] 在 `pkg/ai/obs/observation_type_test.go` 编写 observation type 验证测试；质量门控：先写失败测试，覆盖 generation、retriever、tool、agent、evaluator 和未知类型失败。
- [X] T012 在 `pkg/ai/obs/observation_type.go` 实现 observation type 枚举与验证；质量门控：必须通过 T011，错误信息必须可诊断。
- [X] T013 [P] 在 `pkg/ai/obs/safe_summary_test.go` 编写安全摘要测试；质量门控：先写失败测试，覆盖 hash、length、category、count、score、status、error_class，且敏感原文不得进入摘要。
- [X] T014 在 `pkg/ai/obs/safe_summary.go` 实现 SafeSummary helper；质量门控：必须通过 T013，输入不可变，禁止保留原始字符串。
- [X] T015 [P] 在 `pkg/ai/obs/contract_record_test.go` 扩展契约记录测试；质量门控：先写失败测试，新增 request_id、service_trace_id、span_id、observation_type、failure_status、safe summaries 字段断言。
- [X] T016 在 `pkg/ai/obs/tracer_contract_test.go` 扩展 TracerContractRecord 与断言；质量门控：必须通过 T015，并保持现有 logger contract 兼容。
- [X] T017 [P] 在 `pkg/ai/obs/testutil/span_sink_test.go` 编写内存 span sink 测试；质量门控：先写失败测试，覆盖并发写入、顺序读取、防御性拷贝和 raw payload 隐私扫描。
- [X] T018 在 `pkg/ai/obs/testutil/span_sink.go` 实现内存 span sink；质量门控：必须通过 T017，`go test -race ./pkg/ai/obs/testutil` 不得出现数据竞争。
- [X] T019 [P] 在 `pkg/ai/obs/export_failure_test.go` 编写上报失败保护测试；质量门控：先写失败测试，验证 exporter 失败不会导致 `Tracer.Record` panic 或返回业务错误。
- [X] T020 在 `pkg/ai/obs/export_failure.go` 实现 telemetry export failure 状态 helper；质量门控：必须通过 T019，失败状态必须稳定为 `telemetry_export_failed`。

## Phase 3：US1 - 基础设施观测平面（优先级：P1）

**用户故事目标**：让框架开发者或学习者可以观察普通服务请求在 GoFrame/HTTP 层的服务入口、资源身份、上下文传播、基础 span、错误状态、shutdown 和上报失败降级。

**独立测试标准**：运行基础设施离线 smoke 后，给定 `request_id` 可以定位 HTTP/service 入口记录、service_trace_id、span_id、method/path/status/duration/outcome，并证明默认配置不访问真实平台。

### Tests

- [X] T021 [P] [US1] 在 `internal/cmd/observability_config_test.go` 编写应用观测配置测试；质量门控：先写失败测试，覆盖默认关闭、启用本地 sink、真实平台未配置时不访问外部服务。
- [X] T022 [P] [US1] 在 `internal/cmd/observability_resource_test.go` 编写 OTel resource 语义测试；质量门控：先写失败测试，覆盖 service.name、deployment.environment 和版本/实例可选字段。
- [X] T023 [P] [US1] 在 `internal/cmd/observability_lifecycle_test.go` 编写 TracerProvider 生命周期测试；质量门控：先写失败测试，覆盖初始化幂等、shutdown 幂等和 exporter 失败保护。
- [X] T024 [P] [US1] 在 `internal/eval/smoke/infrastructure_span_test.go` 编写 HTTP/service span 离线 smoke 测试；质量门控：先写失败测试，覆盖 method、path、status、duration、request_id、service_trace_id、span_id。
- [X] T025 [P] [US1] 在 `internal/eval/smoke/context_propagation_test.go` 编写 handler-to-core 上下文传播测试；质量门控：先写失败测试，验证 request_id、service_trace_id、span_id 能从应用层传入 `pkg/ai` 调用边界。
- [X] T026 [P] [US1] 在 `internal/eval/smoke/infrastructure_export_failure_test.go` 编写基础平面上报失败 smoke 测试；质量门控：先写失败测试，验证基础设施 exporter 失败不影响业务结果。

### Implementation

- [X] T027 [US1] 在 `internal/cmd/observability.go` 实现 GoFrame/OTel 观测初始化与本地/no-op sink 装配；质量门控：必须通过 T021，不得要求真实平台配置。
- [X] T028 [US1] 在 `internal/cmd/observability_resource.go` 实现 service resource 构造；质量门控：必须通过 T022，serviceName 与 environment 必须来自配置且有安全默认值。
- [X] T029 [US1] 在 `internal/cmd/observability_lifecycle.go` 实现初始化与 shutdown 生命周期封装；质量门控：必须通过 T023，重复 shutdown 不得 panic。
- [X] T030 [US1] 在 `internal/eval/smoke/infrastructure_span.go` 实现基础设施 span 离线 smoke；质量门控：必须通过 T024，默认不依赖真实平台。
- [X] T031 [US1] 在 `internal/eval/smoke/context_propagation.go` 实现上下文传播验证 helper；质量门控：必须通过 T025，只传播低敏关联字段。
- [X] T032 [US1] 在 `internal/eval/smoke/infrastructure_export_failure.go` 实现基础平面上报失败验证；质量门控：必须通过 T026，失败状态必须可诊断。
- [X] T033 [US1] 在 `specs/002-dual-plane-observability/quickstart.md` 更新基础设施平面离线验证步骤；质量门控：必须说明默认验证与真实平台 opt-in 的区别。

## Phase 4：US1/US2 - 双平面关联层（优先级：P1）

**用户故事目标**：把基础设施 span、AI 语义 trace、Agent 子步骤和 eval evidence 串成一次请求的完整事实链，避免两个观测平面各自孤立。

**独立测试标准**：给定 `request_id` 能查到 HTTP/service 入口、AI generation、retriever/tool/agent/evaluator 记录和最终 outcome；给定 `ai_trace_id` 能回到 service_trace_id/span_id；给定 `eval_run_id + sample_id` 能回到 request 与 AI 阶段。

### Tests

- [X] T034 [P] [US1] 在 `internal/eval/smoke/observability_chain_test.go` 编写完整请求链路 smoke 测试；质量门控：先写失败测试，覆盖 success、upstream_failure、retrieval_miss、tool_error、loop_detected、budget_exceeded、degraded 七类 outcome。
- [X] T035 [P] [US1] 在 `pkg/ai/obs/chain_recorder_test.go` 编写 RequestObservationChain 组装测试；质量门控：先写失败测试，验证阶段顺序、parent 关系、request_id 回查和 outcome 解释。
- [X] T036 [P] [US1] 在 `pkg/ai/obs/otel_mapper_test.go` 编写 `obs.Trace` 到 span 快照映射测试；质量门控：先写失败测试，覆盖 generation/retriever/tool/agent/evaluator 类型和敏感字段缺失。
- [X] T037 [P] [US1] 在 `pkg/ai/obs/dual_plane_link_test.go` 编写双平面 link 规则测试；质量门控：先写失败测试，覆盖 HTTP parent span、AI child/link span、eval link 和缺失身份失败。
- [X] T038 [P] [US2] 在 `internal/eval/smoke/eval_trace_link_test.go` 编写 eval-to-trace smoke 测试；质量门控：先写失败测试，覆盖 90% 回链率和失败样例定位。

### Implementation

- [ ] T039 [US1] 在 `pkg/ai/obs/chain_recorder.go` 实现 RequestObservationChain 记录器；质量门控：必须通过 T035，不得记录原始 query、prompt 或 tool args。
- [ ] T040 [US1] 在 `pkg/ai/obs/otel_mapper.go` 实现 AI trace 到 span 快照的纯映射；质量门控：必须通过 T036，核心逻辑不得依赖真实 exporter。
- [ ] T041 [US1] 在 `pkg/ai/obs/otel_tracer.go` 实现 OTel-style tracer adapter 壳层；质量门控：必须复用 T016 contract，不得把 OTel 类型暴露到 `obs.Tracer` 接口。
- [ ] T042 [US1] 在 `pkg/ai/obs/dual_plane_link.go` 实现双平面 parent/link 规则；质量门控：必须通过 T037，缺失关键身份时必须返回可诊断错误。
- [ ] T043 [US1] 在 `internal/eval/smoke/observability_chain.go` 实现离线完整请求链路 smoke；质量门控：必须通过 T034，默认不依赖真实平台。
- [ ] T044 [US2] 在 `internal/eval/smoke/p0.go` 为 P0 smoke 注入 request_id、service_trace_id、span_id 与 ai_trace_id；质量门控：默认 smoke 仍不依赖真实模型服务。
- [ ] T045 [US2] 在 `internal/eval/smoke/eval_trace_link.go` 实现 eval-to-trace smoke 组合；质量门控：必须通过 T038，低于 90% 回链率时失败并列出缺失样例。
- [ ] T046 [US1] 在 `cmd/eval-smoke/main.go` 增加 observability chain smoke 命令入口；质量门控：命令默认离线运行，失败时返回非零退出码并打印报告路径或失败原因。
- [ ] T047 [US1] 在 `specs/002-dual-plane-observability/quickstart.md` 更新双平面关联验证命令；质量门控：必须说明 request_id、service_trace_id、span_id、ai_trace_id、eval_run_id 的查询关系。

## Phase 5：US1/US2 - AI 语义观测平面（优先级：P1）

**用户故事目标**：让 AI generation、retrieval、tool、Agent step、failure/degraded outcome 和 evaluator evidence 都形成稳定、低敏、可评估的语义记录，并与关联层对齐。

**独立测试标准**：运行离线 smoke 后，AI 语义平面至少包含 generation、retriever/tool/agent/evaluator 中三类记录，且每条记录都有 observation_type、ai_trace_id、safe summary、outcome 和 latency/usage 摘要。

### Tests

- [ ] T048 [P] [US1] 在 `pkg/ai/rag/retriever_observation_test.go` 编写检索观测接入测试；质量门控：先写失败测试，覆盖 retrieval count、miss status、score summary、latency 和 ai_trace_id。
- [ ] T049 [P] [US1] 在 `pkg/ai/agent/executor_observation_test.go` 编写 Agent step 观测接入测试；质量门控：先写失败测试，覆盖 step index、tool call id、termination reason、loop/budget 状态。
- [ ] T050 [P] [US1] 在 `pkg/ai/resilience/provider_wrapper_observation_test.go` 编写 provider wrapper outcome 观测测试；质量门控：先写失败测试，覆盖 upstream_failure、degraded、rate_limited 和 4xx 不熔断。
- [ ] T051 [P] [US2] 在 `pkg/ai/eval/evidence_test.go` 编写 EvaluationEvidence 测试；质量门控：先写失败测试，覆盖必填字段、阈值、regression_status 和 missing trace link。
- [ ] T052 [P] [US2] 在 `pkg/ai/eval/runner_trace_link_test.go` 编写 runner 回链测试；质量门控：先写失败测试，验证单样例 prediction、metric 和 trace identity 正确进入 report。
- [ ] T053 [P] [US2] 在 `internal/eval/smoke/agent_golden_test.go` 增加 Agent eval trace link 断言；质量门控：先写失败测试，保持成功、工具错误、自我纠错或步数限制样例可诊断。

### Implementation

- [ ] T054 [US1] 在 `pkg/ai/rag/retriever.go` 补充检索阶段观测摘要接入点；质量门控：必须通过 T048，新增逻辑不得改变检索结果语义。
- [ ] T055 [US1] 在 `pkg/ai/agent/executor.go` 补充 Agent step 与 tool 调用观测摘要接入点；质量门控：必须通过 T049，不得解析旧式文本 action。
- [ ] T056 [US1] 在 `pkg/ai/resilience/provider_wrapper.go` 补充 degraded/failure outcome 观测接入点；质量门控：必须通过 T050，4xx 仍不得触发上游熔断。
- [ ] T057 [US2] 在 `pkg/ai/eval/evidence.go` 实现 EvaluationEvidence 类型与验证；质量门控：必须通过 T051，单样例错误不得静默吞掉。
- [ ] T058 [US2] 在 `pkg/ai/eval/runner.go` 扩展 Report 以携带 trace identity 与 evidence；质量门控：必须通过 T052，并保持现有 eval runner 测试兼容。
- [ ] T059 [US2] 在 `cmd/eval-smoke/main.go` 输出 eval evidence 摘要；质量门控：输出不得包含原始 prompt 或用户输入，只显示 sample、metric、score 和 trace identity。
- [ ] T060 [US2] 在 `specs/002-dual-plane-observability/quickstart.md` 更新 eval evidence 验证命令；质量门控：命令必须与实现入口一致。

## Phase 6：US3 - 保护敏感内容边界（优先级：P1）

**用户故事目标**：确保普通观测记录只保留诊断摘要，不泄露原始用户输入、完整 prompt、完整工具参数、密钥、认证 token、个人隐私或外部响应原文，并且 baggage 只传播低敏关联字段。

**独立测试标准**：构造包含敏感输入、工具参数和密钥的请求后，logger、span sink、OTel mapper、baggage 和 smoke payload 的敏感原文命中数为 0。

### Tests

- [ ] T061 [P] [US3] 在 `pkg/ai/obs/privacy_contract_test.go` 编写跨 adapter 隐私契约测试；质量门控：先写失败测试，扫描 logger、span sink、OTel mapper raw payload。
- [ ] T062 [P] [US3] 在 `pkg/ai/obs/redaction_test.go` 编写敏感字段拒绝测试；质量门控：先写失败测试，覆盖 raw_query、prompt_content、tool_args、api_key、jwt、password、external_response。
- [ ] T063 [P] [US3] 在 `pkg/ai/obs/baggage_privacy_test.go` 编写 baggage 隐私回归测试；质量门控：先写失败测试，覆盖 query、prompt、tool args、token、PII 禁止进入 baggage。
- [ ] T064 [P] [US3] 在 `internal/eval/smoke/observability_privacy_test.go` 编写端到端隐私 smoke 测试；质量门控：先写失败测试，构造敏感 query、prompt、tool 参数与外部响应原文。

### Implementation

- [ ] T065 [US3] 在 `pkg/ai/obs/redaction.go` 实现 forbidden key/value 扫描 helper；质量门控：必须通过 T062，扫描结果不得修改输入对象。
- [ ] T066 [US3] 在 `pkg/ai/obs/logger.go` 接入安全字段白名单；质量门控：必须通过 T061，现有 logger contract 字段保持稳定。
- [ ] T067 [US3] 在 `pkg/ai/obs/otel_mapper.go` 接入安全字段白名单；质量门控：必须通过 T061，OTel mapper 不得输出敏感原文。
- [ ] T068 [US3] 在 `pkg/ai/obs/baggage_policy.go` 补充 baggage 隐私拒绝逻辑；质量门控：必须通过 T063，不得改变 T010 定义的允许字段语义。
- [ ] T069 [US3] 在 `internal/eval/smoke/observability_privacy.go` 实现隐私 smoke 组合；质量门控：必须通过 T064，泄露命中数必须为 0。
- [ ] T070 [US3] 在 `docs/adr/0007-dual-plane-observability-evaluation-v1.md` 补充隐私实现回顾段落；质量门控：只记录边界与取舍，不记录任何敏感样例原文。
- [ ] T071 [US3] 在 `docs/journal/0005-observability-privacy-boundary.md` 记录隐私边界学习日志；质量门控：必须包含理论误区、工程根因、修复方式、后续预防。

## Phase 7：US4 - 系统学习观测与评估体系（优先级：P2）

**用户故事目标**：把理论学习、工程实验、最佳实践和复盘问题与实现任务绑定，使学习者能系统理解基础设施观测平面、AI 语义观测平面、双平面关联、隐私边界和评估证据关联。

**独立测试标准**：至少 4 个主要工程切片具备“理论概念 -> 工程实验 -> 最佳实践 -> 复盘问题”的学习链路，并互相引用到相关任务、测试或 ADR。

### Documentation Tasks

- [ ] T072 [P] [US4] 在 `docs/observability/01-observability-foundations.md` 编写系统可观测性基础学习资产；质量门控：必须覆盖 logs/metrics/traces、SLO、故障诊断价值和本项目基础设施平面工程落点。
- [ ] T073 [P] [US4] 在 `docs/observability/02-distributed-tracing.md` 编写分布式追踪学习资产；质量门控：必须覆盖 trace/span/context/baggage、传播边界、常见误用和本项目双平面关联身份设计。
- [ ] T074 [P] [US4] 在 `docs/observability/03-ai-agent-observability.md` 编写 AI Agent 可观测性学习资产；质量门控：必须覆盖 generation、retriever、tool、agent step、token/cost、loop 和 budget 诊断。
- [ ] T075 [P] [US4] 在 `docs/observability/04-evaluation-evidence.md` 编写评估证据关联学习资产；质量门控：必须覆盖 dataset/sample/metric/score、回链、回归判断和本项目 eval smoke。
- [ ] T076 [P] [US4] 在 `docs/observability/05-privacy-boundaries.md` 编写隐私边界学习资产；质量门控：必须覆盖敏感原文、hash/summary、baggage 风险、审计链路边界。
- [ ] T077 [P] [US4] 在 `docs/observability/06-platform-integration-tradeoffs.md` 编写平台接入取舍学习资产；质量门控：必须覆盖默认离线验证、真实平台 opt-in smoke、adapter 边界和 SDK 污染风险。
- [ ] T078 [P] [US4] 在 `docs/observability/07-dual-plane-correlation.md` 编写双平面关联学习资产；质量门控：必须覆盖基础 span、AI observation、eval evidence 的 parent/link/baggage 规则。
- [ ] T079 [US4] 在 `docs/observability/README.md` 更新学习路径顺序；质量门控：必须能从概念阅读顺序跳转到对应工程切片和测试证据。
- [ ] T080 [US4] 在 `docs/journal/0006-observability-v1-learning-map.md` 记录 Observability v1 学习地图；质量门控：必须包含基础设施平面、AI 语义平面、双平面关联、隐私边界、评估证据和平台接入六个核心主题。
- [ ] T081 [US4] 在 `specs/002-dual-plane-observability/tasks.md` 为学习资产任务补充实现进度维护说明；质量门控：不得改变任务状态，必须说明学习资产如何随实现同步更新。

## Phase 8：Platform Smoke & Configuration

**目标**：提供真实平台 opt-in smoke 的配置与命令边界，验证平台能接收最小生产观测切片，但不进入默认门禁。

**独立验收**：缺少配置时 smoke 明确跳过或报出可读错误；配置齐备时发送一条包含基础链路、AI generation、retriever 或 tool、eval 摘要的示例链路。

- [ ] T082 [P] 在 `internal/eval/smoke/platform_config_test.go` 编写真实平台 smoke 配置测试；质量门控：先写失败测试，覆盖缺少 endpoint、缺少凭据、未启用开关三种情况。
- [ ] T083 在 `internal/eval/smoke/platform_config.go` 实现真实平台 smoke 配置读取；质量门控：必须通过 T082，不得打印 secret 值。
- [ ] T084 [P] 在 `internal/eval/smoke/platform_smoke_test.go` 编写真平台 opt-in smoke 测试骨架；质量门控：先写失败测试或 skip 逻辑，默认环境必须跳过不失败。
- [ ] T085 在 `internal/eval/smoke/platform_smoke.go` 实现真实平台最小链路发送逻辑；质量门控：必须通过 T084，真实上报失败不得影响默认测试。
- [ ] T086 在 `manifest/config/config.yaml` 补充真实平台 smoke 配置示例注释；质量门控：不得包含真实密钥，必须标明默认关闭。
- [ ] T087 在 `specs/002-dual-plane-observability/quickstart.md` 更新真实平台 opt-in smoke 操作步骤；质量门控：必须提醒不要把密钥写入源码或聊天记录。

## Final Phase：Polish & Cross-Cutting Concerns

**目标**：收口验证命令、任务状态源、文档导航、ADR/journal 和质量门禁，确保后续实现者可以按任务顺序独立推进。

**独立验收**：默认测试命令通过；`tasks.md`、quickstart、AGENTS、docs/observability 索引一致；关键风险都有 ADR 或 journal 入口。

- [ ] T088 [P] 在 `AGENTS.md` 更新 SPECKIT 区任务源为 `specs/002-dual-plane-observability/tasks.md`；质量门控：保留 `<!-- SPECKIT START -->` 与 `<!-- SPECKIT END -->` 标记。
- [ ] T089 [P] 在 `docs/ROADMAP.md` 增加 Observability v1 当前路线说明；质量门控：不得改写旧 P0/P1 历史，只补充当前 spec-kit 导航。
- [ ] T090 在 `Makefile` 确认 `test`、`test-race`、`vet`、`eval-smoke`、`obs-smoke` 命令可运行；质量门控：任一默认命令失败必须阻止完成，真实平台 smoke 不得默认执行。
- [ ] T091 在 `specs/002-dual-plane-observability/checklists/requirements.md` 追加任务拆分审查结果；质量门控：必须检查任务独立性、TDD、基础设施平面、双平面关联、默认离线验证和学习资产覆盖。
- [ ] T092 在 `docs/journal/0007-observability-v1-task-planning.md` 记录任务拆解复盘；质量门控：必须说明为什么按基础设施平面、AI 语义平面、双平面关联、评估回链、隐私边界、学习资产拆分。
- [ ] T093 在 `specs/002-dual-plane-observability/contracts/observability-v1-contract.md` 补充基础设施平面与双平面关联验收摘要；质量门控：不得引入平台专属 schema，必须保持契约可离线验证。
- [ ] T094 运行 `go test ./...` 并在 `specs/002-dual-plane-observability/quickstart.md` 记录最终默认验证结果；质量门控：必须使用真实命令输出或明确标记示意，失败不得标记完成。

## 依赖关系

```text
Phase 1 Setup
  -> Phase 2 Foundational Correlation & Contracts
    -> Phase 3 US1 基础设施观测平面
      -> Phase 4 US1/US2 双平面关联层
        -> Phase 5 US1/US2 AI 语义观测平面
          -> Phase 6 US3 隐私边界
          -> Phase 7 US4 学习资产
        -> Phase 8 Platform Smoke & Configuration
      -> Final Polish
```

关键依赖：

- T007-T020 是所有代码故事的共同基础，尤其是 request/span/AI/eval 关联身份和 baggage 白名单。
- Phase 3 是基础设施平面 MVP：没有 HTTP/service span、resource、lifecycle 和 context propagation，就不能证明“双平面”里的基础平面真实存在。
- Phase 4 是双平面关联层 MVP：没有 parent/link 规则，AI 语义记录和基础 span 会变成两套孤立证据。
- Phase 5 的 AI 语义观测依赖 Phase 4 的关联身份与 mapper，但 RAG、Agent、resilience、eval 的局部测试可在对应契约稳定后并行推进。
- Phase 6 隐私边界依赖前面所有 payload 形态，最终 smoke 必须扫描 logger、span sink、mapper、baggage 和端到端输出。
- Phase 7 学习资产可与代码任务并行，但 final polish 前必须链接到实际工程切片或测试证据。
- Phase 8 平台 smoke 依赖基础设施平面、双平面关联和 AI 语义平面，但不阻塞默认离线 MVP。

## 并行执行机会

### Setup 并行

```text
T001, T002, T003, T004 可并行
T005, T006 依赖命令入口与 quickstart 文案对齐，建议串行
```

### Foundational 并行

```text
T007, T009, T011, T013, T015, T017, T019 可并行编写失败测试
T008, T010, T012, T014, T016, T018, T020 在对应测试完成后并行实现
```

### 基础设施观测平面并行

```text
T021, T022, T023, T024, T025, T026 可并行编写失败测试
T027, T028, T029 可在 internal/cmd 测试完成后并行
T030, T031, T032 可在 smoke 测试完成后并行
```

### 双平面关联层并行

```text
T034, T035, T036, T037, T038 可并行编写失败测试
T039, T040, T041, T042 可在 pkg/ai/obs 契约稳定后并行
T043, T044, T045, T046, T047 可在链路 smoke 和 eval 回链字段稳定后推进
```

### AI 语义观测平面并行

```text
T048, T049, T050, T051, T052, T053 可并行编写失败测试
T054, T055, T056 可并行接入 RAG、Agent、resilience 观测点
T057, T058, T059, T060 可在 eval evidence 字段稳定后推进
```

### 隐私边界并行

```text
T061, T062, T063, T064 可并行编写隐私测试
T066, T067, T068 可在 T065 redaction helper 完成后并行接入 logger、mapper、baggage
T070, T071 可与隐私实现并行沉淀 ADR 和 journal
```

### 学习资产并行

```text
T072, T073, T074, T075, T076, T077, T078 可完全并行
T079, T080, T081 依赖学习资产初稿和任务结构稳定
```

### 平台 smoke 并行

```text
T082, T084 可并行编写配置与平台 smoke 测试
T083, T085 在对应测试完成后实现
T086, T087 与真实平台 smoke 实现并行更新文档和配置注释
```

## 实施策略

### MVP First

MVP 范围为 Phase 1、Phase 2、Phase 3 和 Phase 4：

1. 建立目录、配置占位和命令入口。
2. 完成关联身份、baggage 白名单、observation type、safe summary、contract sink 和 export failure helper。
3. 完成基础设施观测平面：GoFrame/OTel 初始化、resource、lifecycle、HTTP/service span、context propagation 和上报失败降级。
4. 完成双平面关联层：request_id、service_trace_id、span_id、ai_trace_id、eval_run_id 的 parent/link 规则和离线完整链路 smoke。

MVP 完成后即可证明 SC-001、SC-002 的核心路径，并明确展示“基础设施观测平面 + AI 语义观测平面 + 关联层”的最小闭环。

### Incremental Delivery

1. **Correlation Foundation**：先固定 request/span/AI/eval 身份模型和 baggage 边界。
2. **Infrastructure Plane**：让普通 HTTP/service 请求具备可离线验证的基础 span。
3. **Dual-Plane Correlation**：把基础 span、AI trace 和 eval evidence 串成完整事实链。
4. **AI Semantic Plane**：补齐 generation、retriever、tool、agent、evaluator 的语义摘要和 outcome。
5. **Privacy Hardening**：所有普通观测 payload 与 baggage 的敏感原文命中数为 0。
6. **Learning Track**：每个主要切片都有理论、实验、最佳实践和复盘问题。
7. **Platform Smoke**：真实平台 opt-in 验证最小生产观测切片。
8. **Polish**：更新导航、任务状态源和最终验证结果。

### Validation Strategy

- 每个代码任务先写失败测试。
- 每个 story phase 必须能独立运行对应测试命令；基础设施平面和双平面关联层必须有独立 smoke。
- 默认验证不访问真实平台。
- 涉及敏感字段的任务必须包含隐私断言。
- 涉及跨服务传播的任务必须显式验证 baggage 白名单，不得传播原始 query、prompt、tool args、token 或 PII。
- 文档任务必须和实际任务、测试或 ADR 互相链接，避免学习资产与工程实现分叉。
