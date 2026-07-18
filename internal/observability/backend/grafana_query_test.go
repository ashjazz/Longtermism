package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestGrafanaQueryClientContract 先固定四种只读查询的安全失败边界。真实 smoke
// 需要区分“后端没有数据”和“查询后端失败”，因此错误只能暴露 backend 与稳定类别，
// 绝不能把 URL、认证头或响应 body 重新带回 report/log。
func TestGrafanaQueryClientContract(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		status    int
		body      string
		block     bool
		query     func(context.Context, *GrafanaQueryClient) error
		wantError string
	}{
		{
			name:   "Prometheus success",
			path:   "/api/v1/query",
			status: http.StatusOK,
			body:   `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			query: func(ctx context.Context, client *GrafanaQueryClient) error {
				_, err := client.QueryPrometheus(ctx, `up{job="collector"}`)
				return err
			},
		},
		{
			name:   "Loki malformed response",
			path:   "/loki/api/v1/query_range",
			status: http.StatusOK,
			body:   `{`,
			query: func(ctx context.Context, client *GrafanaQueryClient) error {
				_, err := client.QueryLoki(ctx, `{service_name="longtermism"}`)
				return err
			},
			wantError: "loki:malformed_response",
		},
		{
			name:   "Tempo server failure",
			path:   "/api/search",
			status: http.StatusBadGateway,
			body:   `upstream token=should-not-leak`,
			query: func(ctx context.Context, client *GrafanaQueryClient) error {
				_, err := client.QueryTempo(ctx, `resource.service.name = "longtermism"`)
				return err
			},
			wantError: "tempo:backend_unavailable",
		},
		{
			name:   "Grafana datasource authentication failure",
			path:   "/api/datasources/uid/prometheus/health",
			status: http.StatusUnauthorized,
			body:   `authorization rejected`,
			query: func(ctx context.Context, client *GrafanaQueryClient) error {
				_, err := client.QueryGrafanaDatasourceHealth(ctx, "prometheus")
				return err
			},
			wantError: "grafana:authentication_failed",
		},
		{
			name:   "Prometheus timeout",
			path:   "/api/v1/query",
			status: http.StatusOK,
			body:   `{"status":"success"}`,
			block:  true,
			query: func(ctx context.Context, client *GrafanaQueryClient) error {
				deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				_, err := client.QueryPrometheus(deadline, "up")
				return err
			},
			wantError: "prometheus:backend_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", request.URL.Path, tt.path)
				}
				if tt.block {
					<-request.Context().Done()
					return
				}
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewGrafanaQueryClient(GrafanaQueryConfig{
				PrometheusURL: server.URL,
				LokiURL:       server.URL,
				TempoURL:      server.URL,
				GrafanaURL:    server.URL,
				Timeout:       time.Second,
			})
			err := tt.query(context.Background(), client)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("query error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("query error = %v, want %q", err, tt.wantError)
			}
			for _, forbidden := range []string{"should-not-leak", "authorization", server.URL} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("query error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestGrafanaQueryClientRejectsOldResultWindow(t *testing.T) {
	client := NewGrafanaQueryClient(GrafanaQueryConfig{PrometheusURL: "http://127.0.0.1", Timeout: time.Second})
	_, err := client.QueryPrometheusSince(context.Background(), "up", time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute))
	if !errors.Is(err, ErrStaleQueryWindow) {
		t.Fatalf("QueryPrometheusSince() error = %v, want ErrStaleQueryWindow", err)
	}
}
