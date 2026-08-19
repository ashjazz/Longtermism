package backend

import (
	"context"
	"encoding/json"
	"testing"
)

// T130a 契约测试：快照后端只消费窄查询端口，测试用 canned Prometheus
// 响应覆盖三组件全量快照、空向量基线、小数/畸形拒绝与目录一致性。

type fakeCollectorInstantQuery struct {
	results map[string]BackendQueryResult
	errs    map[string]error
	calls   []string
}

func (f *fakeCollectorInstantQuery) QueryPrometheus(_ context.Context, expression string) (BackendQueryResult, error) {
	f.calls = append(f.calls, expression)
	if err, ok := f.errs[expression]; ok {
		return BackendQueryResult{}, err
	}
	result, ok := f.results[expression]
	if !ok {
		return BackendQueryResult{}, errMalformedCollectorSnapshot
	}
	return result, nil
}

func vectorResult(value string) BackendQueryResult {
	payload := json.RawMessage(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"exporter":"otlp/tempo"},"value":[1786684030,"` + value + `"]}]}}`)
	return BackendQueryResult{payload: payload}
}

func emptyVectorResult() BackendQueryResult {
	payload := json.RawMessage(`{"status":"success","data":{"resultType":"vector","result":[]}}`)
	return BackendQueryResult{payload: payload}
}

// 每个组件 5 个指标：sent/send_failed/enqueue_failed/queue_size/queue_capacity。
func collectorSnapshotFixture() map[string]BackendQueryResult {
	results := map[string]BackendQueryResult{}
	for _, component := range []struct {
		id, signal string
	}{
		{"otlp/tempo", "spans"},
		{"otlphttp/loki", "log_records"},
		{"otlphttp/langfuse", "spans"},
	} {
		for metric, value := range map[string]string{
			"otelcol_exporter_sent_" + component.signal + "_total":           "100",
			"otelcol_exporter_send_failed_" + component.signal + "_total":    "12",
			"otelcol_exporter_enqueue_failed_" + component.signal + "_total": "3",
			"otelcol_exporter_queue_size":                                    "7",
			"otelcol_exporter_queue_capacity":                                "10000",
		} {
			results[metric+"{exporter=\""+component.id+"\"}"] = vectorResult(value)
		}
	}
	return results
}

func TestCollectorSnapshotBackendCoversThreeRealComponents(t *testing.T) {
	client := &fakeCollectorInstantQuery{results: collectorSnapshotFixture()}
	backend := NewGrafanaCollectorSnapshotBackend(client)

	snapshots, err := backend.SnapshotCollectorHealth(context.Background())
	if err != nil {
		t.Fatalf("SnapshotCollectorHealth() = err %v", err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("snapshots = %d, want 3（目录声明的全部真实出口）", len(snapshots))
	}
	byComponent := make(map[string]int64, 3)
	componentIDs := make([]string, 0, 3)
	for _, snapshot := range snapshots {
		componentIDs = append(componentIDs, snapshot.ComponentID)
		byComponent[snapshot.ComponentID] = snapshot.SendFailed
	}
	for _, want := range []string{"otlp/tempo", "otlphttp/loki", "otlphttp/langfuse"} {
		if byComponent[want] != 12 {
			t.Errorf("component %q send_failed = %d, want 12；组件集合 %v", want, byComponent[want], componentIDs)
		}
	}
}

func TestCollectorSnapshotBackendEmptyVectorDecodesToZero(t *testing.T) {
	results := collectorSnapshotFixture()
	for expression := range results {
		results[expression] = emptyVectorResult()
	}
	client := &fakeCollectorInstantQuery{results: results}
	backend := NewGrafanaCollectorSnapshotBackend(client)

	snapshots, err := backend.SnapshotCollectorHealth(context.Background())
	if err != nil {
		t.Fatalf("SnapshotCollectorHealth() = err %v, want 空向量基线为 0", err)
	}
	for _, snapshot := range snapshots {
		if snapshot.Sent != 0 || snapshot.SendFailed != 0 || snapshot.QueueCapacity != 0 {
			t.Errorf("component %q 空向量基线非零: %+v", snapshot.ComponentID, snapshot)
		}
	}
}

func TestCollectorSnapshotBackendRejectsFractionalSamples(t *testing.T) {
	results := collectorSnapshotFixture()
	for expression := range results {
		results[expression] = vectorResult("1.5")
	}
	client := &fakeCollectorInstantQuery{results: results}
	backend := NewGrafanaCollectorSnapshotBackend(client)

	if _, err := backend.SnapshotCollectorHealth(context.Background()); err == nil {
		t.Fatal("SnapshotCollectorHealth() = nil error, want 小数样本拒绝")
	}
}

func TestCollectorSnapshotBackendRejectsMalformedPayload(t *testing.T) {
	results := collectorSnapshotFixture()
	for expression := range results {
		payload := json.RawMessage(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"exporter":"x"},"value":["bad"]}]}}`)
		results[expression] = BackendQueryResult{payload: payload}
	}
	client := &fakeCollectorInstantQuery{results: results}
	backend := NewGrafanaCollectorSnapshotBackend(client)

	if _, err := backend.SnapshotCollectorHealth(context.Background()); err == nil {
		t.Fatal("SnapshotCollectorHealth() = nil error, want 畸形 payload 拒绝")
	}
}

func TestCollectorSnapshotBackendQueueSnapshotScopesToTempoComponent(t *testing.T) {
	client := &fakeCollectorInstantQuery{results: collectorSnapshotFixture()}
	backend := NewGrafanaCollectorSnapshotBackend(client)

	snapshot, err := backend.SnapshotCollectorQueue(context.Background())
	if err != nil {
		t.Fatalf("SnapshotCollectorQueue() = err %v", err)
	}
	if snapshot.ComponentID != "otlp/tempo" {
		t.Errorf("ComponentID = %q, want otlp/tempo（T130 full aggregate 固定目标）", snapshot.ComponentID)
	}
	if snapshot.Sent != 100 || snapshot.QueueCapacity != 10000 {
		t.Errorf("queue snapshot = %+v, want sent 100 capacity 10000", snapshot)
	}
	// 查询表达式必须带 exporter selector，禁止全量聚合误读其它组件。
	for _, call := range client.calls {
		if call == "otelcol_exporter_sent_spans_total" || call == "otelcol_exporter_queue_size" {
			t.Errorf("查询缺少 exporter selector: %q", call)
		}
	}
}
