# Runtime Configuration Contract

## 1. Ownership

| Configuration owner | May contain | Must not contain |
| --- | --- | --- |
| Application | Collector endpoint/protocol, resource, signal flags, payload policy, smoke flag, LLM provider config names | Tempo/Loki/Prometheus/Grafana/SigNoz endpoints; Langfuse OTLP endpoint |
| Collector | backend endpoints, exporter headers from env/secret, pipelines, queues, sampling | application API key values in checked-in config |
| Backend profile | service images, ports, volumes, retention, health checks | application business config |
| Grafana provisioning | datasource internal URLs, dashboards, alerts | telemetry exporter credentials |
| Langfuse score adapter | base URL and public/secret key references | prompt/query/output raw values in failure logs |
| Smoke runner | local query URLs and credential references | committed secrets or default paid-service endpoints |

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
    logs_transport: glog_file
  tracing:
    sampling_ratio: 1.0
  payload:
    mode: content_redacted
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

## 3. Fail-fast matrix

| Condition | Required behavior |
| --- | --- |
| mode is `noop` | no exporter, server may start |
| mode is `local` | only local/test sinks, no network |
| mode is `collector` without endpoint | startup error |
| unsupported protocol | startup error |
| chat enabled without model base URL/key/model | startup error |
| production + `content_raw` | startup error |
| smoke disabled | infra-smoke route absent or returns 404 |
| Langfuse score not configured | evidence persists; projection status `not_configured` |
| backend profile missing credentials | affected real smoke fails before sending |

## 4. Environment/secret contract

- Secret-bearing keys are injected by environment, Docker secret, secret file or external manager.
- Config snapshots and error messages expose only the environment variable name and `credential_present` boolean.
- Local override files use an ignored naming convention such as `*.local.yaml`/`.env.local`.
- No command prints the value of `OPENAI_API_KEY`, Langfuse secret/public key pair, OTLP Authorization header or SigNoz ingestion key.

## 5. Collector component IDs

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

## 6. Profile and port contract

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

## 7. Version contract

- `deploy/observability/versions.env` is the single readable tag matrix.
- Compose resolves immutable digests in release/CI evidence.
- `latest` is rejected by `obs-config-check`.
- Every service has a health check and declared CPU/memory limit.
- Full local profile target budget: at most 8 vCPU, 12 GiB RAM and 20 GiB observability volumes.

## 8. Retention contract

Defaults follow `data-model.md`. If a backend cannot enforce record-level `content_raw` retention, raw debug runs use a separate project/instance/volume so the whole unit can be deleted within 24 hours.
