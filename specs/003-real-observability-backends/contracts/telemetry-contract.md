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

Logs are JSON lines. They never contain Authorization, API keys, raw prompt/query/output/tool args, provider error body or recognized PII. Exportable payload capture is limited to metadata-only or redacted content; `content_raw` only creates a local/test `LocalRawPayload` debug artifact and never enters a log or telemetry sink.

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

Synthetic forbidden markers are injected into controlled requests. A platform privacy smoke passes only when exact marker searches across API response, application log, Collector log/queue report, Tempo, Loki, Langfuse trace/score and generated smoke report return zero unredacted hits.
