package chat

import (
	"context"
	"time"

	appobs "github.com/ashjazz/Longtermism/internal/observability"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

// ChatAIExecutionBoundary 选择并标记真实 OTel root/bridge，同时返回从 native
// SpanContext 派生的身份。logic 只消费端口，不创建或猜测平台 trace/span ID。
type ChatAIExecutionBoundary interface {
	Start(
		context.Context,
		obs.CorrelationIdentity,
	) (
		context.Context,
		obs.CorrelationIdentity,
		appobs.EndChatAIExecution,
		error,
	)
}

// ChatGenerationObserver 把已确认的 generation 事实交给 T092 adapter。
type ChatGenerationObserver interface {
	RecordGeneration(context.Context, appobs.GenerationSpanInput) (appobs.PlatformSpanIdentity, error)
}

// ChatEvaluatorObserver 把 T094 生成的 evidence 交给 T092 evaluator span adapter。
type ChatEvaluatorObserver interface {
	RecordEvaluator(context.Context, appobs.EvaluatorSpanInput) (appobs.PlatformSpanIdentity, error)
}

// ChatEvidenceStore 是 T093 本地事实源的消费侧窄端口。
type ChatEvidenceStore interface {
	Append(context.Context, aieval.EvaluationEvidence) error
}

// ChatScoreProjectionInput 只组合已持久化 evidence 与 generation adapter 返回的
// 原生平台身份。未来平台 adapter 负责映射，usecase 不认识 Langfuse schema。
type ChatScoreProjectionInput struct {
	// RunID is present only for an authenticated live-smoke request. It is never
	// derived from EvalRunID or timestamps; ordinary chat leaves it empty.
	RunID      string
	Evidence   aieval.EvaluationEvidence
	Generation appobs.PlatformSpanIdentity
}

// ChatScoreProjectionQueue 只允许同步、立即返回的有界入队。worker 的重试、状态机
// 与 shutdown 属于基础设施层，usecase 不创建 goroutine。
type ChatScoreProjectionQueue interface {
	TryEnqueue(context.Context, ChatScoreProjectionInput) error
}

// AIPlaneFactRecorder 在 AI 执行事实真实发生后（AI 桥接 span 已创建）登记一次
// 有界发射事实，供 infra smoke 的 marker-count AI-negative 查询读取。usecase 只在
// 受信任的 smoke marker 存在时调用；实现必须线程安全、有界且不返回错误（旁路语义）。
type AIPlaneFactRecorder interface {
	RecordAIPlaneFact(marker string, at time.Time)
}
