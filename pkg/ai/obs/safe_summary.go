package obs

// SafeSummary 是普通观测记录可携带的低敏诊断摘要。
//
// 它只表达 hash、长度、分类、数量、分数、状态和错误分类，不提供 raw/content
// 字段。这样 AI 语义观测、基础 span 和 eval evidence 都能保留诊断价值，同时
// 避免把用户原文、完整 prompt 或 tool args 带入普通 trace。
type SafeSummary struct {
	Hash       string   `json:"hash,omitempty"`
	Length     int      `json:"length,omitempty"`
	Category   string   `json:"category,omitempty"`
	Count      int      `json:"count,omitempty"`
	Score      *float64 `json:"score,omitempty"`
	Status     string   `json:"status,omitempty"`
	ErrorClass string   `json:"error_class,omitempty"`
}

// SafeSummaryOption 描述一次不可变安全摘要更新。
type SafeSummaryOption func(SafeSummary) SafeSummary

// NewSafeSummary 创建一条安全摘要。
func NewSafeSummary(options ...SafeSummaryOption) SafeSummary {
	return ApplySafeSummaryOptions(SafeSummary{}, options...)
}

// ApplySafeSummaryOptions 在已有摘要上派生新摘要。
//
// Score 是指针字段，入口和出口各复制一次，避免调用方通过共享指针修改已经生成的
// 观测证据。各 option 只接收值副本，不应保留外部可变引用。
func ApplySafeSummaryOptions(base SafeSummary, options ...SafeSummaryOption) SafeSummary {
	summary := cloneSafeSummary(base)
	for _, option := range options {
		if option == nil {
			continue
		}
		summary = option(summary)
	}
	return cloneSafeSummary(summary)
}

// WithSummaryHash 设置内容身份 hash。
func WithSummaryHash(hash string) SafeSummaryOption {
	return func(summary SafeSummary) SafeSummary {
		summary.Hash = hash
		return summary
	}
}

// WithSummaryLength 设置原始内容长度，而不是保存原文。
func WithSummaryLength(length int) SafeSummaryOption {
	return func(summary SafeSummary) SafeSummary {
		summary.Length = length
		return summary
	}
}

// WithSummaryCategory 设置语言、类型或安全分类。
func WithSummaryCategory(category string) SafeSummaryOption {
	return func(summary SafeSummary) SafeSummary {
		summary.Category = category
		return summary
	}
}

// WithSummaryCount 设置数量型摘要，例如检索 chunk 数或工具调用次数。
func WithSummaryCount(count int) SafeSummaryOption {
	return func(summary SafeSummary) SafeSummary {
		summary.Count = count
		return summary
	}
}

// WithSummaryScore 设置单值分数摘要。
func WithSummaryScore(score float64) SafeSummaryOption {
	return func(summary SafeSummary) SafeSummary {
		summary.Score = cloneFloat64Pointer(&score)
		return summary
	}
}

// WithSummaryStatus 设置阶段状态。
func WithSummaryStatus(status string) SafeSummaryOption {
	return func(summary SafeSummary) SafeSummary {
		summary.Status = status
		return summary
	}
}

// WithSummaryErrorClass 设置稳定错误分类。
func WithSummaryErrorClass(errorClass string) SafeSummaryOption {
	return func(summary SafeSummary) SafeSummary {
		summary.ErrorClass = errorClass
		return summary
	}
}

func cloneSafeSummary(summary SafeSummary) SafeSummary {
	cloned := summary
	cloned.Score = cloneFloat64Pointer(summary.Score)
	return cloned
}
