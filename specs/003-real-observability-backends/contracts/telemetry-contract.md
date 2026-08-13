# Telemetry Contract

## 1. Planes and routing

| Traffic | Infra pipeline | AI pipeline | Langfuse |
| --- | --- | --- | --- |
| health/ping | yes, subject to sampling | no | no |
| infra-smoke | yes, forced for smoke | no | no |
| chat root/bridge | yes | yes | yes |
| chat AI semantic spans | yes | yes | yes |
| chat DB/Redis/ordinary HTTP child spans | yes | no unless explicitly semantic | no |

AI routing requires:

```text
longtermism.observability.plane = "ai"
```

Only root/bridge and AI semantic spans carry it. Absence means infra-only; no adapter may infer AI from route names.

## 2. Span names

```text
HTTP root          HTTP <METHOD> <route-template>
AI bridge          ai.chat
generation         ai.generation
evaluator          ai.evaluator
```

Future retriever/tool/agent spans reuse the 002 observation type contract but are outside the first chat endpoint implementation.

## 3. Required span attributes

### Shared/root

- `service.name`, `service.version`, `deployment.environment`
- `http.request.method`, `http.route`, `http.response.status_code`
- `request.id`
- `longtermism.smoke.run_id` only for smoke requests
- standard OTel trace/span identity from SpanContext

### AI root/bridge

- `longtermism.observability.plane=ai`
- `longtermism.ai.trace_id`
- `ai.feature=chat`
- result/outcome and stable failure class

For an authenticated live-chat smoke, the same runner marker is copied from the trusted local
context to the HTTP root, `ai.chat`, `ai.generation`, `ai.evaluator`, and the controlled HTTP
completion log. It is never inferred from a route, request ID, AI trace ID or caller baggage.
Ordinary chat requests do not acquire a marker.

### Generation

- AI plane marker and correlation attributes
- standard `gen_ai.*` attributes where stable and available
- Langfuse mapping attributes only in platform adapter/downstream transform
- requested/actual model, provider, finish reason
- input/output/reasoning/cache token counts
- total latency; TTFT omitted for non-streaming chat
- prompt identity/hash/version, never raw prompt in metadata-only mode
- payload policy mode and redaction status

## 4. Identity mapping

| Fact | Attribute/report field | Notes |
| --- | --- | --- |
| API request | `request.id` / `request_id` | always returned |
| OTel trace | native TraceID | source of platform trace target |
| OTel span | native SpanID | source of observation target |
| AI domain trace | `longtermism.ai.trace_id` | opaque domain identity |
| eval run | `longtermism.eval.run_id` | evidence/report only |
| smoke run | `longtermism.smoke.run_id` | span/log/report, not metrics |

The live runner receives native service TraceID/SpanID only from the active `SpanContext`. After
the request completes, the application atomically writes request ID, AI trace ID, service
TraceID/SpanID and smoke run identity to a contained mode-0600 local run manifest. The manifest is
single-consumer and must not contain the smoke credential, payload, endpoint or provider body.
Public chat response metadata remains limited to request ID, AI trace ID and the optional safe
evaluation summary; adapters must never derive native identity from `ai_trace_id`.

## 5. Metrics

Required first-wave instruments:

| Metric semantic | Type | Allowed labels |
| --- | --- | --- |
| HTTP requests | counter | route template, method, status class |
| HTTP duration | histogram | route template, method, status class |
| LLM requests | counter | provider, requested model, outcome |
| LLM duration | histogram | provider, requested model, outcome |
| LLM tokens | counter | provider, model, token type |
| LLM cost-ready | counter/gauge | provider, model, currency/estimate status |
| eval result | counter/histogram | evaluator, status, metric name |
| score projection | counter | backend, status |
| score worker queue | gauge | backend |

Forbidden labels include request ID, trace ID, span ID, AI trace ID, session/user ID, raw route, prompt hash and smoke run ID.

Prometheus smoke compares route/status counter and histogram deltas before/after a request; it never queries a request label.

## 6. Structured logs

Every HTTP completion/error log contains:

- UTC timestamp, level, message
- `request_id`, OTel `trace_id`, `span_id`
- route template, method, status, duration
- stable error class when failed
- `ai_trace_id` only for AI requests
- `smoke_run_id` only for smoke runs

Production completion logs are OTel LogRecords sent to the Collector. Their body is one of the fixed completion messages and the fields above are attributes; Loki stores the high-cardinality identities as structured metadata. JSONL is an explicit local diagnostic opt-in only and is never a smoke or Loki ingestion prerequisite. Neither form contains Authorization, API keys, raw prompt/query/output/tool args, provider error body or recognized PII. Exportable payload capture is limited to metadata-only or redacted content; `content_raw` only creates a local/test `LocalRawPayload` debug artifact and never enters a log or telemetry sink.

## 7. Langfuse mapping

- Collector exports only spans passing AI filter.
- OTLP transport is HTTP/protobuf to `/api/public/otel` or `/v1/traces` under that base.
- Include required Basic Auth and ingestion version header through secret/env injection.
- Generation span maps model, usage, latency, status, prompt identity and permitted content fields.
- Trace-level filterable attributes required by Langfuse are copied only from the explicit allowlist; baggage propagation remains low-sensitive.
- Score API uses platform TraceID and optional observation SpanID plus stable idempotency key.

## 8. Sampling

- Local/smoke: 100% traces.
- Production: application head sampling remains configurable; Collector tail sampling retains failures, degradation, eval regression, smoke and designated AI traces at 100%, with configurable ratio for ordinary success.
- Sampling must preserve complete trace decisions; a smoke run cannot pass if the required trace was sampled away.

## 9. Failure semantics

| Failure domain | Required evidence |
| --- | --- |
| Tempo/Loki/Langfuse exporter | component-scoped sent/failed/enqueue/queue metrics |
| Prometheus | target `up`, scrape error/duration and metric deltas |
| Grafana | datasource health/query result |
| Langfuse score worker | queued/sent/failed/dropped counters and local evidence |
| model provider | business error class and AI trace, not telemetry-export failure |
| Collector storage | writable/health, queue age/capacity and storage error |

Exporter failures never change HTTP business envelopes. Model failures retain their original business status and `ai_trace_id`.

## 10. Privacy assertions

Synthetic forbidden markers are injected only through the authenticated, loopback-only fixture trigger. A privacy smoke passes only after the schema-fixed, ordered set of all eight closed surfaces records `attempted=true`, the surface-specific evidence method, scanner policy version and zero counts for every policy category (`synthetic_canary`, `credential`, `authorization`, `token`, `recognized_pii`). Missing, duplicate, skipped or default-zero entries are invalid rather than successful evidence.

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
