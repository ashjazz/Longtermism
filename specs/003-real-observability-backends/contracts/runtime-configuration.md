# Runtime Configuration Contract

## 1. Ownership

| Configuration owner | May contain | Must not contain |
| --- | --- | --- |
| Application | Collector endpoint/protocol, resource, signal flags, payload policy, smoke flag, LLM provider config names | Tempo/Loki/Prometheus/Grafana/SigNoz endpoints; Langfuse OTLP endpoint |
| Collector | backend endpoints, exporter headers from env/secret, pipelines, queues, sampling | application API key values in checked-in config |
| Backend profile | service images, ports, volumes, retention, health checks | application business config |
| Grafana provisioning | datasource internal URLs, dashboards, alerts | telemetry exporter credentials |
| Langfuse score adapter | base URL and public/secret key references | prompt/query/output raw values in failure logs |
| Smoke runner | local query base-URL and credential environment-variable references | committed secrets, credential values, default paid-service endpoints, or business payload |

## 2. Application shape

```yaml
app:
  environment: local
  is_debug: true

observability:
  enabled: true
  mode: collector
  resource:
    service_name: longtermism
    service_version: dev
  collector:
    protocol: grpc
    endpoint: otel-collector:4317
    insecure: true
    headers_env: OTEL_EXPORTER_OTLP_HEADERS
  signals:
    traces_enabled: true
    metrics_enabled: true
    logs_transport: otlp
    local_jsonl_enabled: false
  tracing:
    sampling_ratio: 1.0
  payload:
    mode: content_redacted
    raw_content_enabled: false
  sensitive_data:
    on_match: redact
  smoke:
    enabled: false

ai:
  llm:
    default_provider: openai
    providers:
      openai:
        base_url_env: OPENAI_BASE_URL
        api_key_env: OPENAI_API_KEY
        default_model: gpt-5.5
```

This is a shape contract, not a ready-to-use secret file. Checked-in defaults keep `observability.enabled=false`, `smoke.enabled=false`, and all secret values empty.

`enabled` 与 `mode` 不得形成两套可解释路径：当 `enabled=false` 且 mode 省略或为 `noop` 时，配置加载层规范化为 `mode=noop`；若使用者同时给出 `enabled=false` 与非 `noop` mode，则启动失败。`signals` 只决定 trace/metric provider 是否装配，`smoke.enabled` 只决定 infra-smoke 路由是否注册。

### Protected live chat smoke admission

`observability.smoke.enabled` 是 infra 与 live-chat smoke 的共同总开关；关闭时携带任一
chat smoke header 的请求必须在 handler/provider 前统一拒绝，普通 chat 行为不变。开启
live-chat smoke 还必须配置独立的短期 credential 引用（例如
`observability.smoke.chat.authorization_env`），运行时安全快照只保存引用名与
`credential_present`，绝不保存 credential 值。

- 仅接受 loopback peer；代理头不能把远端请求伪装成本机请求。
- `X-Observability-Smoke-Run-ID` 与 `X-Observability-Smoke-Authorization` 必须同时出现；
  任一出现即进入受保护 admission，不能降级为普通 chat。
- 短期共享 credential 使用恒时比较但不由单次请求消费；经鉴权的 marker 才被原子地
  一次性消费。相同 secret 可服务多个不同 run，而同一 marker 的并发或串行 replay
  最多一个请求进入 handler。registry 必须有明确 TTL/容量，不能成为无界身份存储。
- disabled、remote、缺字段、非法 marker、错误 credential 与 replay 使用同一低敏拒绝
  语义，避免暴露开关或 credential oracle；错误、日志与配置快照不得包含输入值。
- admission 成功后立即删除 authorization header，只把已验证 marker 放入本地 context；
  auth secret 不进入 DTO、command、baggage、span、log、metric、manifest 或 report。

## 3. Fail-fast matrix

| Condition | Required behavior |
| --- | --- |
| mode is `noop` | no exporter, server may start |
| mode is `local` | only local/test sinks, no network |
| `enabled=false` with a non-`noop` mode | startup error; configuration must not silently choose one |
| mode is `collector` without endpoint | startup error |
| unsupported protocol | startup error |
| chat enabled without model base URL/key/model | startup error |
| `content_raw` outside `local`/`test`, without `raw_content_enabled=true`, or `raw_content_enabled=true` for another mode | startup error |
| another unknown payload mode | startup error |
| smoke disabled | infra-smoke route absent or returns 404 |
| a chat request carries smoke headers while smoke is disabled, remote, incomplete, invalid, unauthenticated or replayed | reject before handler/provider with one stable low-sensitive response |
| live chat smoke enabled without its credential reference/value or bounded replay registry | startup error; do not register a partially protected admission path |
| Langfuse score not configured | evidence persists; projection status `not_configured` |
| backend profile missing credentials | affected real smoke fails before sending |
| Collector pipeline references an unknown component or has an invalid graph | `obs-config-check` fails before Compose starts, with file path and stable `invalid_collector_pipeline` category |
| Collector persistent-storage path is absent, not a directory, or not writable by the Collector runtime user | `obs-config-check` fails before Compose starts with `storage_path_unavailable`; a Level 3 injection must also prove runtime storage errors are observable and recovered safely |

## 4. Environment/secret contract

- Secret-bearing keys are injected by environment, Docker secret, secret file or external manager.
- Config snapshots and error messages expose only the environment variable name and `credential_present` boolean.
- Local override files use an ignored naming convention such as `*.local.yaml`/`.env.local`.
- No command prints the value of `OPENAI_API_KEY`, Langfuse secret/public key pair, OTLP Authorization header or SigNoz ingestion key.
- A smoke command may create a short-lived credential only when it owns that credential lifecycle. It must revoke it at the issuing service when revocation is supported, delete all local secret files and run-scoped temporary data before writing its final report, and record cleanup status. Caller-supplied long-lived credentials are never deleted or revoked by smoke.

## 5. Infra smoke negative-query ports

`infra` smoke is a diagnostic client rather than an application dependency. Only its command
composition root may resolve the following environment-variable references; `pkg/ai`, HTTP
controllers, usecases, reports and normal application configuration must not hold their values.

```yaml
observability:
  smoke:
    infra_negative_query:
      langfuse:
        base_url_env: LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL
        credential_env: LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL
      ai_plane:
        base_url_env: LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL
        credential_env: LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL
```

This is a reference-only shape: checked-in files contain neither a URL nor a credential. A
configuration snapshot or diagnostic may expose an environment-variable name and
`credential_present` only. It must never expose an endpoint value, Authorization header, query,
marker, response body or platform error text.

The two ports have separate lifecycle and least-privilege requirements:

| Port | Required capability | Prohibited capability |
| --- | --- | --- |
| `langfuse` | Read/count the current project's trace or observation evidence for one exact smoke marker | ingest/write, score write, token/project/user administration, export or unbounded trace reads |
| `ai_plane` | Read/count low-sensitive Collector or AI-plane routing evidence for one exact smoke marker | pipeline mutation, exporter mutation, queue deletion, credential management or arbitrary metric/query execution |

The concrete Langfuse and AI-plane protocol is intentionally owned by the backend adapter and
will be fixed by T065B's contract tests. The command cannot infer it from an OTLP ingest URL,
Grafana URL, `ai_trace_id`, project identifier or a caller-provided flag.

Both ports follow these non-negotiable bounds:

- Query only the runner-generated exact marker and the current `[started_at, deadline]` window;
  the full smoke window is at most 60 seconds. No CLI flag may provide a marker, query, project,
  pagination, time range or output path.
- Each request is read-only, uses the caller deadline plus a maximum 30-second sub-timeout,
  forbids redirects, and accepts at most 1 MiB of response data. Raw documents are discarded
  before crossing the backend adapter boundary.
- The adapter returns only a non-negative count or a stable error class. A zero count is evidence
  only after a successful bounded query; missing configuration, a missing credential, or a query
  failure must never be converted to zero, `skipped`, or `passed`.
- T065B's first `grafana` profile is host-local only: the base URL host must be `127.0.0.1`, or
  `localhost` after resolution confirms that every address is loopback. Its port may use the
  explicit local override from the profile-and-port contract. Any other hostname, IP literal,
  container DNS name, private-network address or remote address is rejected. A Compose-network or
  remote profile requires a later ADR plus an explicit compiled hostname/port allowlist; it may
  not be introduced through an environment value.
- Query URL validation rejects userinfo, query strings, fragments and arbitrary path overrides;
  it forbids redirects before network I/O. The adapter must re-check a `localhost` resolution at
  connection time so DNS rebinding cannot move the request outside the loopback-only boundary.

| Condition | Required class / behavior |
| --- | --- |
| missing base-URL or credential reference, or credential not resolved | preflight failure before client construction; `authentication_failed` when represented in a report |
| 401 or 403 | `authentication_failed` |
| invalid marker/window or prohibited query construction | `invalid_query` |
| caller or sub-query deadline | `backend_timeout` |
| 429, 5xx, connection failure or unsupported protocol | `backend_unavailable` |
| oversized, non-JSON or count-unreadable response | `malformed_response` |
| other adapter failure | `query_failed` |
| successful count greater than zero | `unexpected_evidence` |

`langfuse_trace` reports only `matched_traces`; `collector` reports only `marker_received`.
Neither evidence DTO, error, report, log nor CLI output may carry platform IDs, endpoint values,
credentials, headers, raw response data or non-marker payload.

## 6. Collector component IDs

Stable component IDs are part of the dashboard/alert/smoke contract:

```text
otlp/tempo
otlphttp/loki
otlphttp/langfuse
prometheus/app
file_storage/tempo
file_storage/loki
file_storage/langfuse
```

The final config may use one `file_storage` extension with separate queue namespaces if supported by the pinned Collector; evidence must still distinguish all three exporters.

## 7. Profile and port contract

Only loopback-facing development ports are published. Internal OTLP/database ports remain on the Compose network unless required by a diagnostic command.

| Surface | Default host port |
| --- | --- |
| Application | 8000 |
| Grafana | 3000 |
| Langfuse UI | 3001 |
| Prometheus | 9090 |
| Loki query | 3100 |
| Tempo query | 3200 |
| Collector health | 13133 |
| Collector self metrics | 8888 |
| Application metrics scrape | 8889 |
| SigNoz UI | 3301 |

Port overrides must be supported through local environment values. Config validation reports conflicts before E2E.

## 8. Version contract

- `deploy/observability/versions.env` is the single readable tag matrix.
- Compose resolves immutable digests in release/CI evidence.
- `latest` is rejected by `obs-config-check`.
- Every service has a health check and declared CPU/memory limit.
- Full local profile target budget: at most 8 vCPU, 12 GiB RAM and 20 GiB observability volumes.

## 9. Retention contract

Defaults use the retention baseline in the decision workbench and are mirrored in `data-model.md`: Prometheus metrics 15 days; Loki and Tempo 7 days; Langfuse metadata/redacted traces 14 days; low-sensitive eval evidence/report 90 days; persistent queue only while backlogged. `content_raw` is a local/test-only, non-serializable debug artifact rather than an observability payload, so no backend raw-content retention unit is permitted.
