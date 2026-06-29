# ADR-0005：向量库 Adapter 边界

**日期**：2026-06-29
**状态**：accepted
**决策者**：JazzAsh、Codex

## Context（背景）

P0 已经建立 `pkg/ai/vectordb.Store` 抽象、内存向量库实现和 Store 契约测试。项目后续仍会在 pgvector 与 Milvus 之间做真实选型，但 RAG、eval、agent harness 不应该直接依赖任何单一向量库的数据模型、索引配置或 SDK 类型。当前需要把“核心契约负责什么”和“真实向量库 adapter 负责什么”写清楚，避免后续接入 pgvector/Milvus 时把基础设施细节反向渗入 `pkg/ai/rag`。

## Decision（决策）

`pkg/ai/vectordb.Store` 是 RAG 上层唯一依赖的向量库边界，核心契约只暴露 `Upsert`、`Search`、`Delete`、`Health` 与 `Vector/Query/Hit` 这些稳定语义。pgvector、Milvus 和 memory fake 都必须作为 adapter 实现同一接口，并通过 `RunStoreContract` 契约测试；索引类型、collection/table schema、连接池、批量写入策略、filter 方言和真实服务运维能力都只能留在具体 adapter 或装配层。

## Boundary（边界）

### 核心契约负责

- 表达用户可见的最小向量库能力：写入、覆盖、检索、删除和健康检查。
- 保持 metadata filter、TopK、Threshold、context cancellation 和错误返回语义稳定。
- 要求实现返回防御性副本，避免调用方修改命中结果后污染 store 内部状态。
- 为 RAG、eval 和后续 failover 提供一致行为，而不是暴露某个数据库的内部能力。

### Adapter 负责

- 把 `Vector/Query/Hit` 映射到具体后端的数据模型、索引和查询语法。
- 处理连接、认证、超时、重试、批量写入、schema/collection 初始化和迁移。
- 将后端错误映射为可诊断错误，不泄露连接串、凭据或原始敏感文本。
- 提供真实服务 smoke 或集成测试；默认单元测试仍使用 memory fake 和契约测试。

### 上层不得依赖

- pgvector 的 SQL、表名、索引名、operator class 或 PostgreSQL 专属类型。
- Milvus 的 collection、partition、index params、consistency level 或 SDK 结构。
- memory fake 的排序实现、内部 map、锁策略或仅为测试存在的行为。

## Alternatives Considered（备选方案）

### 方案 1：直接让 RAG 依赖 pgvector

- **优点**：部署简单，可复用 PostgreSQL、事务、备份和现有应用数据模型；对早期单体应用更友好。
- **缺点**：RAG 代码容易出现 SQL、索引参数和 PostgreSQL 错误码耦合；高规模向量检索、分片和独立扩容能力受限。
- **未采用原因**：pgvector 是重要候选 adapter，但不能成为 `pkg/ai/rag` 的核心依赖，否则后续切换 Milvus 会重写上层检索逻辑。

### 方案 2：直接让 RAG 依赖 Milvus

- **优点**：面向向量检索场景设计，适合较大规模 collection、独立扩容和更专业的索引能力。
- **缺点**：引入额外服务、SDK、collection schema、运维复杂度和本地开发成本；P0 默认门禁会变慢且依赖外部进程。
- **未采用原因**：Milvus 适合作为生产级 RAG 的强候选 adapter，但核心框架在当前阶段更需要稳定契约、离线门禁和可替换边界。

### 方案 3：把 memory fake 当作核心实现长期使用

- **优点**：测试最快、最稳定，不需要外部服务，便于教学和本地 smoke。
- **缺点**：不具备持久化、并发扩展、真实索引、权限隔离和生产运维能力。
- **未采用原因**：memory fake 只用于单元测试、本地 demo 和契约验证，不能被解释为生产向量库选型。

### 方案 4：稳定 Store 契约，pgvector/Milvus/memory 都作为 adapter

- **优点**：RAG 上层只依赖稳定语义；后续可以通过同一套契约测试比较不同后端；真实服务能力和默认本地门禁互不绑死。
- **缺点**：核心契约需要保持克制，某些后端高级能力不能直接暴露给上层。
- **采用原因**：最符合 ADR-0001 的“暂缓最终选型”和 ADR-0004 的“核心控制面透明”原则，也能让 T080 的契约测试成为后续替换后端的质量门控。

## Implementation Notes（实施约束）

- 新增 pgvector 或 Milvus adapter 时，必须新增对应的 `TestXxxStoreContract`，复用 `RunStoreContract`。
- Adapter 可以有额外配置结构，但不能要求 `pkg/ai/rag` 引入数据库 SDK 类型。
- metadata filter 的公共语义先保持精确匹配；如果后续需要范围、数组、全文或混合检索，需要另写 ADR 扩展契约。
- 真实 adapter 的 smoke 测试必须显式 opt-in，不能让 `go test ./...` 默认依赖真实数据库或网络服务。
- 生产接入必须补充 schema/collection 初始化、索引参数、迁移策略、数据一致性和失败降级文档。

## Consequences（影响）

### 正面影响

- RAG、eval 和 agent harness 可以在不关心底层向量库的情况下演进。
- pgvector、Milvus 与 memory fake 的职责差异被显式记录，避免“测试实现”被误当成生产方案。
- Store 契约测试成为后续 adapter 接入的最低验收线。

### 负面影响

- 初始 `Store` 接口不会覆盖所有真实向量库高级能力，例如混合检索、re-rank、partition、TTL 或复杂 filter。
- 后续如果要使用后端特有能力，需要通过新的核心能力建模，而不能直接穿透 adapter。

### 风险

- **风险**：公共 `Filter map[string]any` 过于宽泛，不同后端支持能力不一致。
  **缓解**：P0/P1 只承诺精确匹配；复杂 filter 在真实 adapter 前通过 ADR 和契约测试扩展。
- **风险**：真实后端性能问题被 memory fake 掩盖。
  **缓解**：默认测试保证语义，真实服务 smoke 与 benchmark 另行建立，分别验证延迟、召回、吞吐和成本。
- **风险**：上层为了使用高级能力绕过 `Store` 接口。
  **缓解**：新增能力必须先进入核心契约或 app-layer adapter，不能在 `pkg/ai/rag` 中直接依赖 pgvector/Milvus SDK。

## Revisit Conditions（重新审视条件）

满足以下任一条件时重新审视本决策：

- pgvector 或 Milvus 的首个真实 adapter 落地，并发现现有契约无法表达必要生产语义。
- RAG 阶段需要混合检索、RRF、re-rank、range filter、partition 或多租户物理隔离能力。
- 评估结果显示某个后端能力会显著改变召回率、延迟、成本或降级策略。
- 项目进入真实部署阶段，需要在 pgvector 与 Milvus 之间做最终生产选型。
