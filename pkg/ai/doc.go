// Package ai 是 AI Agent 框架的内核。
//
// 设计目标：把《准备清单.md》描述的生产级 AI 工程能力，沉淀为一套与 Web 框架解耦、
// 可独立测试、可直接演进为独立库的 Go 包。内核不依赖 GoFrame，应用层（internal/）只做装配。
//
// 分层与「准备清单.md」章节映射：
//
//	llm/        §2   模型抽象层：多 provider、流式、token 计数、超时/重试
//	prompt/    §9.1  Prompt as Code：模板、版本、hash 追踪、A/B
//	rag/        §3   检索增强：chunking、embedding、混合检索、RRF、re-rank、HyDE
//	vectordb/   §5   向量库抽象：HNSW/IVF 选型、metadata 过滤
//	agent/      §4   Agent 循环：原生 tool calling、步数上限、循环检测、token 预算
//	cache/      §12  exact/stale fallback cache + 语义缓存
//	resilience/ §10  断路器、多 provider failover、降级层级
//	ratelimit/  §11  token bucket 多维限流、模型路由、成本控制
//	obs/        §8   可观测性：trace 结构、TTFT/幻觉率/成本指标
//	eval/       §6★  评估体系：golden dataset、RAGAS 指标、LLM-as-Judge、CI 门禁
//
// 共同约束（见项目 AGENTS.md「完成定义」）：
// 每个模块都应可被评估、可观测、可降级。新增模块先确认对应的章节，再动手。
package ai
