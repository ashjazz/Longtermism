# Real Backend Acceptance Requirements Checklist

**Purpose**: Validate that the real observability backend completion criteria are complete, measurable, consistent, and ready for implementation.
**Created**: 2026-07-10
**Feature**: [spec.md](../spec.md)
**Decision workbench**: [08-real-backend-decision-workbench.md](../../../docs/observability/08-real-backend-decision-workbench.md)
**History**: Migrated from the 002 decision review checklist; this is the sole active copy after the duplicate was removed from 002.

## Requirement Completeness

- [ ] CHK001 Are completion requirements defined for offline gates, Collector configuration, infra-only traffic, real AI traffic, privacy, recovery, dashboards, retention, alternate backends, and documentation? [Completeness, Workbench §决策 8]
- [ ] CHK002 Are positive and negative routing requirements both specified for the infra-only endpoint and the AI endpoint? [Coverage, Workbench §决策 8]
- [ ] CHK003 Are requirements defined for all four telemetry destinations in the primary profile? [Completeness, Workbench §决策 8]
- [ ] CHK004 Are AI trace delivery and AI score delivery specified as separate acceptance surfaces? [Completeness, Workbench §决策 3, §决策 8]
- [ ] CHK005 Are persistent queue requirements defined for normal delivery, backlog, Collector restart, backend recovery, queue exhaustion, disk failure, and shutdown? [Coverage, Workbench §决策 4, §决策 8]
- [ ] CHK006 Are alternate-backend support requirements explicit enough to prevent “compose starts” from being treated as end-to-end support? [Clarity, Workbench §决策 2, §决策 8]

## Requirement Clarity

- [ ] CHK007 Is every use of “eventually queryable” bounded by a concrete timeout and identified backend query surface? [Clarity, Workbench §决策 8]
- [ ] CHK008 Is the difference between default offline gates, local platform smoke, primary-profile release gates, and alternate-profile compatibility gates unambiguous? [Clarity, Workbench §决策 8]
- [ ] CHK009 Are request, service trace/span, AI trace, platform trace/observation, score, and run-marker identities assigned distinct meanings? [Clarity, Workbench §决策 3, §决策 5]
- [ ] CHK010 Is “business result unaffected by observability failure” defined for both successful model calls and genuine business/upstream failures? [Clarity, Workbench §决策 8]
- [ ] CHK011 Are queue delivery semantics explicitly described as at-least-once rather than exactly-once? [Clarity, Workbench §决策 4, §决策 8]
- [ ] CHK012 Are debug-only response fields, their size limits, and `not_run` evaluation semantics objectively specified? [Clarity, Workbench §决策 5]

## Requirement Consistency

- [ ] CHK013 Is the application’s single Collector dependency consistent with every endpoint and exporter requirement? [Consistency, Workbench §决策 1, §决策 4, §决策 6]
- [ ] CHK014 Are the three-pipeline topology requirements consistent with the requirement that pure infrastructure traffic never reaches the AI backend? [Consistency, Workbench §决策 4, §决策 8]
- [X] CHK015 Resolved: `content_raw` is not supported; only metadata-only or redacted content may enter observability. [Workbench §决策 3, §决策 7]
- [ ] CHK016 Is the persistent storage decision reconciled with the earlier v1 statement that no persistent storage would be added? [Conflict, Workbench §决策 4]
- [ ] CHK017 Is real model runtime validation consistent with the requirement that default gates remain offline and free of paid dependencies? [Consistency, Workbench §决策 5, §决策 8]
- [ ] CHK018 Are retention requirements consistent across backend storage, persistent queue, raw-content isolation, local eval evidence, and cleanup commands? [Consistency, Workbench §决策 7, §决策 8]

## Acceptance Criteria Quality

- [ ] CHK019 Can every release-gate requirement produce machine-readable pass/fail evidence without manual UI inspection? [Measurability, Workbench §决策 8]
- [ ] CHK020 Are the required report fields sufficient to identify profile, marker, backend, failure stage, duration, and cleanup outcome without exposing secrets? [Measurability, Workbench §决策 8]
- [ ] CHK021 Are privacy acceptance criteria based on actual exported/backend-visible data in addition to mapper-level unit tests? [Measurability, Workbench §决策 7, §决策 8]
- [ ] CHK022 Are dashboard acceptance requirements tied to operational questions and provisioned assets rather than merely dashboard existence? [Acceptance Criteria, Workbench §决策 2, §决策 8]
- [ ] CHK023 Are failure attribution requirements measurable separately for trace, log, metrics pull, AI trace, and AI score delivery? [Acceptance Criteria, Workbench §决策 4, §决策 8]
- [ ] CHK024 Are coverage thresholds and the scope of “new core code” defined precisely enough to avoid inconsistent enforcement? [Ambiguity, Workbench §决策 8]

## Scenario And Edge-Case Coverage

- [ ] CHK025 Are requirements present for partial success where one exporter fails and every other exporter succeeds? [Coverage, Exception Flow, Workbench §决策 8]
- [ ] CHK026 Are requirements present for delayed asynchronous scores, duplicate score retries, queue overflow, and process crash before score delivery? [Coverage, Recovery, Workbench §决策 6, §决策 8]
- [ ] CHK027 Are requirements present for missing credentials, malformed endpoints, unsupported protocol selection, and disabled smoke endpoints? [Coverage, Edge Case, Workbench §决策 5, §决策 6]
- [ ] CHK028 Are requirements present for startup failure caused by invalid pipelines, unavailable storage paths, or unwritable persistent volumes? [Coverage, Recovery, Workbench §决策 4, §决策 8]
- [ ] CHK029 Are requirements present for telemetry that arrives after a smoke timeout so stale data cannot satisfy a later run’s marker assertion? [Coverage, Edge Case, Workbench §决策 8]
- [ ] CHK030 Are cleanup requirements explicit for interrupted tests, residual paused containers, raw-content volumes, queues, and temporary credentials? [Coverage, Recovery, Workbench §决策 7, §决策 8]

## Dependencies And Assumptions

- [ ] CHK031 Are required image versions, ports, health checks, backend query APIs, and local resource budgets identified before implementation? [Dependency, Gap]
- [ ] CHK032 Is the assumption that the configured model upstream returns required usage and finish-reason fields documented and testable? [Assumption, Workbench §决策 5]
- [ ] CHK033 Is the assumption that each backend supports the selected retention and query behavior documented, including any local-only workaround? [Assumption, Workbench §决策 7]
- [ ] CHK034 Are the conditions for replacing the in-process score worker with an outbox or external worker specified? [Dependency, Workbench §决策 6]

## Review Follow-Up: 2026-07-10

- [ ] CHK035 Are metrics acceptance requirements based on route/status counter and histogram deltas without introducing request/run IDs as metric labels? [Consistency, Workbench §决策 7, §决策 8]
- [ ] CHK036 Is each failure domain mapped to its actual evidence source, distinguishing Collector push exporters, metrics pull, dashboard queries, and the in-process score worker? [Clarity, Workbench §决策 8]
- [ ] CHK037 Are PR, configuration-change, milestone, release-candidate, and scheduled-canary gate frequencies explicitly separated? [Completeness, Workbench §决策 8]
- [ ] CHK038 Does the alternate profile explicitly retain the AI semantic backend while replacing only the infrastructure backend? [Consistency, Workbench §决策 2, §决策 8]
- [ ] CHK039 Are alert requirements defined for HTTP errors, exporter failures, queue saturation/age, and Collector storage pressure, including firing and resolved evidence? [Coverage, Workbench §决策 8]
- [ ] CHK040 Are destructive reset requirements protected by explicit confirmation, scoped resource labels, a deletion preview, and exclusion of unrelated volumes? [Security, Workbench §决策 8]
- [ ] CHK041 Is `obs-platform-smoke` explicitly scoped as a local controlled-sender integration contract, with real backend delivery/query reserved for the primary and alternate E2E gates? [Clarity, Workbench §决策 8]
