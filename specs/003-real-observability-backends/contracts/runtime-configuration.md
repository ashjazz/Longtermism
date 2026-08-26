# Runtime Configuration Contract

This contract records the final checked-in runtime boundary for feature 003. It distinguishes
application configuration, deployment/profile configuration, and smoke-only query inputs. A value
described as a declaration is not evidence that a backend enforces it at runtime.

## 1. Configuration ownership

| Owner | May contain | Must not contain |
| --- | --- | --- |
| Application | resource identity, signal switches, payload policy, smoke admission references, and the single App -> Collector endpoint | Tempo, Loki, Prometheus, Grafana, SigNoz, or Langfuse observability-backend endpoints |
| Collector profile | backend endpoints, exporter headers resolved from env, pipelines, sampling, queues, and queue storage | application LLM keys or checked-in credential values |
| Backend Compose profile | pinned image references, loopback publications, internal ports, volumes, declared limits, health strategy, and retention settings/declarations | application business configuration |
| Grafana provisioning | Compose-internal datasource URLs, dashboards, and alerts | exporter or query credentials |
| Langfuse score adapter | base-URL/key env names and presence state | raw prompt/query/output or credential values in logs and failures |
| Smoke runner | scenario-scoped endpoint, credential, manifest, and artifact env references | committed credential values, normal application backend dependencies, or unbounded backend queries |

The ownership direction is guarded by
`internal/cmd/observability_bootstrap_test.go — TestBuildObservabilityBootstrapNormalizesRuntimeOwnership`,
`TestNormalizeObservabilityBootstrapInputValidatesSharedOTLPLogsLifecycle`, and
`TestBuildObservabilityBootstrapRejectsCollectorModeWithoutCollectorConfig`.

## 2. Effective application configuration

### 2.1 Checked-in safe defaults

```yaml
observability:
  enabled: false
  mode: noop
  environment: local # composition-root default; omitted in the checked-in file
  resource:
    service_name: longtermism
    service_version: dev
  collector:
    protocol: grpc
    endpoint: ""
    timeout: 10s # composition-root default; omitted in the checked-in file
    insecure: false
    headers_env: OTEL_EXPORTER_OTLP_HEADERS
  signals:
    traces_enabled: false
    metrics_enabled: false
    logs_transport: otlp
    local_jsonl_enabled: false
  tracing:
    sampling_ratio: 1.0
  payload:
    mode: metadata_only
    raw_content_enabled: false
  smoke:
    enabled: false
    chat:
      enabled: false
      authorization_env: LONGTERMISM_CHAT_SMOKE_AUTHORIZATION
      replay_capacity: 64
      replay_ttl: 2m
      manifest_path: build/observability/smoke-manifests
```

This is a key-and-default contract, not an active Collector example or secret file. Collector mode
requires deliberate `enabled=true`, `mode=collector`, endpoint, and signal settings. That endpoint
remains the only application-owned observability-backend address. The protocol selects a gRPC
authority or OTLP/HTTP protobuf URL; timeout must be positive and no more than 60 seconds.

The composition root reads `observability.environment` with a `local` fallback. The checked-in
`app.environment` key does not drive observability runtime identity.
`observability.sensitive_data.on_match` is present in the manifest but is not consumed by the
current composition root, so it is not an effective runtime switch in this contract. The resource
model supports an optional instance ID, but the production composition does not expose a manifest
key for it.

`enabled` and `mode` cannot have two interpretations: disabled plus omitted/`noop` normalizes
to `noop`; disabled plus a network mode fails. `signals` controls provider assembly, while
`smoke.enabled` controls diagnostic route admission.

Evidence:

- `internal/cmd/observability_runtime_config_test.go — TestResolveObservabilityRuntimeConfig`
- `internal/cmd/observability_exporter_test.go — TestObservabilityOTLPExporterConfigurationOwnsOnlyCollectorEndpoint`
- `internal/cmd/observability_exporter_test.go — TestBuildObservabilityOTLPExporterConfig`
- `internal/cmd/observability_resource_test.go — TestBuildObservabilityResource`
- `hack/observability/config_check_test.sh` rejects application-owned backend endpoints.

### 2.2 Provider and score references

```yaml
ai:
  chat:
    enabled: false
  llm:
    default_provider: openai
    providers:
      openai:
        base_url_env: OPENAI_BASE_URL
        api_key_env: OPENAI_API_KEY
        default_model: ""
    timeout: 60s
    retry:
      max: 2
      backoffMs: 1000
```

Version-controlled configuration records env names, not values. The LLM safe snapshot contains the
API-key env name plus `CredentialPresent` and `BaseURLPresent`; it never contains the resolved
URL or key. Chat remains disabled until URL, model, and key requirements are complete. The
effective provider execution timeout must be positive and no more than 60 seconds, including all
configured retries/backoff under the single resilience lifecycle. A successful non-streaming
provider response must explicitly contain a standard `usage` object; missing or `null` usage is an
invalid upstream response, while an explicitly present all-zero usage object remains distinct and
is validated normally.

Score projection reads `LANGFUSE_BASE_URL`, `LANGFUSE_PUBLIC_KEY`, and
`LANGFUSE_SECRET_KEY` directly at composition. All absent means `not_configured`; a partial set
fails with a sanitized error; only a complete set constructs the client.

Evidence: `internal/cmd/llm_provider_test.go —
TestBuildLLMProviderEnabledFailsFastForMissingConfiguration`,
`TestBuildLLMProviderUsesInjectedFakeWithoutExternalCallsOrSecretLeakage`,
`internal/cmd/chat_runtime_test.go —
TestBuildDefaultChatProjectionQueueDoesNoFilesystemWorkWhenLangfuseIsUnconfigured`, and
`internal/cmd/langfuse_score_lifecycle_test.go —
TestBuildLangfuseScoreLifecycleRejectsPartialOrFailedConfigurationWithoutSecrets`.

### 2.3 Protected live-chat smoke admission

`observability.smoke.enabled` is the common infra/live-chat smoke switch. Live chat also requires
`observability.smoke.chat.enabled`, a non-empty authorization env reference and value, and a
bounded replay registry.

- Only loopback peers are accepted; proxy headers cannot make a remote peer local.
- `X-Observability-Smoke-Run-ID` and `X-Observability-Smoke-Authorization` are indivisible.
- Credentials use constant-time comparison. A credential can authorize multiple runs, but an
  authenticated marker is atomically one-shot and the registry has explicit TTL/capacity bounds.
- Disabled, remote, incomplete, malformed, unauthenticated, and replayed requests share one stable,
  low-sensitive rejection before handler/provider work.
- On success the authorization header is removed. Only the trusted marker enters local context;
  the credential never enters a DTO, command, baggage, span, log, metric, manifest, or report.

Evidence: `internal/cmd/observability_runtime_config_test.go —
TestResolveChatSmokeRuntimeConfigRequiresCompleteProtectedAdmission`.

### 2.4 Live-chat execution and evidence budgets

Live chat uses two consecutive bounded phases rather than one shared deadline:

| Phase | Start | Maximum | Ownership |
| --- | --- | --- | --- |
| provider execution | immediately before the protected trigger/provider lifecycle | 60 seconds | application `ai.llm.timeout`, capped by this contract |
| evidence convergence | actual successful API completion | 60 seconds | smoke runner; polls Tempo, Loki, Langfuse and Prometheus |
| complete chat report | smoke `started_at` | 120 seconds total | smoke runner hard cap |

The metric baseline is obtained before the trigger under its own short timeout and may overlap
provider execution; it must not extend the final report deadline. Evidence polling cannot begin
with a stale precomputed timestamp, and a fast model does not donate unused execution budget to an
evidence window longer than 60 seconds. Parent cancellation always shortens both phases.

The local Grafana live profile must expose explicit metric periodic-export, Collector
batch/tail-decision, and Prometheus scrape intervals whose documented worst-case composition fits
inside the 60-second evidence window. Those intervals are operational configuration, not business
facts; missing, non-positive or over-budget smoke overrides fail before the paid trigger. The
ordinary production profile and SigNoz profile are not silently rewritten by chat smoke values.

## 3. Fail-fast matrix

| Condition | Required behavior |
| --- | --- |
| mode `noop` | no exporter; server may start |
| mode `local` | only local/test sinks; no observability network client |
| `enabled=false` with non-`noop` mode | startup error |
| mode `collector` without valid endpoint, protocol, or timeout | startup error |
| production plus insecure Collector transport | startup error in the current composition |
| chat enabled without provider URL/key/model | startup error |
| provider timeout non-positive or greater than 60 seconds | startup error |
| successful provider envelope without explicit valid usage | stable upstream invalid-response before generation/eval/HTTP success projection |
| `content_raw` outside local/test, without explicit opt-in, or inconsistent with opt-in | startup error |
| unknown payload mode | startup error |
| smoke disabled | infra-smoke route absent/404; chat smoke headers are rejected before provider work |
| live-chat smoke incomplete, remote, invalid, unauthenticated, or replayed | one low-sensitive rejection before handler/provider |
| Langfuse score projection not configured | evidence persists; projection status `not_configured` |
| required Compose env reference missing | `${VAR:?}` expansion fails before container creation |
| backend pipeline/component graph invalid | `obs-config-check` fails with stable configuration category |
| Collector storage absent, wrong type, or unwritable | preflight fails; runtime injection must expose the storage failure |
| fixed loopback port occupied | Docker Compose fails while binding the port; no host-port occupancy preflight is implemented and ports are not silently remapped |

## 4. Environment and secret contract

### 4.1 Safe runtime snapshots

| Boundary | Version-controlled reference | Permitted snapshot fields | Prohibited fields |
| --- | --- | --- | --- |
| App -> Collector auth | `OTEL_EXPORTER_OTLP_HEADERS` | `HeaderEnvName`, `CredentialPresent` | header/credential value |
| App live-chat smoke | `LONGTERMISM_CHAT_SMOKE_AUTHORIZATION` | `AuthorizationEnvName`, `CredentialPresent`, readiness/replay bounds | authorization value or marker |
| LLM provider | `OPENAI_API_KEY`, `OPENAI_BASE_URL` | `APIKeyEnvName`, `CredentialPresent`, `BaseURLPresent` | key or resolved base URL |
| Score projection | `LANGFUSE_BASE_URL`, `LANGFUSE_PUBLIC_KEY`, `LANGFUSE_SECRET_KEY` | env names and presence booleans only | URL/key values and credential-bearing errors |
| Smoke query adapters | `LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL`, `LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL`, `LONGTERMISM_SMOKE_SIGNOZ_INGESTION_KEY` | env names and presence booleans only | credential, Authorization header, response body |
| Live smoke chat client | `LONGTERMISM_SMOKE_CHAT_AUTHORIZATION` | env name and presence boolean only | credential or request header |

Endpoint envs such as `LONGTERMISM_SMOKE_*_QUERY_BASE_URL` are non-secret references, but their
resolved values still cannot enter a safe snapshot, report, stable error, or log. Snapshot behavior
is guarded by `TestResolveObservabilityRuntimeConfig`,
`TestResolveChatSmokeRuntimeConfigRequiresCompleteProtectedAdmission`, and
`internal/cmd/observability_exporter_test.go —
TestNewObservabilityOTLPExporterRejectsUnsafeHeaderWithoutEcho`.

### 4.2 Compose injection boundary

Checked-in Compose files contain `${VAR:?}` references, never credential literals.
Secret-bearing references cover the Grafana admin password, Langfuse OTLP authorization, database
URLs/passwords, Langfuse salt/encryption/auth/license/init credentials, Redis connection string,
ClickHouse admin passwords, and MinIO access credentials. Values exist only at the operator/container
injection boundary. Local values belong in ignored `.env.local` or an external secret manager.

No command may print a resolved secret. A smoke command may revoke/delete only a short-lived
credential whose lifecycle it created; caller-owned credentials are never deleted. Cleanup status,
not secret content, is written to the final report.

## 5. Smoke-only query boundary

The smoke command—not the application—resolves:

| Adapter | Endpoint reference | Credential reference | Bound |
| --- | --- | --- | --- |
| Langfuse | `LONGTERMISM_SMOKE_LANGFUSE_QUERY_BASE_URL` | `LONGTERMISM_SMOKE_LANGFUSE_QUERY_CREDENTIAL` | native trace/observation ID + bounded window; nested correlation validation; read only |
| AI plane | `LONGTERMISM_SMOKE_AI_PLANE_QUERY_BASE_URL` | `LONGTERMISM_SMOKE_AI_PLANE_QUERY_CREDENTIAL` | exact marker; no pipeline/queue mutation |
| SigNoz | `LONGTERMISM_SMOKE_SIGNOZ_QUERY_BASE_URL` | `LONGTERMISM_SMOKE_SIGNOZ_INGESTION_KEY` | selected profile; no credential/query echo |
| Prometheus | `LONGTERMISM_SMOKE_PROMETHEUS_QUERY_BASE_URL` | none | compiled query only |
| Loki | `LONGTERMISM_SMOKE_LOKI_QUERY_BASE_URL` | none | compiled query only |
| Tempo | `LONGTERMISM_SMOKE_TEMPO_QUERY_BASE_URL` | none | compiled query only |

These adapters are implemented, not future placeholders. The locked self-hosted Langfuse 3.185
profile uses legacy v1 `GET /api/public/observations`; official compatibility guidance reserves
Observations API v2 for self-hosted Langfuse v4+. The v3 server-side filter uses only native
`traceId`, observation `id`, and the bounded start-time window. The client must then validate
`metadata.attributes.longtermism.smoke.run_id`, `metadata.attributes.request.id`, and
`metadata.attributes.longtermism.ai.trace_id` from the returned row. It must not query obsolete
top-level metadata keys, copy target values into results, or attempt a v1/v2 fallback. The AI plane uses bounded
`GET /api/v1/observability/smoke/marker-count`. Both use a Basic credential at the client boundary,
forbid redirects, revalidate loopback resolution, cap a response at 1 MiB, and use at most a
30-second request sub-timeout. The reusable query adapter permits a window up to 150 seconds so the
120-second persistent-queue drain scenario is representable; ordinary infra remains capped at 60
seconds, while chat may query `[started_at, evidence_deadline]` up to the 120-second total report
cap. Missing credentials/query failures never become a zero count
or pass, and reports never carry endpoints, headers, raw responses, platform IDs, or payloads.

Compatibility source: [Langfuse Observations API](https://langfuse.com/docs/api-and-data-platform/features/observations-api)
and [Versions & Compatibility](https://langfuse.com/docs/compatibility).

Evidence: `internal/observability/backend/langfuse_smoke_query_test.go` contract tests and
`cmd/obs-smoke/main_test.go` scenario-reference tests.

## 6. Stable Collector component matrix

Component IDs are telemetry, dashboard, alert, queue, and smoke selectors.

| Kind | Shared | Grafana + Langfuse | SigNoz + Langfuse |
| --- | --- | --- | --- |
| Receiver | `otlp` | shared | shared |
| Infra/AI connectors | `forward/infra`, `forward/ai` | shared | shared |
| Queue storage | — | `file_storage/tempo`, `file_storage/loki`, `file_storage/langfuse` | `file_storage/signoz`, `file_storage/langfuse` |
| Infra exporters | — | `otlp/tempo`, `otlphttp/loki`, `prometheus/app` | `otlp/signoz` |
| AI exporter | — | `otlphttp/langfuse` | `otlphttp/langfuse` |
| Health extension | `health_check` | shared | shared |

Grafana infra signals route to Tempo/Loki/Prometheus; SigNoz infra signals route through
`otlp/signoz`. Only explicit AI markers route to Langfuse. Exact IDs and graphs are guarded by
`hack/observability/collector_grafana_config_test.sh` and
`hack/observability/collector_signoz_config_test.sh`.

## 7. Profile and port matrix

All published development ports bind `127.0.0.1`. Profiles share Collector and Langfuse ports and
cannot run concurrently under the checked-in mappings.

| Surface | Grafana + Langfuse host -> container | SigNoz + Langfuse host -> container | Scope |
| --- | --- | --- | --- |
| Collector OTLP gRPC | `127.0.0.1:4317 -> 4317` | `127.0.0.1:4317 -> 4317` | published loopback |
| Collector OTLP HTTP | `127.0.0.1:4318 -> 4318` | `127.0.0.1:4318 -> 4318` | published loopback |
| Collector health | `127.0.0.1:13133 -> 13133` | `127.0.0.1:13133 -> 13133` | published loopback |
| Collector self metrics | `127.0.0.1:8888 -> 8888` | `127.0.0.1:8888 -> 8888` | published loopback |
| Grafana | `127.0.0.1:3000 -> 3000` | — | published loopback |
| Prometheus | `127.0.0.1:9090 -> 9090` | — | published loopback |
| Loki query | `127.0.0.1:3100 -> 3100` | — | published loopback |
| Tempo query | `127.0.0.1:3200 -> 3200` | — | published loopback |
| SigNoz UI/query declaration | — | `127.0.0.1:3301 -> 3301` | checked-in publication only; live query viability is not proven |
| Langfuse UI/query | `127.0.0.1:3001 -> 3000` | `127.0.0.1:3001 -> 3000` | published loopback |
| `prometheus/app` | `collector:8889` | — | **Compose-internal only**, never host-published |
| Application | default `:8000`; Grafana smoke example uses `127.0.0.1:8000` | started separately | application config, not Compose |

Checked-in Compose uses fixed ports and has no environment-based override. Conflicts fail fast. A
different mapping requires an explicit local Compose overlay and matching test changes.

The SigNoz service healthcheck currently targets container port `8080`, while Compose publishes
container port `3301`. No checked-in setting explains that difference and no real E2E evidence proves
`3301` is a usable UI/query listener. Therefore the row records the current declaration, not an
operational endpoint guarantee; this inconsistency must be resolved in a deployment task before a
SigNoz live smoke may claim readiness.

Evidence: `hack/observability/compose_grafana_test.sh`,
`hack/observability/compose_signoz_test.sh`, `hack/observability/config_check_test.sh`, and
`internal/cmd/grafana_smoke_config_example_test.go —
TestGrafanaSmokeConfigExampleIsStandaloneAndLoopbackBound`.

## 8. Resource and version matrices

### 8.1 Declared service limits

| Grafana overlay | CPU | Memory |
| --- | ---: | ---: |
| collector-storage-init | 0.10 | 64 MiB |
| collector | 0.50 | 512 MiB |
| collector-health-probe | 0.10 | 64 MiB |
| loki-health-probe | 0.10 | 64 MiB |
| tempo-health-probe | 0.10 | 64 MiB |
| prometheus | 0.50 | 1 GiB |
| loki | 0.75 | 1 GiB |
| tempo | 0.75 | 1 GiB |
| grafana | 0.50 | 512 MiB |

| Shared Langfuse | CPU | Memory |
| --- | ---: | ---: |
| langfuse-db | 0.75 | 1 GiB |
| langfuse-clickhouse | 1.25 | 2 GiB |
| langfuse-clickhouse-init | 0.10 | 128 MiB |
| langfuse-redis | 0.25 | 512 MiB |
| langfuse-minio | 0.25 | 512 MiB |
| langfuse-minio-init | 0.10 | 128 MiB |
| langfuse-web | 1.00 | 2 GiB |
| langfuse-worker | 0.75 | 1 GiB |

| SigNoz overlay | CPU | Memory |
| --- | ---: | ---: |
| collector | 0.50 | 512 MiB |
| collector-health-probe | 0.10 | 128 MiB |
| collector-storage-init | 0.10 | 128 MiB |
| signoz | 0.75 | 1536 MiB |
| signoz-otel-collector | 0.50 | 512 MiB |
| clickhouse (SigNoz override) | 1.50 | 2 GiB |

| Full profile | Services | CPU-limit sum | Memory-limit sum | Declared operator budget |
| --- | ---: | ---: | ---: | --- |
| Grafana + Langfuse | 17 | **7.85 vCPU** | **11776 MiB / 11.5 GiB** | 8 vCPU / 12 GiB RAM / 20 GiB volumes |
| SigNoz + Langfuse | 14 | **7.90 vCPU** | **12288 MiB / 12 GiB** | 8 vCPU / 12 GiB RAM / 20 GiB volumes |

The 20 GiB volume figure is an operator budget declaration, not a Docker named-volume quota. Health
readiness is service-specific: distroless services may use sidecars, initializers are one-shot, and
workers without a health endpoint use dependency/E2E evidence. Not every container has an inline
Docker healthcheck.

### 8.2 Version source

`deploy/observability/versions.env` is the single readable image-tag matrix. It pins Collector
Contrib 0.153.0, Grafana 13.1.0, Prometheus 3.11.0, Loki 3.7.2, Tempo 2.10.5, Langfuse web/worker
3.185.0, SigNoz/its Collector v0.126.0, and pinned storage/helper images. `latest` is rejected.
The checked-in matrix contains tags, not immutable digests; digest resolution remains a release/CI
gate rather than an already checked-in fact. The two `compose_*_test.sh` scripts guard version,
resource, port, volume, and health-strategy invariants.

## 9. Retention and enforcement matrix

| Unit | Baseline | Current enforcement/evidence | Contract strength |
| --- | --- | --- | --- |
| Prometheus metrics | 15 days | Compose command sets `--storage.tsdb.retention.time=15d`; static profile test | configured |
| Loki logs | 168h / 7 days | `loki.yaml` enables compactor retention and sets `168h`; config/profile checks | configured |
| Tempo traces | 168h / 7 days | `tempo.yaml` sets block retention `168h`; static profile test | configured |
| Langfuse metadata/redacted traces | 14 days | headless init sets 14 days for a new project; requires EE; existing projects are not backfilled | conditional initialization; requires live verification |
| SigNoz metrics/logs/traces | 15d / 7d / 7d | `compose.signoz.yaml` has `x-observability-retention` metadata only | **profile declaration only; not runtime-enforced** |
| Low-sensitive local eval evidence | at most 2160h / 90 days | open/config boundary rejects a larger retention | bounded configuration; no automatic compaction |
| Persistent Collector queue | while backlogged | records must drain after delivery; no age TTL or native queue-age metric | lifecycle invariant, not time retention |
| `content_raw` | none | local/test-only, non-serializable debug artifact; cleanup required | never a backend retention unit |

SigNoz storage TTL and real-profile retention require live backend query evidence before being
described as enforced. Static metadata is insufficient. Runner tests with fake backends prove
report/cleanup/failure semantics, not real-backend retention.

Evidence:

- `internal/eval/evidence_store_test.go — TestOpenLocalEvidenceStoreEnforcesNinetyDayRetentionBoundary`
- `internal/observability/smoke/retention_runner_test.go — TestRunRetentionSmokeVerifiesAllUnitsAndCleansRawArtifacts`
- `internal/observability/smoke/retention_runner_test.go — TestRunRetentionSmokeFailsOnRetentionWindowMismatch`
- `internal/observability/smoke/retention_runner_test.go — TestRunRetentionSmokeFailsWhenRawPayloadRetained`
- `internal/observability/smoke/retention_runner_test.go — TestRunRetentionSmokeFailsWhenQueueRetainsDeliveredRecords`
- `hack/observability/langfuse_compose_test.sh`

## 10. Static quality gates

Before a profile starts:

1. `make obs-config-check` (which runs `hack/observability/config_check.sh`) validates the current
   repository's single application Collector endpoint, pinned image references, loopback ports,
   graphs, and storage constraints. `hack/observability/config_check_test.sh` separately tests the
   checker's failure categories with fixtures.
2. `hack/observability/collector_grafana_config_test.sh` or
   `collector_signoz_config_test.sh` validates stable components and signal routing.
3. `hack/observability/compose_grafana_test.sh` or `compose_signoz_test.sh` validates the final
   service/resource/port/volume/health-strategy matrix.
4. `hack/observability/langfuse_compose_test.sh` validates shared Langfuse injection and
   initialization constraints without accepting literal credentials.

These gates protect configuration truth; they do not replace live E2E evidence for backend
readiness, delivery, query behavior, or retention.
