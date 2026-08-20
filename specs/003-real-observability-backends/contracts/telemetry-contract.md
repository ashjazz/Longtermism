# Telemetry Contract

This contract is calibrated to the shipped 003 implementation. Canonical OTel names, Collector
component IDs and evidence sources are facts: adapters and operators must not infer replacements
from routes, span names, backend URLs or incomplete identities. Each table cites the executable
test that guards the corresponding implementation.

## 1. Planes and routing

The infrastructure pipeline receives every trace. `longtermism.observability.plane=ai` is the
sole Collector routing predicate for the AI pipeline. Application-owned AI semantic spans also
carry `longtermism.ai.designated=true`; the pair is required by the designated-AI tail-retention
policy, not by `filter/ai` routing:

```text
longtermism.observability.plane = "ai"
longtermism.ai.designated = "true"
```

| Runtime span/traffic | Infra pipeline | AI pipeline / Langfuse | Executable evidence |
| --- | --- | --- | --- |
| infrastructure-only HTTP service span (executable example: infra-smoke) | yes, subject to retention policy | no | [`internal/observability/http_logging_test.go`](../../../internal/observability/http_logging_test.go) — `TestHTTPCompletionLoggingMiddlewareWritesInfraSmokeCompletion` |
| infra-smoke HTTP span | yes, retained by smoke marker | no | [`internal/logic/observability/infra_smoke_test.go`](../../../internal/logic/observability/infra_smoke_test.go) — `TestInfraSmokeUsecaseRecordsOnlyInfrastructureSignals` |
| ordinary chat HTTP service span | yes | no; it is a service fact, not an AI semantic span | [`internal/observability/http_logging_test.go`](../../../internal/observability/http_logging_test.go) — `TestHTTPCompletionLoggingMiddlewareWritesAuthenticatedChatSmokeMarker` |
| `ai.chat` bridge | yes | yes | [`internal/observability/chat_boundary_test.go`](../../../internal/observability/chat_boundary_test.go) — `TestChatAIExecutionBoundaryCreatesOwnedBridgeBelowActiveRoot` |
| `ai.generation` / `ai.evaluator` | yes | yes | [`internal/observability/generation_test.go`](../../../internal/observability/generation_test.go) — `TestGenerationSpanAdapterRecordsNativeParentageAndExplicitFacts`; [`internal/observability/evaluator_test.go`](../../../internal/observability/evaluator_test.go) — `TestEvaluatorSpanAdapterRecordsEvidenceWithNativeParentage` |
| DB/Redis/ordinary HTTP child | yes | no | [`pkg/ai/obs/otel_mapper_test.go`](../../../pkg/ai/obs/otel_mapper_test.go) — `TestMapSpanRoutingAttributesLeavesInfrastructureChildrenUnmarkedWithAILikeInput` |

`MapSpanRoutingAttributes` supports explicit AI roles for chat root/bridge, generation, retriever,
tool, agent and evaluator. The current HTTP runtime keeps its service span infrastructure-only and
creates an owned `ai.chat` bridge; future code may mark an AI root only by explicitly selecting the
AI-root role. Route, feature, span name, baggage or an `ai_trace_id` must never manufacture the
marker. This role boundary is guarded by
[`pkg/ai/obs/otel_mapper_test.go`](../../../pkg/ai/obs/otel_mapper_test.go) —
`TestMapSpanRoutingAttributesMarksOnlyExplicitAIPlaneRoles`.

## 2. Span names and attributes

| Span | Final name | Required low-sensitive attributes | Executable evidence |
| --- | --- | --- | --- |
| HTTP service completion | `HTTP <CANONICAL_METHOD> <trusted-route-template>` | `http.request.method`, `http.route`, `http.response.status_code`, `request.id`; `longtermism.smoke.run_id` may be present with an empty value, but only protected smoke may supply a non-empty value | [`internal/observability/http_logging_test.go`](../../../internal/observability/http_logging_test.go) — `TestHTTPCompletionLoggingMiddlewareWritesInfraSmokeCompletion`, `TestHTTPCompletionLoggingMiddlewareRejectsRawRouteFallback` |
| AI bridge | `ai.chat` | the five explicit AI/correlation facts (`plane`, `designated`, `longtermism.ai.trace_id`, `ai.feature`, `request.id`), `ai.outcome`, optional stable `ai.failure_status`, optional protected smoke marker | [`internal/observability/chat_boundary_test.go`](../../../internal/observability/chat_boundary_test.go) — `TestChatAIExecutionBoundaryCreatesOwnedBridgeBelowActiveRoot`, `TestChatAIExecutionBoundaryRecordsTrustedSmokeMarker` |
| generation | `ai.generation` | routing facts; `gen_ai.provider.name`, requested/actual model, finish reasons; input/output/reasoning/cache token counts; total latency; prompt version/hash; payload mode/redacted flag; outcome/failure; optional smoke marker | [`internal/observability/generation_test.go`](../../../internal/observability/generation_test.go) — `TestGenerationSpanAdapterRecordsNativeParentageAndExplicitFacts`, `TestGenerationSpanAdapterRecordsTrustedSmokeMarker` |
| evaluator | `ai.evaluator` | routing facts; eval run, dataset name/version, sample, metric, score, optional threshold, regression status; optional smoke marker | [`internal/observability/evaluator_test.go`](../../../internal/observability/evaluator_test.go) — `TestEvaluatorSpanAdapterRecordsEvidenceWithNativeParentage`, `TestEvaluatorSpanAdapterRecordsTrustedSmokeMarker` |

Resource attributes shared by all signals are required `service.name` and
`deployment.environment`, plus optional `service.version` and `service.instance.id`; they are OTel
Resource attributes, not HTTP-root span attributes. This boundary is guarded by
[`internal/cmd/observability_resource_test.go`](../../../internal/cmd/observability_resource_test.go)
— `TestBuildObservabilityResource`. Native trace/span identity always comes from the active OTel
`SpanContext`. Non-streaming chat omits TTFT; if a future streaming path records it,
`ai.latency.ttft_ms` is the explicit millisecond span attribute rather than a seconds-based metric.
Generation validation is guarded by
[`internal/observability/generation_test.go`](../../../internal/observability/generation_test.go) —
`TestGenerationSpanAdapterRecordsNativeParentageAndExplicitFacts`.

For an authenticated live-chat smoke, the same runner-owned marker is copied from trusted local
context to the HTTP service span, `ai.chat`, `ai.generation`, `ai.evaluator`, and the controlled
completion log. Ordinary chat has no marker. The authorization value never enters context,
telemetry or the run manifest; this is guarded by
[`internal/observability/http_logging_test.go`](../../../internal/observability/http_logging_test.go) —
`TestHTTPCompletionLoggingMiddlewareWritesAuthenticatedChatSmokeMarker` and the three semantic
span tests cited above.

## 3. Identity mapping

| Fact | Attribute/report field | Rule | Executable evidence |
| --- | --- | --- | --- |
| API request | `request.id` / `request_id` | transport-owned and always present in the public envelope | [`internal/cmd/request_context_test.go`](../../../internal/cmd/request_context_test.go) |
| service trace | native TraceID | only from active `SpanContext`; platform trace target | [`internal/observability/chat_boundary_test.go`](../../../internal/observability/chat_boundary_test.go) |
| service/semantic span | native SpanID | only from active `SpanContext`; observation target | [`internal/observability/generation_test.go`](../../../internal/observability/generation_test.go) |
| AI domain trace | `longtermism.ai.trace_id` | opaque usecase identity; never parsed as an OTel/Langfuse ID | [`internal/observability/langfuse/trace_mapper_test.go`](../../../internal/observability/langfuse/trace_mapper_test.go) — `TestMapTraceToProjectionKeepsNativeOTelAndAIDomainIdentitiesSeparate` |
| eval run | `longtermism.eval.run_id` | local evidence/report identity; never a metric label | [`internal/observability/evaluator_test.go`](../../../internal/observability/evaluator_test.go) |
| smoke run | `longtermism.smoke.run_id` / `smoke_run_id` | protected span/log/report identity; never a metric label | [`internal/observability/metrics_test.go`](../../../internal/observability/metrics_test.go) — `TestMetricsRecordRequiredInstrumentsWithOnlyLowCardinalityAttributes` |

The live runner receives native service TraceID/SpanID only through the contained mode-0600,
single-consumer run manifest. The manifest binds request, AI and smoke identities but contains no
credential, payload, endpoint or provider body. Public chat metadata remains request ID, AI trace
ID and optional safe eval summary; adapters must never derive native identity from the AI domain
ID. The manifest boundary is guarded by
[`internal/observability/smoke/run_manifest_test.go`](../../../internal/observability/smoke/run_manifest_test.go).

## 4. Final application instruments

The canonical names below are created once by `internal/observability.NewMetrics`. A name, kind,
unit or attribute-set change is a contract change.

| Canonical OTel instrument | Kind / unit | Exact allowed attributes | Executable evidence |
| --- | --- | --- | --- |
| `longtermism.http.server.request.count` | `Int64Counter` / empty unit | `http.route`, `http.request.method`, `http.response.status_class` | [`internal/observability/metrics_test.go`](../../../internal/observability/metrics_test.go) — `TestMetricsRecordRequiredInstrumentsWithOnlyLowCardinalityAttributes` |
| `longtermism.http.server.request.duration` | `Float64Histogram` / `s` | same HTTP attribute set | same test |
| `longtermism.llm.request.count` | `Int64Counter` / empty unit | `gen_ai.provider.name`, `gen_ai.request.model`, `outcome` | same test |
| `longtermism.llm.duration` | `Float64Histogram` / `s` | same LLM-request attribute set | same test |
| `longtermism.llm.tokens` | `Int64Counter` / `{token}` | `gen_ai.provider.name`, `gen_ai.response.model`, `gen_ai.token.type` | same test |
| `longtermism.llm.cost` | `Float64Counter` / empty unit | `gen_ai.provider.name`, `gen_ai.response.model`, `currency`, `estimate.status` | same test |
| `longtermism.eval.result` | `Int64Counter` / empty unit | `evaluator`, `status`, `metric.name` | same test |
| `longtermism.eval.score` | `Float64Histogram` / empty unit (finite, non-negative values) | same eval attribute set | same test; `TestMetricsRejectsInvalidMeasurements` |
| `longtermism.score.projection` | `Int64Counter` / empty unit | `backend`, `status` | same test; [`internal/cmd/langfuse_score_lifecycle_test.go`](../../../internal/cmd/langfuse_score_lifecycle_test.go) |
| `longtermism.score.worker.queue` | synchronous `Int64Gauge` / empty unit | `backend` | same tests |

Bounded vocabularies are part of the contract: unknown route/model/metric values and unsupported
provider/outcome/currency/estimate/evaluator/status/backend values become `other`; they do not open
a new series. Score projection metrics use the coarse states `queued`, `sent`, `failed`, `dropped`
and `not_configured`; detailed immutable projection states remain local evidence. This behavior is
guarded by [`internal/observability/metrics_test.go`](../../../internal/observability/metrics_test.go)
— `TestMetricsCoarsensUnknownLabels` and `TestMetricsPreservesExplicitNotConfiguredScoreStatus`.

The canonical OTel names above are authoritative. Prometheus-exporter normalization is an
operator-asset projection, not a second canonical naming contract; dashboard queries are guarded
by
[`hack/observability/grafana_dashboard_test.go`](../../../hack/observability/grafana_dashboard_test.go),
[`hack/observability/grafana_ai_dashboard_test.go`](../../../hack/observability/grafana_ai_dashboard_test.go)
and [`hack/observability/signoz_dashboard_test.go`](../../../hack/observability/signoz_dashboard_test.go).

### High-cardinality prohibition

Metric attributes must never contain `request_id`, `trace_id`, `span_id`, `ai_trace_id`,
`session_id`, `user_id`, `prompt_hash`, `eval_run_id`, `run_id`, `smoke_run_id`, `raw_route`,
`service_trace_id`, a smoke marker, a raw URL/query, or payload content. Dotted spellings of the
same identities are equally forbidden. These identities remain available in spans, structured
logs and bounded smoke reports. The prohibition is executable in
[`internal/observability/metrics_test.go`](../../../internal/observability/metrics_test.go) and in
the dashboard query guards
[`hack/observability/grafana_dashboard_test.go`](../../../hack/observability/grafana_dashboard_test.go),
[`hack/observability/grafana_ai_dashboard_test.go`](../../../hack/observability/grafana_ai_dashboard_test.go)
and [`hack/observability/signoz_dashboard_test.go`](../../../hack/observability/signoz_dashboard_test.go).
Prometheus smoke therefore compares low-cardinality route/status or provider counter deltas; it
never queries an identity label.

## 5. Structured completion logs

Production completion logs are OTLP LogRecords sent through the same application provider
lifecycle to the Collector. JSONL is an explicit local diagnostic opt-in and is not a Loki or
smoke prerequisite.

| Record field | Contract |
| --- | --- |
| body | exactly `http request completed` or `http request failed` |
| envelope | UTC timestamp, `INFO`/`ERROR` severity |
| required attributes | `request_id`, native `trace_id`, native `span_id`, trusted `route`, canonical `method`, numeric `status`, non-negative `duration_ms` |
| conditional attributes | stable `error_class` only on failure; `ai_trace_id` only for AI; `smoke_run_id` only for protected smoke |
| resource allowlist | `service.name`, `service.version`, `deployment.environment` |

The exact application projection is guarded by
[`internal/observability/logging_test.go`](../../../internal/observability/logging_test.go) —
`TestBuildHTTPCompletionOTLPRecordUsesExactAllowlist`; response isolation and native span identity
are guarded by
[`internal/observability/http_logging_test.go`](../../../internal/observability/http_logging_test.go).
Collector then applies the same exact attribute/resource allowlist before the persistent queue,
as verified by both Collector config tests cited in section 6. Authorization, API keys, raw
prompt/query/output/tool arguments, provider body and recognized PII are never allowed in either
form. `content_raw` remains a local/test-only, non-serializable debug artifact and never enters a
telemetry sink.

## 6. Stable Collector component IDs and pipelines

IDs are operational identities used in config checks, dashboards, alerts and smoke queries. They
must not be renamed without updating all four surfaces and their tests.

| Scope | Type | Stable IDs | Executable evidence |
| --- | --- | --- | --- |
| shared | receiver | `otlp` | both config tests below |
| shared | connectors | `forward/infra`, `forward/ai` | both config tests below |
| shared | processors | `transform/redact-ingress`, `transform/redact-downstream`, `transform/redact-span-events`, `transform/redact-logs`, `filter/ai`, `filter/http-completion-logs`, `tail_sampling/retain` | both config tests below |
| shared | pipeline IDs | `traces/ingress`, `traces/infra`, `traces/ai`, `metrics`, `logs` | both config tests below |
| Grafana | extensions | `health_check`, `file_storage/tempo`, `file_storage/loki`, `file_storage/langfuse` | [`hack/observability/collector_grafana_config_test.sh`](../../../hack/observability/collector_grafana_config_test.sh) |
| Grafana | exporters | `otlp/tempo`, `otlphttp/loki`, `otlphttp/langfuse`, `prometheus/app` | same test |
| SigNoz | extensions | `health_check`, `file_storage/signoz`, `file_storage/langfuse` | [`hack/observability/collector_signoz_config_test.sh`](../../../hack/observability/collector_signoz_config_test.sh) |
| SigNoz | exporters | `otlp/signoz`, `otlphttp/langfuse` | same test |

The final topology is:

| Pipeline | Receiver(s) | Processor contract | Grafana exporter(s) | SigNoz exporter(s) |
| --- | --- | --- | --- | --- |
| `traces/ingress` | `otlp` | ingress redaction before any persistent queue | `forward/infra`, `forward/ai` | same |
| `traces/infra` | `forward/infra` | downstream/event redaction + `tail_sampling/retain` | `otlp/tempo` | `otlp/signoz` |
| `traces/ai` | `forward/ai` | `filter/ai` + downstream/event redaction + `tail_sampling/retain` | `otlphttp/langfuse` | `otlphttp/langfuse` |
| `metrics` | `otlp` | no backend-specific application logic | `prometheus/app` | `otlp/signoz` |
| `logs` | `otlp` | fixed-body filter + fail-closed log allowlist | `otlphttp/loki` | `otlp/signoz` |

Application code knows only the Collector endpoint. A profile changes downstream components, not
application instruments, span semantics or the AI filter.

## 7. Langfuse projection boundary

- Collector exports only spans that pass `filter/ai`; infra-only spans never enter Langfuse.
- Trace ingestion uses `otlphttp/langfuse`, HTTP/protobuf, the `/api/public/otel` base endpoint,
  and environment-injected Authorization plus ingestion-version headers.
- Generation spans project only allowlisted model, usage, latency, status, prompt identity and
  permitted payload fields. Platform attributes are created only by the Langfuse adapter.
- The score API is a separate failure domain. It uses the native platform TraceID, optional
  observation SpanID and a stable projection ID; local eval evidence is persisted first.

Collector transport and filtering are guarded by
[`hack/observability/langfuse_collector_test.sh`](../../../hack/observability/langfuse_collector_test.sh)
and the two profile config tests. Platform allowlisting and identity separation are guarded by
[`internal/observability/langfuse/trace_mapper_test.go`](../../../internal/observability/langfuse/trace_mapper_test.go),
while score identity/idempotency are guarded by
[`internal/observability/langfuse/projection_test.go`](../../../internal/observability/langfuse/projection_test.go).

## 8. Sampling

- Local/smoke application head sampling is 100%.
- Production head sampling remains configurable and untrusted remote `sampled` flags cannot
  bypass the local budget.
- `tail_sampling/retain` retains smoke, OTel error status, HTTP 4xx/5xx, degradation, eval
  regression, and explicitly designated AI spans; ordinary success uses a configurable
  probabilistic policy.
- The designated-AI policy requires both `longtermism.observability.plane=ai` and
  `longtermism.ai.designated=true`; neither attribute may be inferred.
- A smoke run fails if a required trace was sampled away.

Head-sampling trust is guarded by
[`internal/cmd/observability_bootstrap_test.go`](../../../internal/cmd/observability_bootstrap_test.go).
Tail policies and their exact marker keys are guarded by
[`hack/observability/collector_grafana_config_test.sh`](../../../hack/observability/collector_grafana_config_test.sh)
and [`hack/observability/collector_signoz_config_test.sh`](../../../hack/observability/collector_signoz_config_test.sh).

## 9. Failure domains and real evidence sources

The mainline failure catalog is a closed mapping. An allowed source in one row cannot be borrowed
to diagnose another row; missing evidence fails closed.

| Failure domain | Required real evidence | Stable component/metric facts | Executable evidence |
| --- | --- | --- | --- |
| `tempo_exporter` | `collector_component_telemetry` + `collector_queue_snapshot` | `otlp/tempo`, queue `tempo`, `otelcol_exporter_send_failed_spans_total` | [`internal/observability/failure/catalog_test.go`](../../../internal/observability/failure/catalog_test.go) — `TestExporterDomainsCarryRealCollectorFacts` |
| `loki_exporter` | same source types | `otlphttp/loki`, queue `loki`, `otelcol_exporter_send_failed_log_records_total` | same test |
| `langfuse_exporter` | same source types | `otlphttp/langfuse`, queue `langfuse`, `otelcol_exporter_send_failed_spans_total` | same test |
| `prometheus_scrape` | `prometheus_target_telemetry` | target `up`, scrape error/duration and bounded metric deltas; never exporter failure | same file — `TestPrometheusScrapeUsesOnlyTargetTelemetry` |
| `grafana_query` | `grafana_datasource` | datasource health and bounded query result | same file — `TestGrafanaQueryUsesOnlyDatasourceEvidence` |
| `score_worker` | `score_worker_telemetry` | coarse queued/sent/failed/dropped/not-configured metrics plus immutable local evidence | same file — `TestScoreWorkerUsesOnlyWorkerTelemetry` |
| `queue_full` | `collector_queue_snapshot` | `otelcol_exporter_queue_size`, `otelcol_exporter_queue_capacity`; no native dropped/age fact | same file — `TestQueueFullUsesOnlyQueueSnapshot` |
| `storage_unwritable` | `collector_storage_error` | runtime file-storage error; preflight path failures remain a separate stage | same file — `TestStorageUnwritableUsesOnlyStorageError` |
| `collector_restart` | `collector_lifecycle` + `collector_queue_snapshot` | container lifecycle plus same-component backlog/drain | same file — `TestCollectorLifecycleDomainsUseLifecycleAndQueueEvidence` |
| `collector_shutdown` | same source types | lifecycle plus queue snapshot; shutdown timeout is not a provider failure | same test |
| `model_upstream` | `provider_response` | provider 429/5xx/timeout and business `ai_trace_id`; never telemetry-export failure | same file — `TestModelUpstreamIsolatedFromAllObservabilitySources` |

The no-mixing invariant is guarded by
[`internal/observability/failure/catalog_test.go`](../../../internal/observability/failure/catalog_test.go)
— `TestNoEvidenceSourceMixingInvariant`. Concrete Grafana snapshot queries must cover exactly
`otlp/tempo`, `otlphttp/loki` and `otlphttp/langfuse`, with signal-correct sent/send-failed/
enqueue-failed totals and queue size/capacity; see
[`internal/observability/backend/grafana_collector_snapshot_test.go`](../../../internal/observability/backend/grafana_collector_snapshot_test.go)
— `TestCollectorSnapshotBackendCoversThreeRealComponents`.

In the SigNoz profile, component-scoped Collector telemetry uses `otlp/signoz` for infrastructure
traces/metrics/logs and `otlphttp/langfuse` for AI spans. SigNoz signal presence is proven by
bounded logs/metrics/traces backend queries, not container health; see
[`internal/observability/backend/signoz_query_test.go`](../../../internal/observability/backend/signoz_query_test.go)
and [`internal/observability/smoke/signoz_runner_test.go`](../../../internal/observability/smoke/signoz_runner_test.go).

Queue “age” is a five-minute `avg_over_time(queue_size)` backlog proxy because the pinned
Collector exposes no native queue-age metric. Queue rejection appears through the exporter
`enqueue_failed` counter; the current snapshot has no independent dropped value. Exporter
failures, log-emitter failures, score projection failures and backend query failures never change
an HTTP business envelope. Model failures retain their original business status and AI identity.

## 10. Privacy assertions

Synthetic forbidden markers are injected only through the authenticated, loopback-only fixture trigger. A privacy smoke passes only after the schema-fixed, ordered set of all eight closed surfaces records `attempted=true`, the surface-specific evidence method, scanner policy version and zero counts for every policy category (`synthetic_canary`, `credential`, `authorization`, `token`, `recognized_pii`). Missing, duplicate, skipped or default-zero entries are invalid rather than successful evidence.

The closed eight-surface composition is guarded by
[`internal/observability/smoke/privacy_composition_test.go`](../../../internal/observability/smoke/privacy_composition_test.go)
— `TestPrivacyCompositionCreatesEachSurfaceBudgetJustInTime` and
[`internal/observability/smoke/schema_test.go`](../../../internal/observability/smoke/schema_test.go)
— `TestPrivacySmokeReportRequiresTheClosedEightSurfaceProofSet`. Real local, Tempo/Loki and
Langfuse readers are guarded respectively by
[`internal/observability/backend/privacy_local_surfaces_test.go`](../../../internal/observability/backend/privacy_local_surfaces_test.go),
[`internal/observability/backend/privacy_grafana_test.go`](../../../internal/observability/backend/privacy_grafana_test.go)
and [`internal/observability/backend/privacy_langfuse_test.go`](../../../internal/observability/backend/privacy_langfuse_test.go).

| Surface | Required evidence method | Contract |
| --- | --- | --- |
| API response | `bounded_memory_scan` | Scan the successful chat response in memory before consuming the run manifest; raw bytes are not JSON-serializable and never reach disk. |
| application log | `projection_and_exact_query` | Validate the pre-export OTLP completion-record allowlist and correlate it with the exact Loki structured-metadata result for the same run. |
| Collector queue/report | `configuration_and_telemetry` | Verify the running config digest, bind the same-run pre-queue artifact hash, component identity and export-admission correlation, then obtain component-scoped queue telemetry. This proves the pre-queue contract plus queue component state; it does not claim that queue contents were searched or that a specific record entered/remained in the queue. |
| Tempo | `bounded_trace_document` | TraceQL locates the same run/trace inside the exact window; a bounded unique trace document is scanned without projecting target-only fields into the result. |
| Loki | `exact_structured_query` | Query the same run/request/AI/native trace through structured metadata and scan returned body plus metadata; high-cardinality identity remains non-indexed. |
| Langfuse trace | `bounded_platform_document` | For locked self-hosted Langfuse 3.185, use v1 `/api/public/observations`, bounded to the exact trace/window/filter and 100 rows, then scan fields actually returned by the platform. v2 observations is a v4 capability and is forbidden in this profile. |
| Langfuse score | `bounded_platform_document` | Use v3 `/api/public/v3/scores` with stable projection/trace/observation/window identity and `details,subject`; scan the unique returned score document without treating local projection data as platform facts. |
| report | `contained_artifact_scan` | Scan only the typed manifest-registered, hash-verified prior chat fixture report for this run/window. The current privacy report is protected separately by its serialization guard. |

Every remote response is limited to 1 MiB before decoding or scanning, every result set is limited to 100, and every surface has an independent short context. A zero count without successful read/query/scan proof is `unexpected_evidence`, not success. Unsupported platform capabilities fail closed; adapters must not substitute a broad query, default zero, target-derived identity or direct reads of Collector internal queue files.

Scanner policy `1` is a versioned, closed detector set: synthetic canary; configured credential/key/secret patterns; Authorization/Bearer patterns; configured token patterns; and explicitly recognized PII patterns such as supported email/phone formats. Each category is a non-negative integer. `recognized_pii=0` means only that policy version's documented patterns were not observed, not that arbitrary personal data is universally absent. For the Collector composite, category counts apply to the bound pre-queue artifact, while four required boolean bindings prove config/artifact/component/admission integrity.
