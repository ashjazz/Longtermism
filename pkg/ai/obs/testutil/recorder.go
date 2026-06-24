// Package testutil 提供 obs 包的测试工具。
//
// Recorder 是内存版 Tracer：它不写日志、不连平台，只把 trace 存在内存里。
// 这样测试可以稳定断言“记录了什么”，不需要依赖 LangFuse、OTEL 或真实日志后端。
package testutil

import (
	"context"
	"sync"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

// Recorder 记录测试期间产生的 trace。
//
// 生产 tracer 往往会异步写入日志、OTEL 或 LangFuse；测试里如果也这样做，
// 会让断言变慢且不稳定。Recorder 用 mutex 包住内存 slice，专门服务于并发单测和契约测试。
type Recorder struct {
	mu     sync.RWMutex
	traces []obs.Trace
}

// NewRecorder 创建空的内存 trace recorder。
func NewRecorder() *Recorder {
	return &Recorder{}
}

// Record 实现 obs.Tracer。
//
// 这里忽略 ctx，是因为 Recorder 不做 IO，也不存在可取消的外部写入。
// 真实 tracer 实现如果写网络或文件，应尊重 ctx，避免请求取消后继续阻塞热路径。
func (r *Recorder) Record(_ context.Context, trace obs.Trace) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.traces = append(r.traces, cloneTrace(trace))
}

// Count 返回当前记录数量。
func (r *Recorder) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.traces)
}

// Traces 返回 trace 快照副本。
//
// 返回副本是为了防止测试调用方修改内部 slice，造成后续断言被污染。
// 这和 AI pipeline 中“不要把可变内部状态暴露给上层”的原则一致。
func (r *Recorder) Traces() []obs.Trace {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cloned := make([]obs.Trace, len(r.traces))
	for i, trace := range r.traces {
		cloned[i] = cloneTrace(trace)
	}
	return cloned
}

// AssertCount 是测试辅助断言，失败时直接标记调用方测试失败。
func (r *Recorder) AssertCount(t *testing.T, want int) {
	t.Helper()

	if got := r.Count(); got != want {
		t.Fatalf("trace count = %d, want %d", got, want)
	}
}

// AssertTrace 对指定位置的 trace 执行调用方提供的字段断言。
//
// 这样做比暴露内部 slice 更安全，也能让测试以“我要断言哪些字段”的方式阅读。
func (r *Recorder) AssertTrace(t *testing.T, index int, assert func(t *testing.T, trace obs.Trace)) {
	t.Helper()

	trace, ok := r.traceAt(index)
	if !ok {
		t.Fatalf("trace index %d out of range, count = %d", index, r.Count())
	}
	assert(t, trace)
}

func (r *Recorder) traceAt(index int) (obs.Trace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if index < 0 || index >= len(r.traces) {
		return obs.Trace{}, false
	}
	return cloneTrace(r.traces[index]), true
}

func cloneTrace(trace obs.Trace) obs.Trace {
	cloned := trace
	cloned.TopScores = append([]float64(nil), trace.TopScores...)

	if trace.UserRating != nil {
		userRating := *trace.UserRating
		cloned.UserRating = &userRating
	}
	if trace.AutoEvalScore != nil {
		autoEvalScore := *trace.AutoEvalScore
		cloned.AutoEvalScore = &autoEvalScore
	}

	return cloned
}
