# Phase 0 调研与决策：生产级 AI Agent 框架

## 决策 1：P0 先交付最小 AI 工程闭环

**Decision**：P0 只交付 `prompt -> llm -> obs -> eval -> local gate` 的最小闭环，不提前实现完整 RAG、完整 Agent、复杂容错或语义缓存。

**Rationale**：`准备清单.md` 明确强调评估体系、可观测性和生产故障意识。若先做 RAG 或 Agent，后续很容易出现“能力看起来可用，但无法证明有效”的问题。P0 地基必须先让任何模型能力可调用、可追踪、可评估。

**Alternatives considered**：

- 先做 RAG demo：展示效果快，但评估与 trace 后补会返工。
- 先做 Agent tool loop：有趣但依赖 llm provider、tool contract、trace 和 eval，前置条件不足。

## 决策 2：P0-A 使用 OpenAI-compatible provider 作为首个真实 adapter

**Decision**：首个真实模型 adapter 采用 OpenAI-compatible API 形态，但核心接口仍保持 `llm.Provider` 抽象，不绑定任一供应商。

**Rationale**：OpenAI-compatible 形态能兼容多个真实服务和本地推理 endpoint，有利于快速打通真实调用路径。与此同时，项目核心价值在于多 provider 抽象、能力声明、错误边界和可替换性，因此供应商差异必须封装在 adapter 内。

**Alternatives considered**：

- 直接绑定 OpenAI 官方 SDK：上手快，但容易让核心抽象被 SDK 形态牵引。
- 同时实现多 provider：覆盖面广，但 P0 范围过大。
- 仅使用 fake provider：测试稳定，但无法验证真实协议边界。

## 决策 3：默认测试不依赖实时外部服务

**Decision**：默认测试使用 fake、in-memory 和 httptest；真实 API key 的调用只能作为可选 smoke，不进入默认门禁。

**Rationale**：本项目是长期学习和实践载体，默认验证必须稳定、低成本、可重复。外部服务可能受网络、费用、限流和密钥影响，不应阻塞本地开发。

**Alternatives considered**：

- 默认跑真实 API：更接近真实环境，但不稳定且有成本。
- 完全不跑真实 API：稳定，但真实协议问题发现较晚。

## 决策 4：错误边界以生产语义为准

**Decision**：timeout、429、5xx 归为可重试/可降级上游错误；400、401、403、404 等参数或认证错误必须快速失败，不进入上游重试路径。

**Rationale**：错误分类会直接影响 P1 的重试、熔断和 failover。如果把认证或参数错误当成可重试上游错误，会造成无意义重试、成本浪费和故障定位困难。

**Alternatives considered**：

- 所有 provider error 统一包装：实现简单，但会污染 resilience 判断。
- adapter 完全透传 provider error：保留细节，但上层难以稳定处理。

## 决策 5：评估系统先做确定性 runner

**Decision**：P0-D 先实现 JSON dataset、确定性 metric 和 runner/report；LLM-as-Judge、平台数据集同步和 meta-evaluation 留到后续阶段。

**Rationale**：确定性评估适合进入默认本地门禁，成本低、结果稳定。LLM-as-Judge 是必要方向，但需要 bias 处理和人工校准，不适合作为 P0 的第一门禁。

**Alternatives considered**：

- P0 直接做 LLM-as-Judge：贴近 AI 评估场景，但会引入模型依赖和不稳定性。
- 只做单元测试：稳定但无法承载 AI 能力回归。

## 决策 6：向量数据库与可观测平台暂缓最终选型

**Decision**：pgvector vs Milvus、日志/OTEL vs LangFuse 均暂缓最终决策。P0 只固化 `vectordb.Store`、`obs.Tracer`、`eval.Dataset/Runner` 等抽象边界。

**Rationale**：当前阶段的关键不是选择某个后端，而是确保未来能替换后端而不重写核心能力。Milvus 和 LangFuse 都是强候选，但应作为 adapter 或后端实现出现，而不是核心契约。

**Alternatives considered**：

- 立即选 Milvus + LangFuse：利于深入建设，但过早绑定会放大运维和集成复杂度。
- 立即选 pgvector + 本地日志：简单，但可能不足以展示向量库和 LLMOps 深水区。

## 决策 7：普通 trace 不记录原始敏感内容

**Decision**：P0-C 的普通 trace 只记录 hash、长度、语言/类别、模型、用量、延迟、状态和关联 ID，不记录原始 query、完整 prompt 或 tool 参数原文。

**Rationale**：AI 系统 trace 很容易包含用户隐私、密钥片段或敏感业务数据。调试能力必须和隐私边界同时建立。

**Alternatives considered**：

- trace 保存完整 prompt：调试方便，但隐私和合规风险高。
- trace 只记录错误码：安全但无法诊断 AI 特有故障。
