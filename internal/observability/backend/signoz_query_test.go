package backend

// T138：SigNoz 只读查询客户端的 RED 契约测试。
// 实现前本文件必须编译失败（NewSignozQueryClient 等符号不存在），即因目标能力缺失而失败；
// T143 在 signoz_query.go 落地实现后本文件转 GREEN。
//
// 契约沿用 Grafana 主线客户端的安全边界（grafana_query.go）：
//   1. 只读 GET、响应体大小有界、JSON 合法性校验，防止故障后端把诊断路径变成内存或敏感内容汇聚点；
//   2. 错误只暴露 backend 名与稳定类别（BackendQueryError），永不携带 URL、认证头或响应 body；
//   3. Since 变体拒绝旧查询窗口（ErrStaleQueryWindow），防止历史 run 的数据满足当前 run 的验收。
// SigNoz 专属边界：查询认证使用 ingestion key（X-Signoz-Api-Key），
// key 只允许出现在请求头，任何错误/报告通道都不得回显它的值。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// signozIngestionKeyCanary 是合成 canary：若实现把 key 写进错误、日志或结果，
// 下方断言会立即失败。生产代码绝不能出现真实 key 值。
const signozIngestionKeyCanary = "signoz-canary-key-should-not-leak"

// TestSignozQueryClientContract 固定三信号（logs/metrics/traces）查询的安全失败边界。
// 真实 E2E（T139/T144）需要区分“SigNoz 没有数据”和“查询 SigNoz 失败”，
// 因此错误必须可分类，且认证失败、后端故障、超时、畸形响应各自对应稳定类别。
func TestSignozQueryClientContract(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		status    int
		body      string
		block     bool
		query     func(context.Context, *SignozQueryClient) error
		wantError string
	}{
		{
			name:   "Metrics instant query success",
			path:   "/api/v1/query",
			status: http.StatusOK,
			body:   `{"status":"ok","data":{"result":[]}}`,
			query: func(ctx context.Context, client *SignozQueryClient) error {
				_, err := client.QueryMetrics(ctx, `signoz_container_cpu_usage`)
				return err
			},
		},
		{
			name:   "Logs query success",
			path:   "/api/v1/logs",
			status: http.StatusOK,
			body:   `{"status":"ok","data":{"records":[]}}`,
			query: func(ctx context.Context, client *SignozQueryClient) error {
				_, err := client.QueryLogs(ctx, `service.name="longtermism"`)
				return err
			},
		},
		{
			name:   "Traces query success",
			path:   "/api/v1/traces",
			status: http.StatusOK,
			body:   `{"status":"ok","data":[{"trace_id":"0af7651916cd43dd8448eb211c80319c"}]}`,
			query: func(ctx context.Context, client *SignozQueryClient) error {
				_, err := client.QueryTraces(ctx, `service.name = "longtermism"`)
				return err
			},
		},
		{
			name:   "Logs malformed response",
			path:   "/api/v1/logs",
			status: http.StatusOK,
			body:   `{`,
			query: func(ctx context.Context, client *SignozQueryClient) error {
				_, err := client.QueryLogs(ctx, `service.name="longtermism"`)
				return err
			},
			wantError: "signoz:malformed_response",
		},
		{
			name:   "Traces server failure with ingestion key in body",
			path:   "/api/v1/traces",
			status: http.StatusBadGateway,
			body:   `upstream rejected key=` + signozIngestionKeyCanary,
			query: func(ctx context.Context, client *SignozQueryClient) error {
				_, err := client.QueryTraces(ctx, `service.name = "longtermism"`)
				return err
			},
			wantError: "signoz:backend_unavailable",
		},
		{
			name:   "Metrics authentication failure",
			path:   "/api/v1/query",
			status: http.StatusUnauthorized,
			body:   `authentication rejected`,
			query: func(ctx context.Context, client *SignozQueryClient) error {
				_, err := client.QueryMetrics(ctx, `up`)
				return err
			},
			wantError: "signoz:authentication_failed",
		},
		{
			name:   "Traces timeout",
			path:   "/api/v1/traces",
			status: http.StatusOK,
			body:   `{"status":"ok","data":[]}`,
			block:  true,
			query: func(ctx context.Context, client *SignozQueryClient) error {
				deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				_, err := client.QueryTraces(deadline, `service.name = "longtermism"`)
				return err
			},
			wantError: "signoz:backend_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", request.URL.Path, tt.path)
				}
				// ingestion key 只允许出现在请求头：认证通道必须携带，错误通道绝不能回显。
				if got := request.Header.Get("X-Signoz-Api-Key"); got != signozIngestionKeyCanary {
					t.Errorf("X-Signoz-Api-Key = %q, want canary key", got)
				}
				if tt.block {
					<-request.Context().Done()
					return
				}
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewSignozQueryClient(SignozQueryConfig{
				SignozURL:    server.URL,
				IngestionKey: signozIngestionKeyCanary,
				Timeout:      time.Second,
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
			// 错误通道隐私断言：类别、URL 与 ingestion key 都不允许出现。
			for _, forbidden := range []string{signozIngestionKeyCanary, "authorization", server.URL} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("query error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

// TestSignozQueryClientRejectsOldResultWindow 防止旧 run 的 marker 满足当前 run 的验收：
// 备选 profile 的 E2E 与主线一样必须使用限定窗口，SigNoz 接受任意历史范围不等于可以照单全收。
func TestSignozQueryClientRejectsOldResultWindow(t *testing.T) {
	client := NewSignozQueryClient(SignozQueryConfig{SignozURL: "http://127.0.0.1", Timeout: time.Second})
	_, err := client.QueryMetricsSince(context.Background(), "up", time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute))
	if !errors.Is(err, ErrStaleQueryWindow) {
		t.Fatalf("QueryMetricsSince() error = %v, want ErrStaleQueryWindow", err)
	}
	_, err = client.QueryLogsSince(context.Background(), `service.name="longtermism"`, time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute))
	if !errors.Is(err, ErrStaleQueryWindow) {
		t.Fatalf("QueryLogsSince() error = %v, want ErrStaleQueryWindow", err)
	}
	_, err = client.QueryTracesSince(context.Background(), `service.name = "longtermism"`, time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute))
	if !errors.Is(err, ErrStaleQueryWindow) {
		t.Fatalf("QueryTracesSince() error = %v, want ErrStaleQueryWindow", err)
	}
}

func TestSignozQueryClientSafetyGuards(t *testing.T) {
	t.Run("does not follow backend redirects", func(t *testing.T) {
		redirected := false
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
		defer target.Close()
		source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, target.URL, http.StatusFound)
		}))
		defer source.Close()
		client := NewSignozQueryClient(SignozQueryConfig{SignozURL: source.URL, Timeout: time.Second})
		if _, err := client.QueryMetrics(context.Background(), "up"); err == nil || err.Error() != "signoz:backend_unavailable" || redirected {
			t.Fatalf("redirect result = %v, redirected = %v", err, redirected)
		}
	})

	t.Run("bounds configuration inputs and output", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"status":"ok","data":{}}`))
		}))
		defer server.Close()
		client := NewSignozQueryClient(SignozQueryConfig{SignozURL: server.URL, Timeout: time.Hour})
		if client.timeout != maximumBackendQueryTimeout {
			t.Fatalf("timeout = %s, want %s", client.timeout, maximumBackendQueryTimeout)
		}
		// 超长查询直接拒绝，防止把后端查询语言当作无界输入通道。
		if _, err := client.QueryLogs(context.Background(), strings.Repeat("x", maximumQueryLength+1)); err == nil || err.Error() != "signoz:invalid_query" {
			t.Fatalf("oversize query error = %v", err)
		}
		for _, backendURL := range []string{"ftp://127.0.0.1", "http://user:password@127.0.0.1"} {
			unsafeClient := NewSignozQueryClient(SignozQueryConfig{SignozURL: backendURL, Timeout: time.Second})
			if _, err := unsafeClient.QueryMetrics(context.Background(), "up"); err == nil || err.Error() != "signoz:backend_unavailable" {
				t.Fatalf("unsafe backend URL %q error = %v", backendURL, err)
			}
		}
		result, err := client.QueryMetrics(context.Background(), "up")
		if err != nil {
			t.Fatalf("safe query error = %v", err)
		}
		// 原始响应可能含日志/链路内容，必须显式 Decode 低敏字段，不能整包进 report。
		if _, err := json.Marshal(result); err == nil {
			t.Fatal("BackendQueryResult must not be JSON serializable")
		}
	})

	t.Run("smoke variant rejects non-loopback endpoints", func(t *testing.T) {
		// 真实 smoke 客户端只接受显式 loopback 端点并在拨号时重验 DNS，
		// 使 CLI 配置无法变成 SSRF 原语（与 Grafana 主线同一条边界）。
		if _, err := NewSignozSmokeQueryClient(SignozQueryConfig{SignozURL: "http://example.com", Timeout: time.Second}); err == nil || err.Error() != "signoz:backend_unavailable" {
			t.Fatalf("non-loopback smoke client error = %v, want signoz:backend_unavailable", err)
		}
		loopback := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"status":"ok","data":{}}`))
		}))
		defer loopback.Close()
		client, err := NewSignozSmokeQueryClient(SignozQueryConfig{SignozURL: loopback.URL, Timeout: time.Second})
		if err != nil {
			t.Fatalf("loopback smoke client error = %v", err)
		}
		if _, err := client.QueryMetrics(context.Background(), "up"); err != nil {
			t.Fatalf("loopback smoke query error = %v", err)
		}
	})

	t.Run("ingestion key never appears in any error class", func(t *testing.T) {
		// 系统性断言：对每个稳定错误类别逐一触发，确认错误文本只有 backend:类别，
		// ingestion key 不存在于任何失败通道（T138 质量门控的核心隐私要求）。
		cases := []struct {
			name      string
			status    int
			body      string
			query     func(*SignozQueryClient) error
			wantError string
		}{
			{
				name:      "authentication failed",
				status:    http.StatusUnauthorized,
				body:      `rejected key=` + signozIngestionKeyCanary,
				wantError: "signoz:authentication_failed",
				query: func(client *SignozQueryClient) error {
					_, err := client.QueryMetrics(context.Background(), "up")
					return err
				},
			},
			{
				name:      "malformed response",
				status:    http.StatusOK,
				body:      `not-json ` + signozIngestionKeyCanary,
				wantError: "signoz:malformed_response",
				query: func(client *SignozQueryClient) error {
					_, err := client.QueryLogs(context.Background(), `service.name="x"`)
					return err
				},
			},
			{
				name:      "backend unavailable",
				status:    http.StatusInternalServerError,
				body:      `key=` + signozIngestionKeyCanary,
				wantError: "signoz:backend_unavailable",
				query: func(client *SignozQueryClient) error {
					_, err := client.QueryTraces(context.Background(), `service.name = "x"`)
					return err
				},
			},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.WriteHeader(tt.status)
					_, _ = writer.Write([]byte(tt.body))
				}))
				defer server.Close()
				client := NewSignozQueryClient(SignozQueryConfig{SignozURL: server.URL, IngestionKey: signozIngestionKeyCanary, Timeout: time.Second})
				if err := tt.query(client); err == nil || err.Error() != tt.wantError {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				} else if strings.Contains(err.Error(), signozIngestionKeyCanary) {
					t.Fatalf("error class %q leaked ingestion key: %v", tt.wantError, err)
				}
			})
		}
	})
}

// TestSignozQueryClientBoundsRangeQueries 固定 Since 变体的请求形状：
// 备选 profile 的验收证据必须限定在本次 run 的时间窗内并带有结果上限。
func TestSignozQueryClientBoundsRangeQueries(t *testing.T) {
	startedAt := time.Now().UTC().Add(-30 * time.Second)
	endedAt := time.Now().UTC()
	requests := make(map[string]url.Values)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests[request.URL.Path] = request.URL.Query()
		_, _ = writer.Write([]byte(`{"status":"ok","data":{}}`))
	}))
	defer server.Close()
	client := NewSignozQueryClient(SignozQueryConfig{SignozURL: server.URL, Timeout: time.Second})
	if _, err := client.QueryMetricsSince(context.Background(), "up", startedAt, endedAt); err != nil {
		t.Fatalf("QueryMetricsSince() error = %v", err)
	}
	if _, err := client.QueryLogsSince(context.Background(), `service.name="longtermism"`, startedAt, endedAt); err != nil {
		t.Fatalf("QueryLogsSince() error = %v", err)
	}
	if _, err := client.QueryTracesSince(context.Background(), `service.name = "longtermism"`, startedAt, endedAt); err != nil {
		t.Fatalf("QueryTracesSince() error = %v", err)
	}
	if got := requests["/api/v1/query_range"].Get("step"); got != backendQueryStep {
		t.Fatalf("metrics range step = %q, want %q", got, backendQueryStep)
	}
	for _, path := range []string{"/api/v1/logs", "/api/v1/traces"} {
		query := requests[path]
		if query.Get("start") == "" || query.Get("end") == "" || query.Get("limit") != backendQueryLimit {
			t.Fatalf("%s query = %v, want bounded start/end/limit", path, query)
		}
	}
}
