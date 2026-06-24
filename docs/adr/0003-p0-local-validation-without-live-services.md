# ADR-0003：P0 默认离线验证策略

**日期**：2026-06-23
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

本项目是边学习边构建的 AI Agent 框架，默认验证必须稳定、低成本、可重复。P0 需要打通 `prompt -> llm -> obs -> eval -> local gate`，但如果默认门禁依赖真实模型服务、真实向量库、LangFuse/OTEL 平台或实时网络，开发会被 API key、限流、费用、网络波动和平台状态拖住。项目已经引入 fake provider、in-memory trace recorder、static dataset 和本地 prompt 目录，本 ADR 固化默认离线验证与真实服务 smoke 的边界。

## Decision（决策）

P0 默认验证采用离线策略：`go test`、`go test -race`、`go vet` 和默认 `eval-smoke` 不得强制依赖真实 API key、真实向量数据库、真实可观测平台或实时外部服务。默认验证必须优先使用 fake、in-memory、httptest、本地 JSON dataset、本地 prompt 模板和确定性指标。

真实服务 smoke 允许存在，但必须显式 opt-in，例如通过独立命令、环境变量或 build tag 开启；它不能作为默认本地门禁的必要前置，失败也不能覆盖离线确定性评估结论。

## Alternatives Considered（备选方案）

### 方案 1：默认门禁直接调用真实模型和平台

- **优点**：最贴近真实生产链路，能更早暴露供应商协议、鉴权和网络问题。
- **缺点**：测试不稳定、成本不可控、需要 API key、容易受限流和外部平台状态影响。
- **未采用原因**：P0 首要目标是建立可重复的工程闭环，而不是把开发节奏绑定到外部服务可用性。

### 方案 2：完全禁止真实服务 smoke

- **优点**：默认验证最稳定，完全无外部依赖。
- **缺点**：真实 provider 协议、错误映射、认证配置和平台集成问题会暴露得太晚。
- **未采用原因**：真实服务 smoke 是必要补充，但它应是显式的、可隔离的，而不是默认门禁。

### 方案 3：默认离线验证 + 显式真实服务 smoke

- **优点**：本地和 CI 保持稳定，真实服务问题仍可通过手动或独立 smoke 暴露。
- **缺点**：需要维护两套验证路径，并清楚标注 smoke 结果的语义。
- **采用原因**：最符合学习型项目和生产工程化的平衡：日常验证稳定，真实集成可逐步加深。

## Consequences（影响）

### 正面影响

- 新会话、新机器和 CI 可以在没有 API key 的情况下运行默认验证。
- P0 能优先验证核心抽象、契约测试、trace 字段和 eval runner，而不是被部署环境阻塞。
- 真实 provider、向量库和观测平台都能作为 adapter 逐步接入，不污染核心契约。

### 负面影响

- 默认门禁不能证明真实模型服务、真实向量库或 LangFuse/OTEL 的集成质量。
- 真实服务 smoke 的问题可能晚于离线测试暴露，需要后续专门补集成测试和运行文档。

### 风险

- **风险**：团队误把 fake/in-memory 通过当成真实生产可用。
  **缓解**：所有报告和文档必须标明默认验证是离线确定性验证；真实服务 smoke 单独标记。
- **风险**：真实服务 smoke 长期缺失，导致 adapter 协议问题积累。
  **缓解**：在 P0-A provider 实现后增加显式 smoke 命令，并在 quickstart 中说明触发方式和失败语义。
- **风险**：默认 eval-smoke 悄悄依赖环境变量。
  **缓解**：默认 `make eval-smoke` 必须在无 API key 情况下可运行；任何 live smoke 使用不同命令或显式 opt-in 参数。

## Revisit Conditions（重新审视条件）

满足以下任一条件时重新审视本决策：

- P0 最小闭环完成后，需要把某个真实 provider smoke 纳入夜间任务或非阻塞 CI。
- 项目进入真实部署阶段，需要定义 staging 环境的 live integration gate。
- 默认离线评估无法覆盖某类关键回归，必须引入真实服务才可判断。
- 引入 LangFuse、OTEL、Milvus 或 pgvector adapter 后，需要重新定义默认门禁与集成门禁的边界。
