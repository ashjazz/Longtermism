# Real Backend Acceptance Requirements Checklist

**Purpose**: Validate that the real observability backend completion criteria are complete, measurable, consistent, and ready for implementation.
**Created**: 2026-07-10
**Feature**: [spec.md](../spec.md)
**Decision workbench**: [08-real-backend-decision-workbench.md](../../../docs/observability/08-real-backend-decision-workbench.md)
**History**: Migrated from the 002 decision review checklist; this is the sole active copy after the duplicate was removed from 002.

## T166 Evidence Semantics（2026-08-21）

本清单的 `[X]` 表示相应 completion requirement 已由版本控制内的 task、可执行测试或契约明确
定义并可复验；它不等于对应真实平台已经通过。当前接受的 SmokeReport schema 是 v3。仓库本地
`build/observability/smoke-reports/` 中观察到的 infra 与 persistent-queue 报告属于历史 schema v2，
且该目录被忽略，因此不能关闭当前 v3 live acceptance，也不在本清单注册为 `report:` 证据。

任何未来 live completion 必须在对应的 `Live evidence` 行引用受审、版本控制内的 schema-v3
`passed report`，并在
`specs/003-real-observability-backends/evidence/manifest.sha256` 登记精确内容哈希：报告必须有非空
`marker`，每个 backend check 的 `failure_stage` 必须为 `none`，并且同时记录
`cleanup.temporary_credentials`、`cleanup.temporary_data` 与 `cleanup.residual_resources`。smoke 自建
的临时凭据/数据必须证明撤销或删除；外部注入的长期凭据只能记录 `not_created`，不得由 smoke 撤销。
Compose healthy、静态资产、fake backend、UI 截图或历史报告都不能替代实际 backend 查询。

本次逐项审计保留五个 blocker：最终 release gate、privacy live report、SigNoz query/health 端口、
alert firing/resolved live report，以及 reset hardening。其余勾选项只关闭 requirements-quality 问题；
各 profile 的真实支持状态仍由专用 live checklist 与当前 v3 report 决定。

## Requirement Completeness

- [X] CHK001 Are completion requirements defined for offline gates, Collector configuration, infra-only traffic, real AI traffic, privacy, recovery, dashboards, retention, alternate backends, and documentation? [Completeness, Workbench §决策 8]
  - **Repository evidence**: `task:T164`; `test:hack/observability/quickstart_runbook_test.go::TestQuickstartRunbookCoversFinalOperationalSurface`
- [X] CHK002 Are positive and negative routing requirements both specified for the infra-only endpoint and the AI endpoint? [Coverage, Workbench §决策 8]
  - **Repository evidence**: `task:T199`; `test:internal/observability/smoke/infra_runner_test.go::TestInfrastructureSmokeRunnerContract`; `test:internal/observability/smoke/chat_runner_test.go::TestChatSmokeRunnerContract`
- [X] CHK003 Are requirements defined for all four telemetry destinations in the primary profile? [Completeness, Workbench §决策 8]
  - **Repository evidence**: `task:T054`; `asset:hack/observability/collector_grafana_config_test.sh`
- [X] CHK004 Are AI trace delivery and AI score delivery specified as separate acceptance surfaces? [Completeness, Workbench §决策 3, §决策 8]
  - **Repository evidence**: `task:T106`; `test:internal/observability/smoke/score_runner_test.go::TestScoreSmokeRunnerContract`; `asset:hack/observability/langfuse_collector_test.sh`
- [X] CHK005 Are persistent queue requirements defined for normal delivery, backlog, Collector restart, backend recovery, queue exhaustion, disk failure, and shutdown? [Coverage, Workbench §决策 4, §决策 8]
  - **Repository evidence**: `task:T124`; `test:internal/observability/smoke/storage_failure_runner_test.go::TestRunStorageFailureSmokeQueueExhaustionObservesDroppedEvidence`; `test:internal/observability/smoke/persistent_queue_runner_test.go::TestRunPersistentQueueSmokeRecoversBacklogWithinDrainWindow`
- [X] CHK006 Are alternate-backend support requirements explicit enough to prevent “compose starts” from being treated as end-to-end support? [Clarity, Workbench §决策 2, §决策 8]
  - **Repository evidence**: `task:T144`; `test:internal/observability/smoke/signoz_runner_test.go::TestSignozInfrastructureSmokeRunnerContract`; `contract:specs/003-real-observability-backends/checklists/signoz.md`

## Requirement Clarity

- [X] CHK007 Is every use of “eventually queryable” bounded by a concrete timeout and identified backend query surface? [Clarity, Workbench §决策 8]
  - **Repository evidence**: `task:T175`; `test:internal/observability/smoke/poller_test.go::TestBoundedMarkerPollerQueriesOnceAtDeadlineBeforeTimingOut`
- [X] CHK008 Is the difference between default offline gates, local platform smoke, primary-profile release gates, and alternate-profile compatibility gates unambiguous? [Clarity, Workbench §决策 8]
  - **Repository evidence**: `task:T164`; `test:hack/observability/quickstart_runbook_test.go::TestQuickstartRunbookDefinesGateFrequencyDependenciesAndCost`
- [X] CHK009 Are request, service trace/span, AI trace, platform trace/observation, score, and run-marker identities assigned distinct meanings? [Clarity, Workbench §决策 3, §决策 5]
  - **Repository evidence**: `task:T161`; `contract:specs/003-real-observability-backends/contracts/telemetry-contract.md`
- [X] CHK010 Is “business result unaffected by observability failure” defined for both successful model calls and genuine business/upstream failures? [Clarity, Workbench §决策 8]
  - **Repository evidence**: `task:T126`; `test:internal/logic/chat/failure_classification_test.go::TestFailureDomainsNeverCrossClassify`
- [X] CHK011 Are queue delivery semantics explicitly described as at-least-once rather than exactly-once? [Clarity, Workbench §决策 4, §决策 8]
  - **Repository evidence**: `task:T123`; `test:internal/observability/smoke/persistent_queue_runner_test.go::TestRunPersistentQueueSmokeSurfacesDuplicateDelivery`; `adr:docs/adr/0008-real-observability-backends-and-minimal-http-loop.md`
- [X] CHK012 Are debug-only response fields, their size limits, and `not_run` evaluation semantics objectively specified? [Clarity, Workbench §决策 5]
  - **Repository evidence**: `task:T094`; `test:internal/logic/chat/evaluator_test.go::TestDebugEvalSummaryExposureIsBoundedAndHonorsDebugFlag`; `contract:specs/003-real-observability-backends/contracts/http-api.yaml`

## Requirement Consistency

- [X] CHK013 Is the application’s single Collector dependency consistent with every endpoint and exporter requirement? [Consistency, Workbench §决策 1, §决策 4, §决策 6]
  - **Repository evidence**: `task:T162`; `test:internal/cmd/observability_exporter_test.go::TestObservabilityOTLPExporterConfigurationOwnsOnlyCollectorEndpoint`
- [X] CHK014 Are the three-pipeline topology requirements consistent with the requirement that pure infrastructure traffic never reaches the AI backend? [Consistency, Workbench §决策 4, §决策 8]
  - **Repository evidence**: `task:T054`; `asset:hack/observability/collector_grafana_config_test.sh`; `test:pkg/ai/obs/otel_mapper_test.go::TestMapSpanRoutingAttributesLeavesInfrastructureChildrenUnmarkedWithAILikeInput`
- [X] CHK015 Resolved: `content_raw` is only an explicitly enabled local/test `LocalRawPayload` debug artifact; only metadata-only or redacted snapshots may enter observability. [Workbench §决策 3, §决策 7]
  - **Repository evidence**: `task:T024`; `test:pkg/ai/obs/payload_policy_test.go::TestPayloadPolicyLocalRawPayloadIsExactAndCannotBeSerialized`
- [X] CHK016 Is the persistent storage decision reconciled with the earlier v1 statement that no persistent storage would be added? [Conflict, Workbench §决策 4]
  - **Repository evidence**: `task:T165`; `test:hack/observability/adr_0008_appendix_test.go::TestADR0008AcceptedDecisionBaselineIsAppendOnly`; `adr:docs/adr/0008-real-observability-backends-and-minimal-http-loop.md`
- [X] CHK017 Is real model runtime validation consistent with the requirement that default gates remain offline and free of paid dependencies? [Consistency, Workbench §决策 5, §决策 8]
  - **Repository evidence**: `task:T164`; `test:hack/observability/quickstart_runbook_test.go::TestQuickstartRunbookDefinesGateFrequencyDependenciesAndCost`
- [X] CHK018 Are retention requirements consistent across backend storage, persistent queue, raw-content isolation, local eval evidence, and cleanup commands? [Consistency, Workbench §决策 7, §决策 8]
  - **Repository evidence**: `task:T162`; `contract:specs/003-real-observability-backends/contracts/runtime-configuration.md`; `test:internal/observability/smoke/retention_runner_test.go::TestRunRetentionSmokeVerifiesAllUnitsAndCleansRawArtifacts`

## Acceptance Criteria Quality

- [ ] CHK019 Can every release-gate requirement produce machine-readable pass/fail evidence without manual UI inspection? [Measurability, Workbench §决策 8]
  - **Repository evidence**: `task:T163`; `test:internal/observability/smoke/schema_test.go::TestSmokeReportSchemaValidatorAcceptsEverySupportedScenario`
  - **Live evidence**: `pending:T167` — the final release gate aggregate does not exist yet, so “every release gate” is not closed.
- [X] CHK020 Are the required report fields sufficient to identify profile, marker, backend, failure stage, duration, and cleanup outcome without exposing secrets? [Measurability, Workbench §决策 8]
  - **Repository evidence**: `task:T163`; `test:internal/observability/smoke/schema_test.go::TestSmokeReportSchemaValidatorRejectsFinalClosedVocabularyFixtures`; `test:internal/observability/smoke/report_test.go::TestBuildSmokeReportAggregatesChecksAndOwnedCleanupEvidence`
- [ ] CHK021 Are privacy acceptance criteria based on actual exported/backend-visible data in addition to mapper-level unit tests? [Measurability, Workbench §决策 7, §决策 8]
  - **Repository evidence**: `task:T198`; `test:internal/observability/smoke/privacy_composition_test.go::TestPrivacyCompositionCreatesEachSurfaceBudgetJustInTime`; `test:internal/observability/backend/privacy_grafana_test.go::TestPrivacyGrafanaSurfacesReadRealTempoAndLokiFacts`
  - **Live evidence**: pending until a current `scenario=privacy` schema-v3 `passed report` proves all eight attempted backend-visible surfaces.
- [X] CHK022 Are dashboard acceptance requirements tied to operational questions and provisioned assets rather than merely dashboard existence? [Acceptance Criteria, Workbench §决策 2, §决策 8]
  - **Repository evidence**: `task:T104`; `test:hack/observability/grafana_dashboard_test.go::TestGrafanaOverviewDashboardContract`
- [X] CHK023 Are failure attribution requirements measurable separately for trace, log, metrics pull, AI trace, and AI score delivery? [Acceptance Criteria, Workbench §决策 4, §决策 8]
  - **Repository evidence**: `task:T120`; `test:internal/observability/failure/catalog_test.go::TestNoEvidenceSourceMixingInvariant`
- [X] CHK024 Are coverage thresholds and the scope of “new core code” defined precisely enough to avoid inconsistent enforcement? [Ambiguity, Workbench §决策 8]
  - **Repository evidence**: `task:T036`; `asset:hack/observability/coverage_check_test.sh`

## Scenario And Edge-Case Coverage

- [X] CHK025 Are requirements present for partial success where one exporter fails and every other exporter succeeds? [Coverage, Exception Flow, Workbench §决策 8]
  - **Repository evidence**: `task:T122`; `test:internal/observability/smoke/exporter_failure_runner_test.go::TestRunExporterFailureSmokeAttributesFaultAndPreservesHTTPResult`; `test:internal/observability/smoke/exporter_failure_runner_test.go::TestRunExporterFailureSmokeFailsWhenOtherExporterAlsoFailed`
- [X] CHK026 Are requirements present for delayed asynchronous scores, duplicate score retries, queue overflow, and process crash before score delivery? [Coverage, Recovery, Workbench §决策 6, §决策 8]
  - **Repository evidence**: `task:T184`; `test:internal/observability/smoke/score_runner_test.go::TestScoreSmokeRunnerRejectsDuplicateScoresForOneStableProjectionID`; `test:internal/cmd/langfuse_score_lifecycle_test.go::TestBuildLangfuseScoreLifecycleRecoversPendingBeforeStart`
- [X] CHK027 Are requirements present for missing credentials, malformed endpoints, unsupported protocol selection, and disabled smoke endpoints? [Coverage, Edge Case, Workbench §决策 5, §决策 6]
  - **Repository evidence**: `task:T162`; `test:internal/cmd/observability_runtime_config_test.go::TestResolveObservabilityRuntimeConfig`; `test:internal/cmd/routes_observability_test.go::TestRegisterObservabilityRoutesGatesInfraSmokeAndPreservesRequestIdentity`
- [X] CHK028 Are requirements present for startup failure caused by invalid pipelines, unavailable storage paths, or unwritable persistent volumes? [Coverage, Recovery, Workbench §决策 4, §决策 8]
  - **Repository evidence**: `task:T124`; `test:internal/observability/smoke/storage_failure_runner_test.go::TestRunStorageFailureSmokePreflightRejectionIsStableAndInjectsNothing`; `asset:hack/observability/config_check_test.sh`
- [X] CHK029 Are requirements present for telemetry that arrives after a smoke timeout so stale data cannot satisfy a later run’s marker assertion? [Coverage, Edge Case, Workbench §决策 8]
  - **Repository evidence**: `task:T175`; `test:internal/observability/smoke/poller_test.go::TestBoundedMarkerPollerRejectsOtherAndOutOfWindowMarkers`; `test:internal/observability/smoke/persistent_queue_runner_test.go::TestRunPersistentQueueSmokeIsolatesLateMarker`
- [X] CHK030 Are cleanup requirements explicit for interrupted tests, residual paused containers, raw-content volumes, queues, and temporary credentials? [Coverage, Recovery, Workbench §决策 7, §决策 8]
  - **Repository evidence**: `task:T164`; `test:hack/observability/quickstart_runbook_test.go::TestQuickstartRunbookClosesCredentialAndCleanupOwnership`; `test:internal/observability/smoke/report_test.go::TestBuildSmokeReportMakesCleanupFailurePartOfOverallResult`

## Dependencies And Assumptions

- [ ] CHK031 Are required image versions, ports, health checks, backend query APIs, and local resource budgets identified before implementation? [Dependency, Gap]
  - **Repository evidence**: `task:T162`; `contract:specs/003-real-observability-backends/contracts/runtime-configuration.md`; `test:internal/observability/backend/signoz_query_test.go::TestSignozQueryClientContract`
  - **Live evidence**: pending because the SigNoz publication declares port `3301` while its container health/query path uses `8080`; query viability is not proven.
- [X] CHK032 Is the assumption that the configured model upstream returns required usage and finish-reason fields documented and testable? [Assumption, Workbench §决策 5]
  - **Repository evidence**: `task:T092`; `test:internal/observability/generation_test.go::TestGenerationSpanAdapterRecordsNativeParentageAndExplicitFacts`; `contract:specs/003-real-observability-backends/contracts/http-api.yaml`
- [X] CHK033 Is the assumption that each backend supports the selected retention and query behavior documented, including any local-only workaround? [Assumption, Workbench §决策 7]
  - **Repository evidence**: `task:T162`; `contract:specs/003-real-observability-backends/contracts/runtime-configuration.md`; `test:internal/observability/smoke/retention_runner_test.go::TestRunRetentionSmokeFailsOnRetentionWindowMismatch`
- [X] CHK034 Are the conditions for replacing the in-process score worker with an outbox or external worker specified? [Dependency, Workbench §决策 6]
  - **Repository evidence**: `task:T165`; `test:hack/observability/adr_0008_appendix_test.go::TestADR0008ImplementationAppendixDefinesVerifiedResultsAndRevisitThresholds`; `adr:docs/adr/0008-real-observability-backends-and-minimal-http-loop.md`

## Review Follow-Up: 2026-07-10

- [X] CHK035 Are metrics acceptance requirements based on route/status counter and histogram deltas without introducing request/run IDs as metric labels? [Consistency, Workbench §决策 7, §决策 8]
  - **Repository evidence**: `task:T161`; `test:internal/observability/metrics_test.go::TestMetricsRecordRequiredInstrumentsWithOnlyLowCardinalityAttributes`; `contract:specs/003-real-observability-backends/contracts/telemetry-contract.md`
- [X] CHK036 Is each failure domain mapped to its actual evidence source, distinguishing Collector push exporters, metrics pull, dashboard queries, and the in-process score worker? [Clarity, Workbench §决策 8]
  - **Repository evidence**: `task:T120`; `test:internal/observability/failure/catalog_test.go::TestNoEvidenceSourceMixingInvariant`; `test:internal/observability/failure/catalog_test.go::TestExporterDomainsCarryRealCollectorFacts`
- [X] CHK037 Are PR, configuration-change, milestone, release-candidate, and scheduled-canary gate frequencies explicitly separated? [Completeness, Workbench §决策 8]
  - **Repository evidence**: `task:T164`; `test:hack/observability/quickstart_runbook_test.go::TestQuickstartRunbookDefinesGateFrequencyDependenciesAndCost`
- [X] CHK038 Does the alternate profile explicitly retain the AI semantic backend while replacing only the infrastructure backend? [Consistency, Workbench §决策 2, §决策 8]
  - **Repository evidence**: `task:T142`; `asset:hack/observability/collector_signoz_config_test.sh`; `asset:hack/observability/compose_signoz_test.sh`
- [ ] CHK039 Are alert requirements defined for HTTP errors, exporter failures, queue saturation/age, and Collector storage pressure, including firing and resolved evidence? [Coverage, Workbench §决策 8]
  - **Repository evidence**: `task:T127`; `test:internal/observability/smoke/alert_runner_test.go::TestRunAlertSmokeCoversAllFourAlertClasses`; `test:hack/observability/grafana_alerts_test.go::TestGrafanaAlertRulesContract`
  - **Live evidence**: pending until a future report schema records each distinct alert class and a current `scenario=alert` schema-v3 passed report proves all four classes reached both `firing` and `resolved`; current aggregate counts cannot close this item.
- [ ] CHK040 Are destructive reset requirements protected by explicit confirmation, scoped resource labels, a deletion preview, and exclusion of unrelated volumes? [Security, Workbench §决策 8]
  - **Repository evidence**: `task:T164`; `test:hack/observability/quickstart_runbook_test.go::TestQuickstartRunbookClosesCredentialAndCleanupOwnership`; `asset:hack/observability/reset_test.sh`
  - **Live evidence**: pending reset hardening: named volumes still lack required `volume labels`, and `run-root` containment/symlink validation is not fail-closed; only inventory preview is allowed.
- [X] CHK041 Is `obs-platform-smoke` explicitly scoped as a local controlled-sender integration contract, with real backend delivery/query reserved for the primary and alternate E2E gates? [Clarity, Workbench §决策 8]
  - **Repository evidence**: `task:T157`; `test:internal/eval/smoke/platform_report_test.go::TestPlatformSmokeReportMarksRealBackendsSkippedWithScopeNote`; `test:internal/eval/smoke/platform_smoke_test.go::TestPlatformSmokeLocalRunSkipsByDefaultWithZeroExternalAttempts`
