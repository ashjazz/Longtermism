# Dataset Identity 领域建模讨论复盘

**日期**：2026-07-07
**关联任务**：T057 / T058
**关联模块**：pkg/ai/eval
**状态**：已讨论 / 已实现

## 发生了什么

在实现 eval evidence 与 runner trace-link 时，`LocalRunner` 增加了 `WithDatasetName`、
`WithEvalRunID` 和 `WithMetricThreshold` 选项。代码审查时提出一个关键问题：

> `DatasetName` 是否应该只是可选配置？如果存在多个评估数据集，仅靠 version 是否无法区分？

最初的保守判断是：为了兼容旧 runner 测试，`DatasetName` 可以只在 evidence 模式下必填；
旧的 `NewRunner("version")` 汇总模式继续允许没有 dataset name。

进一步讨论后，我们形成了更清晰的工程判断：项目仍处在框架早期阶段，快速迭代和功能扩展是常态。
此时不应该为了迁就早期简化实现而保留不完整领域语义。旧测试和旧接口只能说明历史契约存在，
不能天然证明它是正确领域模型。

## 讨论过程

这次讨论的核心不只是 `DatasetName` 是否必填，而是如何识别“新增字段”背后的领域概念。

如果一个字段满足以下特征，它就可能不只是旧对象上的一个属性，而应被提升为独立领域对象：

1. 它和其它字段共同形成不可分割的身份或不变量。
2. 它会被多个模块共同依赖，例如 report、evidence、平台同步、CI gate。
3. 缺失它会导致事实无法解释，或在多实例、多数据集、多策略场景下发生混淆。
4. 继续把它作为零散 option 会制造非法状态，例如有 `dataset_version` 但没有 `dataset_name`。
5. 后续演进很可能围绕它继续增加属性，例如 source、schema version、split、checksum、owner。

沿着这个标准重新审视后，`dataset_name + dataset_version` 不是 runner 的普通配置，而是评估体系里的
数据集身份。单独的 `v1.2.0` 只有在某个数据集上下文里才有意义；在多数据集场景中，
`agent-golden@v1.2.0`、`rag-retrieval-golden@v1.2.0` 和 `provider-failover-smoke@v1.2.0`
不能被同一个 version 字符串区分。

因此，`DatasetName` 不应被视为 evidence 的可选装饰字段。更准确的设计是引入一个小的值对象，
例如：

```go
type DatasetIdentity struct {
	Name    string
	Version string
}
```

它是 eval 领域对象，不是平台 adapter 字段，也不是 runner 的临时 option。

## 形成的结论

我们达成以下设计共识：

1. **早期框架不应为了兼容旧的简化契约而牺牲正确领域建模。**
   旧测试如果表达的是不完整语义，应调整测试和契约，而不是让实现继续迁就。

2. **KISS 不等于保留原始模型。**
   KISS 的目标是让模型清晰、职责单一、非法状态更少。引入一个小的 `DatasetIdentity` 值对象，
   比在 runner、report、evidence 上散落 `datasetName` 和 `datasetVersion` 更简单。

3. **是否新增抽象，要看是否出现新的独立职责和不变量。**
   `DatasetIdentity` 的职责是唯一标识评估数据集版本；`Runner` 的职责是编排评估；
   `Evidence` 的职责是记录 sample/metric/score 与 trace link。三者不应混成一个配置包。

4. **评估证据必须具备完整数据集身份。**
   只靠 dataset version 无法支撑多数据集、多 RAG 策略、多 agent 任务、多 provider smoke 的横向比较。

5. **后续实现应优先围绕领域对象收敛接口。**
   已将 runner 构造从 `NewRunner(datasetVersion, options...)` 升级为接收 `DatasetIdentity`，
   并让 `Report`、`EvaluationEvidence` 共同使用同一份数据集身份语义。

## 指导思想

后续在框架早期阶段遇到类似问题时，先不要问“怎样最少改动让测试继续通过”，而要先问：

- 这个扩展是否代表一个新的领域概念？
- 这个概念是否有独立生命周期、边界和不变量？
- 放进原对象是否违反单一职责原则？
- 当前对象是否会因此出现更多非法字段组合？
- 新抽象是否让系统更清晰，而不是为了抽象而抽象？

如果答案指向新的领域对象，应优先重新建模，再调整旧接口和测试。早期项目的优势正是迁移成本低，
此时越早修正领域模型，后续观测、评估、平台接入和回归门禁越不容易长歪。

## 后续行动

- 增加或调整测试：补充 dataset identity 缺失时的失败路径，覆盖 report/evidence 的完整身份。
- 增加或调整评估 case：多数据集场景下验证相同 version 不会混淆 evidence。
- 增加或调整 trace 字段：如后续 trace 直接携带 eval dataset 信息，应复用 DatasetIdentity 语义。
- 增加或调整降级路径：缺少 dataset identity 不应被默认值补齐；应 fail fast 或拒绝生成 evidence。
- 需要补充的 ADR / ROADMAP / tasks：已在 T058 后续调整中引入 `DatasetIdentity` 并收敛 runner/report/evidence 契约。
