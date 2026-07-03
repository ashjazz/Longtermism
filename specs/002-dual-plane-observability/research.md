# 调研与决策：双平面观测与评估体系 v1

## 决策 1：使用双平面观测，而不是单一平台模型

**Decision**：基础链路观测与 AI 语义观测分为两个平面。基础链路平面负责服务入口、跨组件调用、上下文传播和基础延迟/错误；AI 语义平面负责 LLM generation、retrieval、tool、agent、evaluator、token、成本、prompt 身份和评估证据。

**Rationale**：一次 AI Agent 请求既有传统服务问题，也有 AI 特有问题。只用基础链路观测会丢失 prompt、token、retrieval、tool 和 eval 语义；只用 AI 观测平台会削弱服务入口、数据库、缓存、外部调用和跨服务传播能力。双平面符合 ADR-0007，也能让后续 RAG、Agent 和评估能力共用同一证据链。

**Alternatives considered**：
- 只做基础链路观测：标准化强，但无法表达 AI 质量和评估语义。
- 只做 AI 观测平台：AI UI 更直接，但无法形成完整服务链路。
- 自建完整平台：语义最贴合，但成本过高，不符合本阶段目标。

## 决策 2：GoFrame 应用层复用现有 tracing 接入，AI 内核不依赖平台类型

**Decision**：应用启动、shutdown、exporter 配置和基础服务链路在 GoFrame 应用层接入；`pkg/ai` 继续只暴露 `obs.Trace` 和 `obs.Tracer`，不引用 OTel/Langfuse SDK 类型。

**Rationale**：GoFrame 已有 tracing contrib 模块，适合服务层接入。AI 内核需要可独立测试和可替换后端，不能被平台 SDK 类型牵引。这样既能减少基础设施自研，也能保留核心抽象独立。

**Alternatives considered**：
- 在 `pkg/ai` 直接创建 span：实现简单，但会污染核心接口。
- 完全自建 tracing 初始化：控制力强，但重复框架能力，维护成本高。
- 只保留本地 logger：默认门禁稳定，但无法验证生产平台接入。

## 决策 3：`obs.Trace` 是 AI 语义源模型，平台 adapter 只做映射

**Decision**：`obs.Trace` 继续作为 AI 语义源模型。OTel adapter 将它映射为 span/event/attribute；Langfuse 接入优先消费 OTel/OTLP 语义或最小 ingestion adapter；本地 logger 和 eval report 也消费同一源模型。

**Rationale**：现有 `obs.Trace` 已有 contract/privacy 测试和核心字段。保持它作为源模型，可以让本地测试、平台接入、eval 报告和学习记录复用同一语义，避免平台模型反向决定核心字段。

**Alternatives considered**：
- 以 OTel span 作为源模型：标准化强，但 AI 语义不够清晰。
- 以 Langfuse observation 作为源模型：AI 平台能力强，但核心会被平台绑定。
- 每个后端单独定义字段：短期灵活，长期会导致字段漂移和回归困难。

## 决策 4：先定义关联身份契约，再实现真实 adapter

**Decision**：v1 先定义 request id、service trace id、AI trace id、session id、user id、eval run/sample id、平台 observation id 的关联规则，再实现 adapter。

**Rationale**：本规格的核心用户价值是“从一次请求知道发生了什么”。如果先接平台再补关联，容易产生孤立 span、重复 trace 或无法从 eval 回链到请求的问题。

**Alternatives considered**：
- 先接平台 UI 再补字段：见效快，但后续返工概率高。
- 只使用一个全局 trace id：简单，但无法表达多 AI 子步骤、eval run 和平台 observation 的关系。
- 每层各自生成 id：局部清晰，全链路割裂。

## 决策 5：普通 trace 只保留安全摘要，原文留存另行设计

**Decision**：普通观测记录只保留 hash、长度、分类、状态、数量、分数、耗时、token 和成本摘要。原始 query、完整 prompt、完整 tool args、密钥、认证 token、PII 和外部响应原文默认禁止进入普通 trace。

**Rationale**：AI trace 很容易携带敏感内容。观测系统一旦接入外部平台或集中日志，泄露面会放大。v1 的目标是建立可诊断摘要，不建立原文审计链路。

**Alternatives considered**：
- 记录完整内容方便调试：短期排障方便，但隐私和合规风险高。
- 完全不记录 query/prompt 相关信息：安全但无法定位 prompt 版本和输入类别问题。
- 加密保存原文：可能需要，但必须另行设计权限、retention 和审计，不属于 v1。

## 决策 6：默认离线验证与真实平台 smoke 分离

**Decision**：默认门禁只依赖 fake/in-memory/logger sink；真实 OTel collector 或 Langfuse endpoint 只通过显式 opt-in smoke 验证。

**Rationale**：项目宪章要求默认验证不能依赖实时外部付费服务。观测平台验证又必须在某个阶段真实跑通，因此将默认回归和真实 smoke 分层。

**Alternatives considered**：
- 默认测试直接打真实平台：最接近生产，但不稳定且需要密钥。
- 完全不做真实 smoke：默认稳定，但无法发现平台字段映射问题。
- 手工 UI 验证：能辅助学习，但不能作为完成证据。

## 决策 7：学习资产按“理论概念 -> 工程实验 -> 最佳实践 -> 复盘问题”组织

**Decision**：每个主要观测切片必须关联学习资产，至少包含学习目标、核心概念、工程实验、最佳实践、失败模式、降级路径和复盘问题。

**Rationale**：用户明确把“边做边学”作为项目重点之一，且学习目标不仅是事后复盘，还包括系统理解系统可观测性、分布式追踪、AI Agent 可观测性、评估体系和工程最佳实践。将学习资产纳入验收能防止它变成额外装饰。

**Alternatives considered**：
- 只在 journal 记录问题修复：适合复盘，不足以覆盖理论学习。
- 只写外部资料链接：学习成本低，但与工程落点脱节。
- 单独写教程不关联任务：可读性好，但容易和实现分叉。

## 决策 8：v1 不迁移 prompt/dataset/user feedback/CI gate 到平台

**Decision**：v1 只为 prompt 身份、eval score、用户反馈和自动化门禁预留关联字段；不迁移 prompt registry、annotation queue、user feedback、LLM-as-Judge calibration 或 CI experiment gate。

**Rationale**：这些能力都很重要，但会显著扩大范围。v1 应优先证明请求链路、AI 语义记录、隐私边界、eval 回链和真实平台最小 smoke。

**Alternatives considered**：
- 一次性建设完整 LLMOps：完整但风险大，违背最小切片原则。
- 完全不考虑后续平台能力：短期简单，但字段和关联模型可能返工。
