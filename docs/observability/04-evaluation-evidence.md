# 评估证据关联

**关联任务**：T075
**关联规格**：`specs/002-dual-plane-observability/spec.md`
**状态**：drafted

## 理论概念

评估体系的核心不是“跑出一个平均分”，而是能回答：哪个数据集、哪个样例、哪个指标、哪次请求、哪条 AI trace 产生了这个分数，以及它是否构成回归。

Observability v1 把单条评估事实建模为 **evidence**。一条 evidence 至少包含：

- **Dataset**：评估数据集身份。它由 `dataset_name + dataset_version` 共同构成，单独的 version 不能唯一说明评估来源。
- **Sample**：数据集中的单个样例，例如某个 Agent tool loop、RAG retrieval miss 或 provider failover case。
- **Metric**：评价维度，例如 exact match、answer relevance、tool trajectory correctness 或 retrieval quality。
- **Score**：该样例在该指标上的得分。v1 采用 `[0,1]` 区间，方便阈值和聚合。
- **Threshold**：该指标的门禁阈值。没有阈值时不能断言通过，只能进入 warning 或人工复核。
- **Regression status**：当前分数相对阈值的判定，例如 passed、warning、failed。
- **Trace link**：`eval_run_id`、`request_id`、`ai_trace_id`、`service_trace_id`、`span_id`，用于从评估结果回到产生输出的请求链路。

evidence 的价值在于把“模型表现变差了”转换成可定位事实：不是只知道平均分下降，而是知道哪个 sample、哪个 metric、哪次请求、哪个 AI 阶段出现了问题。

## 关键问题

- 这个主题解决什么生产问题？
  它解决“评估报告显示分数下降，但无法定位失败样例、请求链路、AI observation 或具体回归原因”的问题。

- 它在传统后端观测和 AI Agent 观测中有什么差异？
  传统后端通常用错误率、延迟和可用性判断质量；AI Agent 还需要用 golden dataset、metric、score、trajectory 和人工/自动评估来判断“答得是否正确”。因此 eval evidence 必须和 trace 关联，否则评估只是一张离线表。

- 哪些信息必须可见，哪些信息必须隐藏？
  必须可见的是 dataset identity、sample id、metric name、score、threshold、regression status、eval run id、request id、AI trace id 和 service span identity。必须隐藏的是样例原始 query、完整 prompt、模型完整回答、工具完整参数和外部响应原文。

## 工程实验

本项目用三层实现 eval evidence：

1. `pkg/ai/eval/dataset_identity.go` 定义 `DatasetIdentity`，把 dataset name 和 version 建模成一个值对象，防止只靠 version 区分多套评估数据集。
2. `pkg/ai/eval/evidence.go` 定义 `EvaluationEvidence`，负责校验 dataset/sample/metric/score/threshold 和 trace link，并给出 regression status。
3. `pkg/ai/eval/runner.go` 在 `LocalRunner` 中把 prediction 的 `TraceIdentity` 转换成 evidence，输出到 `Report.Evidence`。
4. `internal/eval/smoke/eval_trace_link.go` 验证 evidence 回链率：至少 90% 样例应能回链到 `request_id + ai_trace_id`，并能列出缺失字段和阈值失败样例。

可运行的验证命令：

```bash
go test ./pkg/ai/eval -run 'TestNewEvaluationEvidence|TestValidateEvaluationEvidence|TestRunnerAddsEvaluationEvidence|TestRunnerReportsMissingTraceLink|TestRunnerAddsFailedRegressionEvidence' -count=1
go test ./internal/eval/smoke -run TestEvalTraceLinkSmoke -count=1
```

观察结果应满足：

- evidence 缺少 dataset name/version、sample id、metric name、eval run id 或 trace link 时失败。
- score 和 threshold 必须落在 `[0,1]`。
- score 低于 threshold 时，regression status 为 failed，并生成低敏 failure summary。
- runner evidence 能从 prediction trace identity 回链到 request、service trace、span 和 AI trace。
- eval trace link smoke 能统计 link rate，并列出缺失关联身份的 sample。

## 最佳实践

本项目在评估证据上采用以下实践：

- **dataset identity 显式建模**：`name + version` 是完整身份，不能把 dataset name 当成 runner 的可选装饰字段。
- **sample-level evidence 优先于平均分**：平均分可以用于概览，但回归分析必须能下钻到 sample + metric。
- **trace link 是必填事实**：生成 evidence 时必须具备 `request_id`、`ai_trace_id`、`service_trace_id`、`span_id` 和 `eval_run_id`。缺失时 fail fast。
- **threshold 缺失不等于通过**：没有阈值时状态应偏 warning，而不是默认 passed。
- **普通 evidence 不保存原文**：query、answer、context 和 tool args 属于 dataset 或审计链路，不进入普通 evidence。
- **失败摘要低敏化**：failure summary 说明 metric、score 和 threshold，不回显用户输入或模型回答。

暂不采用的做法：

- 不只用 report average score 作为 CI gate 的唯一证据。
- 不把 Langfuse score 或外部平台 id 作为核心 evidence 的唯一身份。
- 不在 evaluation evidence 中保存完整 prompt、完整回答或完整 context。

## 失败模式

评估证据常见失败方式包括：

- **dataset 身份不完整**：只有 version，没有 name，导致不同数据集的同名版本混淆。
- **样例无法回链**：sample 有 score，但缺少 `request_id` 或 `ai_trace_id`，无法定位生成该输出的请求。
- **平均分掩盖局部回归**：整体分数变化不大，但关键 sample failed，没有 per-sample evidence 无法发现。
- **threshold 缺失被误判成功**：没有门禁阈值时直接 passed，会让尚未校准的指标进入 CI。
- **失败报告泄露原文**：为了方便排障，把 query、answer、prompt 或 tool args 写入 evidence。
- **平台同步反向污染核心模型**：为了适配某个平台的 score schema，牺牲本项目的 dataset/sample/metric/trace link 语义。

## 降级路径

评估证据的降级策略应围绕“没有证据就不要假装可回归”：

1. **没有 eval run id**：可以生成普通 report，但不生成 evidence。
2. **prediction 缺 trace identity**：evidence 构建失败，让 runner 返回带 sample/metric 上下文的错误。
3. **threshold 未配置**：生成 warning evidence，等待人工复核或后续校准。
4. **link rate 低于门槛**：smoke 返回完整 result 和错误，列出缺失字段，方便 CI 或 quickstart 展示。
5. **平台不可用**：保留本地 report/evidence，跳过外部 score 同步；不能丢掉本地回归证据。

降级不能做的事是自动补齐假的 `request_id`、把缺少阈值当成通过，或者把原始样例内容塞进 failure summary。

## 复盘问题

- 这次实现证明了哪个理论概念？
  评估体系必须以 evidence 为核心，把 dataset/sample/metric/score 与 trace link 连接起来。否则评估无法指导工程修复。

- 哪个字段或边界最容易被误用？
  dataset version 和 score 最容易被孤立使用。version 缺少 dataset name 没有完整身份；score 缺少 sample/metric/trace link 没有诊断价值。

- 如果线上出问题，应该先看哪条记录？
  先看 failed evidence 的 sample id、metric name、score/threshold 和 failure summary，再用 `request_id + ai_trace_id` 回到完整请求链路。

- 后续阶段需要补什么能力？
  需要把 Langfuse score/dataset experiment、LLM-as-Judge 校准、人工反馈和 CI gate 与本地 evidence 契约对齐，并补充平台同步 smoke。

## 关联任务 / 测试

- 任务：T051-T060、T075
- 测试：
  - `pkg/ai/eval/evidence_test.go`
  - `pkg/ai/eval/runner_trace_link_test.go`
  - `internal/eval/smoke/eval_trace_link_test.go`
  - `internal/eval/smoke/agent_golden_test.go`
- ADR：
  - `docs/adr/0007-dual-plane-observability-evaluation-v1.md`
- Journal：
  - `docs/journal/0006-dataset-identity-domain-modeling.md`
