# Observability v1 任务拆解复盘

**日期**：2026-07-08
**关联任务**：T001-T094，T092
**关联模块**：specs/002-dual-plane-observability、docs/observability、pkg/ai/obs、pkg/ai/eval、internal/eval/smoke、internal/cmd
**状态**：已复盘

## 发生了什么

Observability v1 的任务拆解没有按代码包或外部平台能力直接展开，而是按生产级观测与评估体系的事实链路拆分：

1. 先建立两个平面共享的关联身份和安全字段契约。
2. 再让基础设施观测平面独立成立。
3. 然后实现双平面关联层，把服务事实、AI 语义事实和 eval evidence 串成完整请求链。
4. 随后补齐 AI 语义观测平面的 generation、retriever、tool、agent 和 evaluator 事实。
5. 再统一收紧隐私边界，避免观测体系变成敏感数据出口。
6. 同步沉淀学习资产，让理论概念、工程实验、最佳实践和复盘问题能随着实现一起演进。
7. 最后把真实平台 smoke 设计成 opt-in 出口，防止默认验证依赖外部平台。

这次拆分服务的是 Longtermism 的项目北极星：模型、工具和 Agent 范式会变化，但可观测事实、可评估证据和可回归改进是长期稳定的工程基础。

## 拆分理由

### 基础设施平面

基础设施平面负责传统服务事实，例如 HTTP/service span、resource、lifecycle、context propagation、exporter failure 和默认配置边界。它回答的是“请求在服务系统中发生了什么”。

我们单独拆出 Phase 3，而不是把它混在 AI trace 里，是因为基础设施事实和 AI 语义事实有不同的职责、字段和失败模式。HTTP 状态码、服务延迟、shutdown、exporter 失败保护这些问题不应被 LLM generation 或 tool call 语义污染。

对应任务：

- T021-T026 先写配置、resource、lifecycle、service span、context propagation 和 exporter failure 测试。
- T027-T033 再实现基础设施平面和 quickstart 验证。

### AI 语义平面

AI 语义平面负责 generation、retriever、tool、agent step、evaluator、token、cost、loop、budget 和 failure/degraded outcome。它回答的是“模型和 Agent 决策过程发生了什么”。

我们把它放在 Phase 5，而不是最先做，是因为 AI 语义记录必须依赖前面的 observation type、correlation identity、safe summary 和双平面 link。否则 AI observation 只能成为孤立日志，无法回到请求入口或评估证据。

对应任务：

- T048-T053 先覆盖 RAG、Agent、provider wrapper、EvaluationEvidence 和 runner 回链测试。
- T054-T060 再把观测摘要接入 retriever、executor、resilience、eval runner 和 smoke 输出。

### 双平面关联

双平面关联层是 Observability v1 的主骨架。它负责用 `request_id`、`service_trace_id`、`span_id`、`ai_trace_id`、`session_id` 和 `eval_run_id` 把基础设施平面、AI 语义平面和 eval evidence 串起来。

如果没有这一层，基础设施观测和 AI 观测都会“各自正确”，但工程师仍然无法回答一次用户请求到底发生了什么、AI 阶段挂在哪儿、失败样例能否回到服务 span。

对应任务：

- T007-T020 先建立 correlation、baggage、observation type、safe summary、span sink 和 export failure 等基础契约。
- T034-T038 先写完整请求链、chain recorder、OTel mapper、dual-plane link 和 eval-to-trace 测试。
- T039-T047 再实现 chain recorder、mapper、tracer adapter、link 规则、离线完整链路 smoke 和命令入口。

### 评估回链

评估回链负责把 dataset、sample、metric、score 和 regression status 从“孤立分数”变成可回查证据。它回答的是“这次能力表现是否退化、退化来自哪个样例、能否定位到对应请求与 AI 阶段”。

我们没有把 eval evidence 当作评估模块内部细节处理，而是让它进入双平面链路，是因为 Longtermism 的核心不是只看 trace，也不是只看 score，而是让 trace 和 score 能互相解释。

对应任务：

- T038 和 T045 验证 eval-to-trace 90% 回链率和失败样例定位。
- T051、T052、T057、T058 定义并生成 EvaluationEvidence。
- T059、T060 约束命令输出只显示 sample、metric、score 和 trace identity。

### 隐私边界

隐私边界负责定义什么不能进入普通观测面。raw query、完整 prompt、tool args、外部响应、密钥、JWT、密码和个人隐私不能出现在 logger、span sink、OTel mapper、baggage 或 smoke result 中。

我们把隐私边界作为独立 Phase 6，而不是分散到每个模块，是因为观测体系的风险主要发生在“出口”。即使领域对象没有 raw 字段，adapter、mapper、测试报告或 baggage 也可能把敏感值带出去。

对应任务：

- T061-T064 先写跨 adapter 隐私契约、redaction、baggage privacy 和端到端 privacy smoke。
- T065-T069 再统一实现 forbidden key/value 扫描并接入 logger、OTel mapper、baggage 和 smoke。
- T070-T071 把隐私实现回顾写回 ADR 和 journal。

### 学习资产

学习资产是当前阶段的内部工程原则。它负责把理论概念、工程实验、最佳实践、失败模式和复盘问题绑定到实际任务、测试和 ADR 上。

我们把它拆成 Phase 7，而不是在最后写一篇泛泛总结，是因为本项目仍然处于边做边学阶段。Observability v1 的学习价值必须跟随实现同步增长，否则文档会变成落后的概念说明，无法支撑后续复盘和面试表达。

对应任务：

- T072-T078 分别覆盖系统可观测性、分布式追踪、AI Agent 可观测性、评估证据、隐私边界、平台接入取舍和双平面关联。
- T079-T081 更新学习路径、学习地图和维护规则。

## 质量控制

这次任务拆解使用了几条约束来降低耦合：

- 每个代码阶段优先拆成测试任务和实现任务，保持 TDD 的 RED -> GREEN 路径。
- 每个任务尽量只聚焦一个文件、一个测试面、一个实现面或一个文档面。
- 基础设施平面和 AI 语义平面分别有独立验收，避免互相掩盖失败。
- 双平面关联层必须能通过离线 smoke 证明，不依赖真实 OTel collector、Langfuse 或模型服务。
- 平台 smoke 明确是 opt-in，不进入默认门禁。
- 学习资产必须能指向工程证据，不能只有概念描述。

## 学到什么

观测与评估体系不能按“我要接哪个平台”来拆，也不能按“哪个包还缺功能”来拆。更稳定的拆法是按事实链路拆：谁产生事实、事实属于哪个平面、如何关联、如何形成评估证据、如何避免泄露、如何被默认离线验证。

这也解释了为什么 T089 需要先把 ROADMAP 重新锚定到 Longtermism，T090 需要把 `obs-smoke` 从占位变成真实离线聚合入口，T091 需要补任务拆分审查。文档导航、命令入口和任务状态源都是观测与评估闭环的一部分。

## 后续预防

- 增加或调整测试：未来真实 Langfuse/OTel adapter 落地时，必须先补 app config 到 platform smoke 的桥接契约，不能只依赖 `internal/eval/smoke` runner。
- 增加或调整评估 case：后续 prompt version、model routing、RAG strategy、loop strategy 和 context compression 进入实验体系时，应沿用 dataset identity + evidence + trace identity 的回链模式。
- 增加或调整 trace 字段：新增字段必须明确归属基础设施平面、AI 语义平面、关联层或评估证据，不允许为了平台展示方便混入核心事实模型。
- 增加或调整降级路径：平台不可用、exporter 失败或配置缺失时，默认离线 smoke 和本地诊断必须继续可用。
- 需要补充的 ADR / ROADMAP / tasks：如果未来进入开源与生产阶段，应把“学习资产”从对外核心卖点收敛为“设计决策可解释、可追溯、可复盘”的工程治理原则。
