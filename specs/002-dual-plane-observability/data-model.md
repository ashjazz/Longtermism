# 数据模型：双平面观测与评估体系 v1

本文定义规格中的业务实体与验证规则。它描述“要追踪什么”和“如何保持可关联、可评估、可学习”，不绑定具体平台实现。

## Entity: RequestObservationChain（请求观测链路）

**说明**：一次用户请求下的完整可追溯记录，串联服务入口、AI 阶段、辅助步骤、评估摘要和最终 outcome。

**字段**：
- `request_id`：请求级稳定身份，必填。
- `service_trace_id`：基础链路追踪身份，必填。
- `root_ai_trace_id`：本请求下首个或主 AI trace 身份，必填。
- `session_id`：会话身份，可选。
- `user_id` / `tenant_id`：用户与租户身份，可选；跨服务传播时应使用低敏或 hash 后身份。
- `feature`：功能或能力名称，必填。
- `started_at` / `ended_at`：请求链路起止时间，必填。
- `outcome_status`：`success`、`failure`、`terminated`、`degraded` 之一，必填。
- `stage_refs`：关联的基础链路记录、AI 语义记录和评估证据引用，至少一条。

**验证规则**：
- 一个 `request_id` 可以关联多个 AI 语义记录。
- `outcome_status` 必须能由关联记录解释，不能只有自由文本。
- 不得包含原始用户输入、完整 prompt 或完整工具参数。

## Entity: ServiceObservation（基础链路记录）

**说明**：描述服务入口、跨组件调用、基础耗时和错误状态，用于定位传统服务问题。

**字段**：
- `service_trace_id`：基础链路追踪身份，必填。
- `span_id`：阶段身份，必填。
- `parent_span_id`：父阶段身份，可选。
- `request_id`：请求级关联身份，必填。
- `stage_name`：阶段名称，必填。
- `component`：入口、客户端、数据库、缓存、外部调用等类别，必填。
- `latency_ms`：耗时，必填。
- `status`：成功、错误或取消，必填。
- `error_class`：错误分类，可选。
- `safe_attributes`：低敏属性集合，可选。

**验证规则**：
- `request_id` 必须能回链到 RequestObservationChain。
- `safe_attributes` 不得携带敏感原文或凭据。

## Entity: AISemanticObservation（AI 语义记录）

**说明**：描述模型生成、检索、工具调用、Agent 执行和评估相关的 AI 特有阶段。

**字段**：
- `ai_trace_id`：AI 语义记录身份，必填。
- `request_id`：请求级关联身份，必填。
- `service_trace_id` / `parent_span_id`：基础链路关联身份，可选但推荐。
- `observation_type`：`generation`、`retriever`、`tool`、`agent`、`evaluator` 之一，必填。
- `feature`：能力名称，必填。
- `model` / `provider`：模型与供应商身份，generation 类型必填。
- `prompt_version` / `prompt_hash`：prompt 身份，generation 类型必填。
- `usage_summary`：输入、输出、推理和缓存 token 摘要，可选但推荐。
- `latency_summary`：TTFT、总耗时、检索耗时等摘要，可选但推荐。
- `cost_summary`：成本计算所需字段，可选。
- `retrieval_summary`：检索数量、分数摘要、query rewrite 身份、miss 状态，可选。
- `tool_summary`：工具名、调用身份、状态、参数摘要或参数 hash，可选。
- `agent_summary`：步骤序号、终止原因、循环检测、步数限制、预算限制，可选。
- `outcome_status`：`success`、`failure`、`terminated`、`degraded` 之一，必填。
- `failure_status`：timeout、rate_limited、retrieval_miss、tool_error、loop_detected、budget_exceeded 等稳定枚举，可选。

**验证规则**：
- `observation_type=generation` 必须包含模型身份和 prompt 身份。
- `observation_type=tool` 必须包含工具名和调用状态，但不得包含完整参数原文。
- `observation_type=retriever` 必须包含检索数量或 miss 状态。
- `observation_type=evaluator` 必须能关联 EvaluationEvidence。

## Entity: CorrelationIdentity（关联身份）

**说明**：用于把请求、服务链路、AI 语义记录、会话、用户和评估结果串联起来。

**字段**：
- `request_id`：请求级身份。
- `service_trace_id`：基础链路身份。
- `service_span_id`：当前服务阶段身份。
- `ai_trace_id`：AI 语义记录身份。
- `session_id`：会话身份。
- `user_id` / `tenant_id`：用户与租户身份。
- `eval_run_id`：评估运行身份。
- `eval_sample_id`：评估样例身份。
- `platform_observation_id`：真实平台观察记录身份，可选。

**验证规则**：
- 关联身份必须能从 eval 结果回链到 AI 语义记录和请求链路。
- 跨服务传播字段必须低敏；原始用户输入、prompt 和 tool args 不得作为关联身份传播。

## Entity: SafeSummary（安全摘要）

**说明**：不保存敏感原文的诊断字段集合。

**字段**：
- `hash`：内容身份，可选。
- `length`：内容长度，可选。
- `language` / `category`：语言或分类，可选。
- `count`：数量摘要，可选。
- `score_summary`：分数摘要，可选。
- `status`：状态摘要，可选。
- `error_class`：错误分类，可选。

**验证规则**：
- 可用于普通 trace、日志、评估报告和学习记录。
- 不得包含敏感原文、密钥、token、完整外部响应或个人隐私。

## Entity: EvaluationEvidence（评估证据）

**说明**：评估样例、指标、得分、阈值和对应请求记录之间的可追溯关系。

**字段**：
- `eval_run_id`：评估运行身份，必填。
- `dataset_name` / `dataset_version`：数据集身份，必填。
- `sample_id`：样例身份，必填。
- `metric_name`：指标名称，必填。
- `score`：得分，必填。
- `threshold`：阈值，可选。
- `regression_status`：passed、warning、failed，可选。
- `request_id` / `ai_trace_id`：回链身份，必填。
- `failure_summary`：失败摘要，可选。

**验证规则**：
- 至少 90% 的评估样例结果必须能回链到 `request_id` 和 `ai_trace_id`。
- 单样例错误不得静默吞掉，必须体现在失败摘要或报告中。

## Entity: LearningAsset（学习资产）

**说明**：围绕观测主题、工程切片或失败修复沉淀的学习材料。

**字段**：
- `topic`：学习主题，必填。
- `slice_id`：关联工程切片，可选。
- `concepts`：理论概念列表，必填。
- `standards_or_patterns`：标准语义或最佳实践，必填。
- `experiment`：工程实验说明，必填。
- `failure_modes`：失败模式，可选。
- `fallbacks`：降级路径，可选。
- `review_questions`：复盘问题，必填。
- `links`：关联 spec、plan、tasks、ADR、journal 或测试证据，可选。

**验证规则**：
- 至少 4 个主要工程切片必须配套学习资产。
- 学习资产必须能从概念回到工程切片，也能从工程切片回到概念复盘。

## State Transitions

### Observation outcome

```text
started -> success
started -> failure
started -> terminated
started -> degraded
failure -> degraded
```

### Evaluation evidence

```text
created -> linked -> reported
created -> unlinked -> reported_with_warning
```

### Learning asset

```text
planned -> drafted -> validated -> referenced
```
