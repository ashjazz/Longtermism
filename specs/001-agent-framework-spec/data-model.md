# 数据模型：生产级 AI Agent 框架

> 本文档描述规格和计划层面的核心实体。具体代码结构可在实现阶段映射到包、结构体、接口或配置文件。

## 实体：项目规格

**职责**：定义项目使命、目标用户、范围、成功标准和就绪规则。

**字段**

- `name`：规格名称。
- `feature_directory`：规格目录。
- `created_at`：创建日期。
- `status`：草稿、评审中、已接受。
- `user_stories`：用户故事列表。
- `functional_requirements`：功能需求列表。
- `success_criteria`：成功标准列表。
- `scope_boundaries`：范围内/范围外说明。
- `assumptions`：假设列表。

**验证规则**

- 必须至少包含一个 P1 用户故事。
- 必须包含可度量成功标准。
- 不允许存在未解决的澄清标记。

## 实体：能力阶段

**职责**：表达从 P0 到 P5 的阶段化建设路径。

**字段**

- `id`：如 P0、P1、P2。
- `name`：阶段名称。
- `objective`：阶段目标。
- `dependencies`：前置阶段或能力。
- `deliverables`：交付物。
- `acceptance_evidence`：验收证据。

**关系**

- 一个能力阶段包含多个 AI 能力。
- 一个能力阶段可以依赖其他能力阶段。

## 实体：AI 能力

**职责**：表达可交付的框架行为。

**字段**

- `id`：能力编号。
- `name`：能力名称。
- `phase`：所属阶段。
- `scope`：范围说明。
- `tests`：测试要求。
- `evaluation`：评估要求。
- `observability`：追踪要求。
- `failure_modes`：失败模式。
- `fallbacks`：降级行为。
- `docs`：相关文档。

**验证规则**

- 完成前必须具备测试、评估/回归、可观测性、失败模式、降级路径和文档。
- 涉及外部调用时必须说明错误分类和重试边界。

## 实体：模型供应商

**职责**：表达可替换的模型调用来源。

**字段**

- `name`：供应商标识。
- `capabilities`：支持的能力，如流式、工具调用、结构化输出、推理强度、视觉、缓存。
- `request`：模型请求。
- `response`：模型响应。
- `usage`：用量信息。
- `error_classification`：错误分类。

**状态转换**

- `available` -> `degraded`：出现限流、超时或部分失败。
- `degraded` -> `unavailable`：连续失败或熔断打开。
- `unavailable` -> `available`：健康检查或半开探测恢复。

## 实体：提示词资产

**职责**：表达可版本化、可渲染、可追踪的 prompt。

**字段**

- `name`：模板名称。
- `version`：模板版本。
- `variables`：渲染变量。
- `rendered_content`：渲染结果。
- `content_hash`：渲染结果 hash。

**验证规则**

- 缺失必需变量必须失败。
- 渲染结果必须可追踪到版本和 hash。

## 实体：追踪记录

**职责**：表达一次 AI 交互的可观测证据。

**字段**

- `trace_id`：追踪 ID。
- `feature`：功能名称。
- `model`：模型身份。
- `prompt_version`：提示词版本。
- `prompt_hash`：提示词 hash。
- `input_tokens` / `output_tokens` / `reasoning_tokens`：用量。
- `latency_ms` / `ttft_ms`：延迟。
- `retrieval_summary`：检索摘要。
- `cost_ready_fields`：成本计算预留字段。
- `outcome_status`：成功、失败、降级或终止原因。

**验证规则**

- 普通 trace 不保存原始敏感内容。
- 失败和降级必须有可诊断状态。

## 实体：评估样例

**职责**：表达可重复评估的单条 case。

**字段**

- `id`：样例 ID。
- `query`：输入。
- `ground_truth`：期望答案或判断依据。
- `relevant_context`：相关上下文。
- `difficulty`：难度。
- `category`：类别。
- `metadata`：扩展信息。

**验证规则**

- 必须能被 runner 加载。
- 至少包含一种可判定的期望结果。

## 实体：评估报告

**职责**：表达一次评估运行结果。

**字段**

- `dataset_version`：数据集版本。
- `sample_count`：样本数。
- `scores`：指标得分。
- `regressions`：回归项。
- `status`：通过、警告、失败。

**验证规则**

- 指标名称必须稳定。
- 回归判断必须可重复。

## 实体：工具契约

**职责**：表达 Agent 可调用动作的能力边界。

**字段**

- `name`：工具名。
- `description`：用途说明。
- `input_schema`：输入约束。
- `output_contract`：输出约束。
- `safety_class`：安全等级。
- `error_behavior`：错误返回方式。

**验证规则**

- 工具错误必须对模型或调用方有用。
- 敏感操作必须声明确认或权限要求。

## 实体：降级策略

**职责**：表达正常路径不可用时的用户可见或调用方可见行为。

**字段**

- `trigger`：触发条件。
- `fallback_path`：降级路径。
- `user_visible_result`：可见结果。
- `trace_marker`：追踪标记。
- `evaluation_impact`：评估影响。

## 实体：决策记录

**职责**：记录架构选择或暂缓决策。

**字段**

- `title`：决策标题。
- `status`：提议、接受、暂缓、废弃。
- `context`：背景。
- `decision`：决策。
- `alternatives`：备选方案。
- `consequences`：影响。
- `revisit_condition`：重新审视条件。
