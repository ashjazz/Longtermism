package testutil

import (
	"encoding/json"
	"sync"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

// SpanSnapshot 是基础设施 span 或 OTel-style adapter 的稳定测试快照。
//
// 它只保存低敏关联身份、span 关系、字符串属性和 SafeSummary；不提供 raw payload
// 字段，避免测试工具把用户原文、完整 prompt 或 tool args 变成默认观测路径。
type SpanSnapshot struct {
	Name            string
	RequestID       string
	ServiceTraceID  string
	SpanID          string
	ParentSpanID    string
	ObservationType obs.ObservationType
	Attributes      map[string]string
	Summaries       map[string]obs.SafeSummary
}

// MemorySpanSink 是 span snapshot 的并发安全内存 sink。
//
// 它用于默认离线测试，不连接真实 collector 或平台。后续 OTel/Langfuse adapter
// 可以把自身输出映射成 SpanSnapshot，再复用这套断言。
type MemorySpanSink struct {
	mu        sync.RWMutex
	snapshots []SpanSnapshot
}

// NewMemorySpanSink 创建空的内存 span sink。
func NewMemorySpanSink() *MemorySpanSink {
	return &MemorySpanSink{}
}

// Record 写入一条 span snapshot。
func (s *MemorySpanSink) Record(snapshot SpanSnapshot) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.snapshots = append(s.snapshots, cloneSpanSnapshot(snapshot))
}

// Snapshots 返回按写入顺序排列的快照副本。
func (s *MemorySpanSink) Snapshots() []SpanSnapshot {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	cloned := make([]SpanSnapshot, len(s.snapshots))
	for index, snapshot := range s.snapshots {
		cloned[index] = cloneSpanSnapshot(snapshot)
	}
	return cloned
}

// RawPayload 返回可被隐私测试扫描的 JSON payload。
func (s *MemorySpanSink) RawPayload() string {
	payload, err := json.Marshal(s.Snapshots())
	if err != nil {
		return ""
	}
	return string(payload)
}

func cloneSpanSnapshot(snapshot SpanSnapshot) SpanSnapshot {
	cloned := snapshot
	cloned.Attributes = cloneStringMap(snapshot.Attributes)
	cloned.Summaries = cloneSafeSummaryMap(snapshot.Summaries)
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneSafeSummaryMap(values map[string]obs.SafeSummary) map[string]obs.SafeSummary {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]obs.SafeSummary, len(values))
	for key, value := range values {
		cloned[key] = obs.NewSafeSummary(
			obs.WithSummaryHash(value.Hash),
			obs.WithSummaryLength(value.Length),
			obs.WithSummaryCategory(value.Category),
			obs.WithSummaryCount(value.Count),
			cloneSummaryScoreOption(value),
			obs.WithSummaryStatus(value.Status),
			obs.WithSummaryErrorClass(value.ErrorClass),
		)
	}
	return cloned
}

func cloneSummaryScoreOption(summary obs.SafeSummary) obs.SafeSummaryOption {
	if summary.Score == nil {
		return nil
	}
	return obs.WithSummaryScore(*summary.Score)
}
