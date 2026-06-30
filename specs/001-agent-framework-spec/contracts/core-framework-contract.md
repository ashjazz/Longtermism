# 契约：核心框架完成标准

## 目的

本契约定义任何 AI 能力被视为“完成”前必须满足的横向要求。它适用于模型交互、提示词管理、检索、Agent、缓存、容错、限流、观测和评估等所有能力。

## 能力完成契约

每项能力必须提供以下证据：

1. **测试证据**：至少覆盖正常路径、边界路径和错误路径。
2. **评估证据**：AI 行为必须有可重复评估 case 或回归指标。
3. **可观测证据**：关键路径必须产生 trace 或等价诊断记录。
4. **失败模式**：必须列出主要生产失效方式。
5. **降级路径**：必须说明正常路径失败时的处理方式。
6. **文档证据**：重要取舍必须写入规格、计划、ADR 或 journal。

## 外部后端契约

外部服务只能通过稳定抽象接入：

- 模型服务通过模型供应商契约接入。
- 向量数据库通过向量存储契约接入。
- 可观测平台通过追踪契约接入。
- 评估平台通过数据集、指标和报告契约接入。
- 缓存服务通过缓存契约接入。

任何后端不得要求核心能力直接依赖其私有数据模型。

## 后端替换验收契约

替换 fake、in-memory、本地日志或 JSON 文件实现为真实后端时，用户可见能力预期不得改变。这里的“用户可见”不是指底层性能、索引参数或平台 UI 完全一致，而是指 `pkg/ai` 上层调用方依赖的语义保持一致。

| 后端类别 | 核心抽象 | 可替换实现 | 必须保持的用户可见语义 | 默认验收 |
|----------|----------|------------|------------------------|----------|
| 模型供应商 | `llm.Provider` | fake、OpenAI-compatible、未来 DeepSeek/Ollama/Anthropic adapter | `Name` 非空、能力声明稳定、Chat/content/tool call/usage/finish reason 一致、stream delta 顺序一致、`ErrUpstream` 与 context cancel 分类一致 | `go test ./pkg/ai/llm -run TestProviderAdaptersAreReplaceable -count=1` |
| 向量库 | `vectordb.Store` | memory、pgvector、Milvus | Upsert 覆盖语义、Search TopK/Threshold/filter、Delete 幂等、Health、context cancel、防御性副本一致 | `go test ./pkg/ai/vectordb -run TestMemoryStoreContract -count=1` |
| 可观测平台 | `obs.Tracer` | 本地日志、LangFuse、OTEL | 核心 trace 字段落地、记录顺序、普通 trace 不泄露原始 query/prompt/tool args、防御性拷贝一致 | `go test ./pkg/ai/obs -run TestLoggerTracerContract -count=1` |
| 评估数据集 | `eval.Dataset` | local JSON、对象存储、评估平台同步 | 样例字段、顺序、重复加载、防御性副本、context cancel 一致 | `go test ./pkg/ai/eval -run TestJSONDatasetContract -count=1` |
| 降级缓存 | `cache.FallbackCache` | memory、Redis、分布式 cache | scope 隔离、ttl、exact/stale、miss 返回 `nil,nil`、context cancel、防御性副本一致 | `go test ./pkg/ai/cache -run TestMemoryFallbackCacheContract -count=1` |

新增真实 adapter 时必须至少满足：

1. 复用对应的 `Run*Contract` 契约测试，不能只写 adapter 私有 happy path。
2. 默认 `go test ./...` 不依赖真实 API key、真实数据库、真实可观测平台或网络服务。
3. 平台/数据库/供应商专属类型不得进入 `pkg/ai` 上层接口；确需扩展能力时先更新契约和 ADR。
4. 错误、降级、隐私和防御性拷贝语义不得因为替换后端而改变。
5. 真实服务 smoke 必须显式 opt-in，并和默认离线门禁分离。

## 隐私契约

普通运行记录不得保存：

- 原始用户提示词。
- 完整模型 prompt。
- 原始 tool 参数。
- API key、token、secret。

允许保存：

- hash。
- 长度。
- 语言或类别。
- 模型身份。
- 用量。
- 延迟。
- 状态。
- 关联 ID。

## 门禁契约

默认本地门禁必须满足：

- 不强制依赖真实外部付费服务。
- 能识别测试失败。
- 能识别评估冒烟失败。
- 能识别明显静态检查问题。
- 能在失败时给出明确定位。
