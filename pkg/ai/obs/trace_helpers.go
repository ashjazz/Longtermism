package obs

import "time"

// TraceOption 描述一次不可变 trace 更新。
//
// 这里没有采用“func(*Trace)”的常见写法，是因为本项目把不可变性作为核心约束：
// option 接收当前 Trace 值并返回新 Trace 值，调用方传入的 base trace 不会被原地改动。
type TraceOption func(Trace) Trace

// NewTrace 创建一条带核心身份字段的 trace。
//
// traceID/feature/timestamp 是所有可观测记录的最小骨架；其它字段通过 option 分组写入。
// 这样后续 eval smoke、provider wrapper 或 agent executor 可以按阶段逐步补充模型、
// prompt、检索、成本和结果状态，而不需要到处手写巨大的 Trace struct literal。
func NewTrace(traceID, feature string, timestamp time.Time, options ...TraceOption) Trace {
	return ApplyTraceOptions(Trace{
		TraceID:   traceID,
		Feature:   feature,
		Timestamp: timestamp,
	}, options...)
}

// ApplyTraceOptions 在已有 trace 基础上派生新 trace。
//
// 函数入口和出口都会复制 slice/pointer 字段，防止调用方共享可变底层数据。
// 对 AI trace 来说，这一点很关键：一个请求的观测记录一旦生成，就应当能作为
// 可靠证据用于 eval、排障和成本复盘，而不是被后续步骤悄悄改写。
func ApplyTraceOptions(base Trace, options ...TraceOption) Trace {
	trace := cloneTraceValue(base)
	for _, option := range options {
		if option == nil {
			continue
		}
		trace = cloneTraceValue(option(cloneTraceValue(trace)))
	}
	return cloneTraceValue(trace)
}

// WithTenant 设置多租户和会话上下文。
//
// 这些字段是排查“某个租户/用户是否异常消耗 token 或频繁失败”的基础维度。
func WithTenant(tenantID, userID, sessionID string) TraceOption {
	return func(trace Trace) Trace {
		trace.TenantID = tenantID
		trace.UserID = userID
		trace.SessionID = sessionID
		return trace
	}
}

// WithQuery 设置安全查询摘要。
//
// 普通 trace 只记录 query hash、语言和长度，不保存原文，避免身份证号、手机号、
// 账号等敏感内容进入普通日志或观测平台。
func WithQuery(queryHash, queryLang string, queryLen int) TraceOption {
	return func(trace Trace) Trace {
		trace.QueryHash = queryHash
		trace.QueryLang = queryLang
		trace.QueryLen = queryLen
		return trace
	}
}

// WithModel 设置本次调用使用的模型标识。
func WithModel(model string) TraceOption {
	return func(trace Trace) Trace {
		trace.Model = model
		return trace
	}
}

// WithPrompt 设置 prompt as code 的版本和渲染 hash。
//
// prompt 版本用于回溯“哪版模板导致行为变化”，prompt hash 用于证明具体渲染内容
// 是否变化；普通 trace 仍然不记录完整 prompt 原文。
func WithPrompt(templateVersion, promptHash string) TraceOption {
	return func(trace Trace) Trace {
		trace.PromptTemplateVer = templateVersion
		trace.PromptHash = promptHash
		return trace
	}
}

// WithUsage 设置 token 用量。
//
// reasoning/cache token 后续会影响成本和模型选择策略，因此即使 P0 只是本地闭环，
// 也先把字段收敛到统一 trace 入口。
func WithUsage(inputTokens, outputTokens, reasoningTokens int) TraceOption {
	return func(trace Trace) Trace {
		trace.InputTokens = inputTokens
		trace.OutputTokens = outputTokens
		trace.ReasoningTokens = reasoningTokens
		return trace
	}
}

// WithCacheUsage 设置缓存命中读写 token 统计。
func WithCacheUsage(readTokens, writeTokens int) TraceOption {
	return func(trace Trace) Trace {
		trace.CacheReadTokens = readTokens
		trace.CacheWriteTokens = writeTokens
		return trace
	}
}

// WithTemperature 设置生成温度，用于复盘输出稳定性。
func WithTemperature(temperature float64) TraceOption {
	return func(trace Trace) Trace {
		trace.Temperature = temperature
		return trace
	}
}

// WithLatency 设置首 token 延迟和总延迟。
func WithLatency(ttftMs, totalLatencyMs int64) TraceOption {
	return func(trace Trace) Trace {
		trace.TTFTMs = ttftMs
		trace.TotalLatencyMs = totalLatencyMs
		return trace
	}
}

// WithRetrieval 设置 RAG 检索摘要。
//
// topScores 是 slice，必须复制后写入 trace；否则调用方后续复用同一个切片做排序、
// 归一化或追加，会污染已经记录下来的检索证据。
func WithRetrieval(chunksRetrieved int, queryRewrittenHash string, topScores []float64, retrievalLatencyMs int64) TraceOption {
	return func(trace Trace) Trace {
		trace.ChunksRetrieved = chunksRetrieved
		trace.QueryRewrittenHash = queryRewrittenHash
		trace.TopScores = cloneFloat64s(topScores)
		trace.RetrievalLatencyMs = retrievalLatencyMs
		return trace
	}
}

// WithCost 设置本次请求的成本字段。
func WithCost(costUSD float64) TraceOption {
	return func(trace Trace) Trace {
		trace.CostUSD = costUSD
		return trace
	}
}

// WithOutcome 设置结果状态，例如 success、failed、degraded。
func WithOutcome(status string) TraceOption {
	return func(trace Trace) Trace {
		trace.OutcomeStatus = status
		return trace
	}
}

// WithFeedback 设置人工反馈和自动评估分。
//
// 指针字段也要复制，避免调用方复用局部变量时把历史 trace 的反馈值一起改掉。
func WithFeedback(userRating *int, autoEvalScore *float64) TraceOption {
	return func(trace Trace) Trace {
		trace.UserRating = cloneIntPointer(userRating)
		trace.AutoEvalScore = cloneFloat64Pointer(autoEvalScore)
		return trace
	}
}

func cloneTraceValue(trace Trace) Trace {
	cloned := trace
	cloned.TopScores = cloneFloat64s(trace.TopScores)
	cloned.UserRating = cloneIntPointer(trace.UserRating)
	cloned.AutoEvalScore = cloneFloat64Pointer(trace.AutoEvalScore)
	return cloned
}
