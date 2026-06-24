# ADR-0002：P0 模型调用错误分类策略

**日期**：2026-06-23
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

P0-A 会引入首个 OpenAI-compatible provider adapter，后续 P1 的重试、断路器、failover、缓存降级都会依赖 provider 返回的错误语义。如果错误分类不稳定，上层 resilience 可能把参数错误当成上游故障反复重试，或者把真实上游故障当成普通调用失败而无法降级。项目已经在 `llm.ErrUpstream` 和 `pkg/ai/internal/apperror` 中建立了上游错误和内部分类辅助，本 ADR 固化 P0 阶段的 HTTP/context 错误边界。

## Decision（决策）

P0 provider adapter 采用以下错误分类：

- `timeout`、`context deadline exceeded`、连接失败、`429`、`5xx`：归为可重试/可降级的上游错误，必须支持 `errors.Is(err, llm.ErrUpstream)`。
- `400`、`401`、`403`、`404`：归为不可重试的调用方错误，必须快速失败，不得包装为 `llm.ErrUpstream`，也不得进入断路器失败计数。
- 响应格式不符合 adapter 预期：归为协议错误，保留原始上下文供诊断，但是否重试由后续实现根据具体场景显式决定。
- 内部分类可以使用 `pkg/ai/internal/apperror`，但公开契约仍以 `llm.ErrUpstream` 等业务包错误为准。

## Alternatives Considered（备选方案）

### 方案 1：所有 provider 错误统一包装为 ErrUpstream

- **优点**：实现最简单，上层只需要处理一种错误。
- **缺点**：400 参数错误、401 认证错误、403 权限错误会被无意义重试和熔断，浪费 token、放大延迟，并掩盖真实配置问题。
- **未采用原因**：这会直接破坏 P1 的 resilience 判断，让断路器把“调用方修不好的请求”误判成“供应商不可用”。

### 方案 2：完全透传供应商原始错误

- **优点**：保留最多供应商细节，不丢失原始诊断信息。
- **缺点**：上层无法稳定判断哪些错误应该重试、熔断、failover 或降级；每个 provider 都会把私有错误模型泄漏到核心逻辑。
- **未采用原因**：本项目要求 provider 可替换，核心层不能依赖单一供应商错误结构。

### 方案 3：按生产语义分类并保留 cause

- **优点**：上层可以稳定使用 `errors.Is` 判断重试/熔断路径，同时保留底层 cause 供日志和诊断。
- **缺点**：adapter 需要维护一层错误映射，且必须用契约测试防止分类漂移。
- **采用原因**：这是可观测、可降级、可替换 provider 的最小稳定边界。

## Consequences（影响）

### 正面影响

- P1 的重试、断路器和 failover 可以只对真正的上游不可用生效。
- 4xx 调用方错误会快速暴露，便于修配置、修请求、修权限，而不是被重试噪声掩盖。
- 不同 provider adapter 可以共享同一套错误契约测试，减少后续替换供应商的行为漂移。

### 负面影响

- 每个真实 adapter 都必须维护 HTTP 状态码、context error 和协议错误的映射。
- 某些供应商可能把业务失败塞进 200 响应或非标准错误体，adapter 需要额外解析和测试。

### 风险

- **风险**：供应商错误结构变化导致 adapter 分类失准。
  **缓解**：P0-A 测试必须覆盖 400、401、429、5xx、timeout；后续真实 smoke 只作为补充，不替代默认离线测试。
- **风险**：协议错误是否可重试存在灰区。
  **缓解**：默认先归为协议错误并保留 cause，不自动进入 `ErrUpstream`；只有通过 ADR 或明确测试证明可安全重试时才调整。

## Revisit Conditions（重新审视条件）

满足以下任一条件时重新审视本决策：

- 首个 OpenAI-compatible provider 实现后，发现供应商错误结构无法按当前分类稳定映射。
- P1 resilience wrapper 需要更细的错误语义，例如 rate_limit、timeout、temporary_unavailable、auth_failed。
- 引入第二个真实 provider 后，多个供应商的错误模型出现无法被当前四类覆盖的差异。
- 线上或 smoke 发现某类错误被错误重试、错误熔断或错误降级。
