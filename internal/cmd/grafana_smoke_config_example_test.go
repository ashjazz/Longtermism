package cmd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gogf/gf/v2/os/gcfg"
)

// TestGrafanaSmokeConfigExampleIsStandaloneAndLoopbackBound prevents the local runbook from
// drifting into a partial override that GoFrame would never load. The selected file must contain
// every safety-critical value needed for a local smoke run, including a loopback-only HTTP entry.
func TestGrafanaSmokeConfigExampleIsStandaloneAndLoopbackBound(t *testing.T) {
	t.Parallel()

	adapter, err := gcfg.NewAdapterFile(filepath.Join("..", "..", "manifest", "config", "config.grafana-smoke.example.yaml"))
	if err != nil {
		t.Fatalf("load Grafana smoke configuration example: %v", err)
	}
	config := gcfg.NewWithAdapter(adapter)

	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "application only listens on loopback", key: "server.address", want: "127.0.0.1:8000"},
		{name: "observability is enabled", key: "observability.enabled", want: "true"},
		{name: "collector mode is selected", key: "observability.mode", want: "collector"},
		{name: "application only knows the local collector", key: "observability.collector.endpoint", want: "127.0.0.1:4317"},
		{name: "traces are enabled", key: "observability.signals.traces_enabled", want: "true"},
		{name: "metrics are enabled", key: "observability.signals.metrics_enabled", want: "true"},
		{name: "smoke route is explicitly enabled", key: "observability.smoke.enabled", want: "true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.MustGet(context.Background(), tt.key).String()
			if got != tt.want {
				t.Fatalf("%s = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
