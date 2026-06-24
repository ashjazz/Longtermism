# ADR-0004：优先自建 lightweight Agent Harness

**日期**：2026-06-23
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

本项目的核心目标是学习并构建生产级 AI Agent 框架，而不是快速拼装一个依赖第三方编排框架的应用。Agent Harness 是后续 tool calling、状态追踪、轨迹评估、成本控制、循环检测和降级策略的控制面。如果 P0/P3 过早引入 LangGraph、CrewAI、AutoGen、LangChain 等编排框架作为核心依赖，prompt、token、tool 参数、trace、cost 和降级行为可能被框架封装隐藏，反而削弱学习价值和可诊断性。

## Decision（决策）

本项目默认自建 lightweight Agent Harness，并把核心实现保留在 `pkg/ai/agent`。第三方编排框架不得成为 `pkg/ai` 核心依赖；只有在确实减少复杂度且不隐藏关键控制面时，才可以作为 adapter 或 app-layer integration 引入。

Harness 必须显式掌控以下能力：`ProviderCapabilities`、native tool calling loop、tool contract/schema、step trace/replay、trajectory eval、max steps、step timeout、token budget、loop detection、fallback/cache/failover/rate limit。

## Alternatives Considered（备选方案）

### 方案 1：直接采用 LangGraph 作为核心 Agent Runtime

- **优点**：状态图、checkpoint、human-in-the-loop、持久化执行路径成熟，适合复杂多步骤 Agent。
- **缺点**：会把执行状态、节点调度、重放语义和部分观测逻辑绑定到框架模型上。
- **未采用原因**：当前阶段更需要理解和控制 native tool calling loop、限制条件、trace 和 eval，而不是优先获得完整状态图能力。

### 方案 2：直接采用 LangChain / CrewAI / AutoGen 等应用级框架

- **优点**：生态丰富，集成工具多，能快速搭建 demo 或多 Agent 流程。
- **缺点**：抽象层较厚，容易隐藏 prompt 组织、工具参数、token 成本、失败分类和降级行为。
- **未采用原因**：项目的学习目标是生产工程化能力，核心要求是可 trace、可替换、可测试，而不是最快完成演示。

### 方案 3：完全拒绝第三方框架

- **优点**：核心边界最清晰，所有行为都可由项目控制。
- **缺点**：后续遇到 durable checkpoint、复杂状态图、人工审批、连接器生态时可能重复造轮子。
- **未采用原因**：第三方框架可以作为有边界的 adapter 或应用层集成，只是不应进入核心依赖。

### 方案 4：自建 lightweight harness，第三方框架作为 adapter/app-layer integration

- **优点**：核心控制面透明，契约测试可覆盖，后续仍可按场景接入成熟框架能力。
- **缺点**：需要自行实现最小 tool loop、限制控制、trace、eval 和错误边界。
- **采用原因**：最符合本项目“30% 模型化 + 70% 工程化”的学习目标，也符合 P0/P3 先建立可观测、可评估、可降级地基的路线。

## Consequences（影响）

### 正面影响

- `pkg/ai/agent` 的核心执行语义保持清晰，可通过单元测试和 trajectory eval 直接验证。
- provider 能力、tool schema、step trace、token budget、loop detection 和降级路径不会被外部框架隐藏。
- 后续引入 LangGraph、LangChain、CrewAI、AutoGen 或平台连接器时，可以通过 adapter 隔离框架差异。

### 负面影响

- 项目需要自行实现最小 native tool calling executor 和限制控制。
- 短期内不会获得第三方框架现成的 checkpoint、复杂图编排、human-in-the-loop 和丰富连接器生态。

### 风险

- **风险**：自建 harness 范围膨胀，重复实现成熟框架的大量功能。
  **缓解**：P3 只实现最小 native tool calling loop、工具注册、限制控制、trace 和 trajectory eval；复杂状态图和 durable checkpoint 另写 ADR 再评估。
- **风险**：adapter 边界不清，第三方框架能力渗入核心契约。
  **缓解**：第三方框架只能依赖 `pkg/ai` 公共契约，不能反向要求核心接口接受框架专属类型。
- **风险**：自建实现缺少生产能力。
  **缓解**：每个 harness 能力必须配套契约测试、失败 trace 和 eval case；真实引入框架前必须证明它不隐藏 prompt、token、tool 参数、cost、trace 和降级行为。

## Revisit Conditions（重新审视条件）

满足以下任一条件时重新审视本决策：

- P3 最小 native tool calling executor 已完成，且出现明确的复杂状态图、durable checkpoint 或 human-in-the-loop 需求。
- 第三方框架能通过 adapter 接入，并且不改变 `pkg/ai` 核心契约。
- trajectory eval、step replay 或生产运行需要框架级持久化能力，而自建实现成本明显高于收益。
- 新框架或平台能提供显著的观测、评估或连接器价值，并能保留 prompt、token、tool 参数、cost、trace 和降级行为的可见性。
