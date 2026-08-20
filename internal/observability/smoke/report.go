// Package smoke contains bounded, offline-verifiable observability smoke primitives.
package smoke

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"time"
)

const (
	smokeReportSchemaVersion = "2"
	minimumSmokeIdentitySize = 8
	maximumSmokeIdentitySize = 128
)

var (
	errInvalidSmokeReport       = errors.New("invalid smoke report")
	errSensitiveSmokeReportData = errors.New("sensitive smoke report data is not allowed")
)

var (
	allowedProfiles  = stringSet("grafana", "signoz", "local")
	allowedScenarios = stringSet(
		"infra", "chat", "score", "privacy", "exporter_failure", "persistent_queue",
		"storage_failure", "score_worker_failure", "alert", "retention", "platform_contract", "full",
	)
	allowedBackends                    = stringSet("api", "collector", "tempo", "loki", "prometheus", "grafana", "langfuse_trace", "langfuse_score", "signoz", "signoz_traces", "signoz_logs", "signoz_metrics", "privacy")
	allowedStatuses                    = stringSet("passed", "failed", "skipped")
	allowedFailureStages               = stringSet("none", "preflight", "api", "export", "query", "cleanup")
	allowedCleanupStatuses             = stringSet("not_required", "completed", "failed")
	allowedTemporaryCredentialStatuses = stringSet("not_created", "revoked", "deleted", "failed")
	allowedTemporaryDataStatuses       = stringSet("not_created", "deleted", "failed")
	allowedErrorClasses                = stringSet("authentication_failed", "backend_timeout", "temporary_credential_revoke_failed", "backend_unavailable", "export_failed", "identity_mismatch", "invalid_query", "malformed_response", "marker_missing", "query_failed", "metric_delta_missing", "unexpected_evidence", "storage_unavailable", "queue_full", "alert_not_firing", "alert_not_resolved", "invalid_configuration", "retention_violation", "score_projection_missing")
	allowedVersionKeys                 = stringSet("api", "collector", "grafana", "langfuse", "loki", "prometheus", "schema", "signoz", "smoke_runner", "tempo")
	allowedResidualResources           = stringSet("run-directory", "temporary-debug-data", "temporary-queue-data", "paused-service", "unwritable-storage", "langfuse-api-unavailable", "score-worker-queue-full", "alert-condition-active")
	allowedEvidenceKeysByBackend       = map[string]map[string]struct{}{
		"api": stringSet("cleanup_attempted", "marker_seen", "response_status", "retention_days"),
		"collector": stringSet("exporter_failed", "exporter_sent", "marker_received", "queue_depth",
			"tempo_sent_delta", "tempo_failed_delta", "tempo_enqueue_delta", "tempo_queue_delta",
			"loki_sent_delta", "loki_failed_delta", "loki_enqueue_delta", "loki_queue_delta",
			"langfuse_sent_delta", "langfuse_failed_delta", "langfuse_enqueue_delta", "langfuse_queue_delta",
			"duplicate_delivered", "enqueue_failed_delta", "dropped_delta", "storage_writable", "shutdown_timed_out"),
		"tempo":          stringSet("matched_spans", "retention_days", "raw_payload_found"),
		"loki":           stringSet("matched_logs", "retention_days", "raw_payload_found"),
		"prometheus":     stringSet("metric_delta", "target_up", "retention_days", "raw_payload_found"),
		"grafana":        stringSet("datasource_healthy", "query_succeeded", "alerts_firing", "alerts_resolved"),
		"langfuse_trace": stringSet("matched_traces", "retention_days", "raw_payload_found"),
		"langfuse_score": stringSet("matched_scores", "score_attempts", "dropped_projections", "local_evidence_intact", "shutdown_timed_out"),
		"signoz":         stringSet("matched_logs", "matched_spans"),
		// 备选 profile 的三信号是三个独立证据面：分信号 backend 让 per-signal
		// 失败归因与 dashboard/检查单对齐（T139/T140 契约），而不是混成一个 signoz。
		"signoz_traces":  stringSet("matched_spans"),
		"signoz_logs":    stringSet("matched_logs"),
		"signoz_metrics": stringSet("metric_delta"),
		"privacy":        stringSet("forbidden_marker_hits"),
	}
	smokeVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,63}$`)
)

// SmokeReportInput contains only the low-sensitivity facts eligible for report persistence.
// Raw credentials, payloads, and paths intentionally have no field here: callers must keep such
// local debug artifacts outside this value object so generic serialization cannot leak them.
type SmokeReportInput struct {
	RunID      string
	Marker     string
	Profile    string
	Scenario   string
	RequestID  string
	AITraceID  string
	StartedAt  time.Time
	Deadline   time.Time
	FinishedAt time.Time
	Versions   map[string]string
	Checks     []BackendCheckInput
	Cleanup    SmokeCleanupInput
	// PrivacyEvidence is mandatory for the privacy scenario and forbidden elsewhere: the
	// eight ordered surface proofs cannot be impersonated by a generic check.
	PrivacyEvidence []PrivacySmokeReportEvidenceInput
}

type BackendCheckInput struct {
	Backend      string
	Status       string
	Duration     time.Duration
	FailureStage string
	ErrorClass   string
	Evidence     map[string]any
}

type SmokeCleanupInput struct {
	Status               string
	ResidualResources    []string
	TemporaryCredentials string
	TemporaryData        string
}

// PrivacySmokeReportEvidenceInput is the closed per-surface proof projected into the privacy
// report. Counts and the collector bindings are the only allowed facts; artifact refs, raw
// payloads and credentials have no field here so generic serialization cannot leak them.
type PrivacySmokeReportEvidenceInput struct {
	Surface                PrivacySmokeSurface
	EvidenceMethod         string
	Status                 string
	ScannerPolicyVersion   string
	Counts                 map[string]int
	CollectorProofVerified bool
}

// privacySmokeReportEvidence is the wire shape validated by the version-controlled schema.
// Bindings are only emitted for collector_queue: additionalProperties=false forbids them
// elsewhere, and the schema pins them to const true for that surface.
type privacySmokeReportEvidence struct {
	Surface                      string         `json:"surface"`
	EvidenceMethod               string         `json:"evidence_method"`
	Attempted                    bool           `json:"attempted"`
	Status                       string         `json:"status"`
	ScannerPolicyVersion         string         `json:"scanner_policy_version"`
	Counts                       map[string]int `json:"counts"`
	RuntimeConfigDigestVerified  bool           `json:"runtime_config_digest_verified,omitempty"`
	PrequeueArtifactHashVerified bool           `json:"prequeue_artifact_hash_verified,omitempty"`
	ComponentIdentityVerified    bool           `json:"component_identity_verified,omitempty"`
	ExportAdmissionCorrelated    bool           `json:"export_admission_correlated,omitempty"`
}

// SmokeReport is immutable after construction. The unexported state prevents callers from
// mutating a completed report between validation and persistence; accessors return copies.
type SmokeReport struct {
	runID      string
	marker     string
	profile    string
	scenario   string
	requestID  string
	aiTraceID  string
	startedAt  time.Time
	finishedAt time.Time
	status     string
	versions   map[string]string
	checks     []BackendCheck
	cleanup    SmokeCleanup
	// privacyEvidence is the ordered eight-surface proof set for the privacy scenario.
	privacyEvidence []privacySmokeReportEvidence
}

type BackendCheck struct {
	Backend      string         `json:"backend"`
	Status       string         `json:"status"`
	DurationMS   int64          `json:"duration_ms"`
	FailureStage string         `json:"failure_stage"`
	ErrorClass   string         `json:"error_class,omitempty"`
	Evidence     map[string]any `json:"evidence,omitempty"`
}

type SmokeCleanup struct {
	Status               string   `json:"status"`
	ResidualResources    []string `json:"residual_resources"`
	TemporaryCredentials string   `json:"temporary_credentials"`
	TemporaryData        string   `json:"temporary_data"`
}

type smokeReportJSON struct {
	SchemaVersion   string                       `json:"schema_version"`
	RunID           string                       `json:"run_id"`
	Marker          string                       `json:"marker"`
	Profile         string                       `json:"profile"`
	Scenario        string                       `json:"scenario"`
	RequestID       string                       `json:"request_id,omitempty"`
	AITraceID       string                       `json:"ai_trace_id,omitempty"`
	StartedAt       string                       `json:"started_at"`
	FinishedAt      string                       `json:"finished_at"`
	Status          string                       `json:"status"`
	Versions        map[string]string            `json:"versions,omitempty"`
	Checks          []BackendCheck               `json:"checks"`
	Cleanup         SmokeCleanup                 `json:"cleanup"`
	PrivacyEvidence []privacySmokeReportEvidence `json:"privacy_evidence,omitempty"`
}

func BuildSmokeReport(input SmokeReportInput) (*SmokeReport, error) {
	if err := validateSmokeReportInput(input); err != nil {
		return nil, err
	}

	checks := cloneChecks(input.Checks)
	cleanup := cloneCleanup(input.Cleanup)
	return &SmokeReport{
		runID:           input.RunID,
		marker:          input.Marker,
		profile:         input.Profile,
		scenario:        input.Scenario,
		requestID:       input.RequestID,
		aiTraceID:       input.AITraceID,
		startedAt:       input.StartedAt.UTC(),
		finishedAt:      input.FinishedAt.UTC(),
		status:          aggregateSmokeStatus(checks, cleanup, clonePrivacySmokeEvidence(input.PrivacyEvidence)),
		versions:        cloneVersions(input.Versions),
		checks:          checks,
		cleanup:         cleanup,
		privacyEvidence: clonePrivacySmokeEvidence(input.PrivacyEvidence),
	}, nil
}

func (r SmokeReport) MarshalJSON() ([]byte, error) {
	return json.Marshal(smokeReportJSON{
		SchemaVersion:   smokeReportSchemaVersion,
		RunID:           r.runID,
		Marker:          r.marker,
		Profile:         r.profile,
		Scenario:        r.scenario,
		RequestID:       r.requestID,
		AITraceID:       r.aiTraceID,
		StartedAt:       r.startedAt.Format(time.RFC3339Nano),
		FinishedAt:      r.finishedAt.Format(time.RFC3339Nano),
		Status:          r.status,
		Versions:        r.Versions(),
		Checks:          r.Checks(),
		Cleanup:         r.Cleanup(),
		PrivacyEvidence: clonePrivacySmokeWireEvidence(r.privacyEvidence),
	})
}

func (r SmokeReport) Checks() []BackendCheck { return cloneSafeChecks(r.checks) }

// Status returns the already-validated aggregate outcome without exposing the report's internal
// representation. Command composition roots need this one low-sensitivity fact for exit codes.
func (r SmokeReport) Status() string { return r.status }

// Scenario returns the sealed scenario identity without exposing mutable state. The smoke CLI
// composition root uses it to name persisted artifacts; it never derives facts from the caller.
func (r SmokeReport) Scenario() string { return r.scenario }

// Marker returns the sealed runner-owned marker identity. Composition roots use it to enforce
// marker uniqueness when aggregating several scenario reports; it must never be treated as a
// caller-supplied input.
func (r SmokeReport) Marker() string { return r.marker }

func (r SmokeReport) Cleanup() SmokeCleanup { return cloneSafeCleanup(r.cleanup) }

func (r SmokeReport) Versions() map[string]string { return cloneVersions(r.versions) }

func validateSmokeReportInput(input SmokeReportInput) error {
	if !isOpaqueSmokeIdentity(input.RunID) ||
		!isOpaqueSmokeIdentity(input.Marker) ||
		!contains(allowedProfiles, input.Profile) ||
		!contains(allowedScenarios, input.Scenario) ||
		input.StartedAt.IsZero() ||
		input.Deadline.IsZero() ||
		input.FinishedAt.IsZero() ||
		input.FinishedAt.After(input.Deadline) ||
		input.FinishedAt.Before(input.StartedAt) ||
		len(input.Checks) == 0 {
		return errInvalidSmokeReport
	}
	if (input.RequestID == "") != (input.AITraceID == "") || input.RequestID != "" && !isSafePollMarker(input.RequestID) || input.AITraceID != "" && !isSafePollMarker(input.AITraceID) {
		return errInvalidSmokeReport
	}
	if err := validateVersions(input.Versions); err != nil {
		return err
	}
	for _, check := range input.Checks {
		if err := validateCheck(check); err != nil {
			return err
		}
	}
	if err := validatePrivacySmokeEvidence(input.Scenario, input.PrivacyEvidence); err != nil {
		return err
	}
	return validateCleanup(input.Cleanup)
}

// validatePrivacySmokeEvidence enforces the closed eight-surface proof set for the privacy
// scenario: fixed order, fixed evidence method per surface, scanner policy 1, five non-negative
// category counts and the four verified collector bindings. Non-privacy reports must not carry
// a proof set, otherwise the schema's conditional requirement would reject the document.
func validatePrivacySmokeEvidence(scenario string, evidence []PrivacySmokeReportEvidenceInput) error {
	if scenario != "privacy" {
		if len(evidence) != 0 {
			return errSensitiveSmokeReportData
		}
		return nil
	}
	if len(evidence) != len(privacyCompositionSchemaOrder) {
		return errInvalidSmokeReport
	}
	for index, surface := range privacyCompositionSchemaOrder {
		item := evidence[index]
		if item.Surface != surface || item.EvidenceMethod != privacyCompositionMethod(surface) ||
			item.ScannerPolicyVersion != "1" || item.Status != "passed" && item.Status != "failed" ||
			!validPrivacyCompositionCounts(item.Counts) {
			return errInvalidSmokeReport
		}
		if surface == PrivacySmokeSurfaceCollectorQueue && !item.CollectorProofVerified {
			return errInvalidSmokeReport
		}
	}
	return nil
}

func clonePrivacySmokeEvidence(input []PrivacySmokeReportEvidenceInput) []privacySmokeReportEvidence {
	if input == nil {
		return nil
	}
	evidence := make([]privacySmokeReportEvidence, len(input))
	for index, item := range input {
		itemWire := privacySmokeReportEvidence{
			Surface: string(item.Surface), EvidenceMethod: item.EvidenceMethod, Attempted: true,
			Status: item.Status, ScannerPolicyVersion: item.ScannerPolicyVersion, Counts: clonePrivacyCounts(item.Counts),
		}
		if item.Surface == PrivacySmokeSurfaceCollectorQueue && item.CollectorProofVerified {
			itemWire.RuntimeConfigDigestVerified, itemWire.PrequeueArtifactHashVerified = true, true
			itemWire.ComponentIdentityVerified, itemWire.ExportAdmissionCorrelated = true, true
		}
		evidence[index] = itemWire
	}
	return evidence
}

func clonePrivacySmokeWireEvidence(input []privacySmokeReportEvidence) []privacySmokeReportEvidence {
	if input == nil {
		return nil
	}
	evidence := make([]privacySmokeReportEvidence, len(input))
	for index, item := range input {
		item.Counts = clonePrivacyCounts(item.Counts)
		evidence[index] = item
	}
	return evidence
}

func validateVersions(versions map[string]string) error {
	for key, value := range versions {
		if !contains(allowedVersionKeys, key) || !smokeVersionPattern.MatchString(value) {
			return errSensitiveSmokeReportData
		}
	}
	return nil
}

func validateCheck(check BackendCheckInput) error {
	if !contains(allowedBackends, check.Backend) ||
		!contains(allowedStatuses, check.Status) ||
		check.Duration < 0 ||
		!contains(allowedFailureStages, check.FailureStage) {
		return errInvalidSmokeReport
	}
	if (check.Status == "passed" || check.Status == "skipped") && check.FailureStage != "none" || check.Status == "failed" && check.FailureStage == "none" {
		return errInvalidSmokeReport
	}
	if check.ErrorClass != "" && (!contains(allowedErrorClasses, check.ErrorClass) || check.Status != "failed") {
		return errSensitiveSmokeReportData
	}
	allowedEvidenceKeys, exists := allowedEvidenceKeysByBackend[check.Backend]
	for key, value := range check.Evidence {
		if !exists || !contains(allowedEvidenceKeys, key) || !isSafeEvidenceValue(value) {
			return errSensitiveSmokeReportData
		}
	}
	return nil
}

func validateCleanup(cleanup SmokeCleanupInput) error {
	if !contains(allowedCleanupStatuses, cleanup.Status) ||
		!contains(allowedTemporaryCredentialStatuses, cleanup.TemporaryCredentials) ||
		!contains(allowedTemporaryDataStatuses, cleanup.TemporaryData) {
		return errSensitiveSmokeReportData
	}
	for _, resource := range cleanup.ResidualResources {
		if !contains(allowedResidualResources, resource) {
			return errSensitiveSmokeReportData
		}
	}
	if cleanup.Status == "completed" && (cleanup.TemporaryCredentials == "failed" || cleanup.TemporaryData == "failed" || len(cleanup.ResidualResources) != 0) {
		return errInvalidSmokeReport
	}
	if cleanup.Status == "failed" && cleanup.TemporaryCredentials != "failed" && cleanup.TemporaryData != "failed" && len(cleanup.ResidualResources) == 0 {
		return errInvalidSmokeReport
	}
	if cleanup.Status == "not_required" && (cleanup.TemporaryCredentials != "not_created" || cleanup.TemporaryData != "not_created" || len(cleanup.ResidualResources) != 0) {
		return errInvalidSmokeReport
	}
	return nil
}

func isSafeEvidenceValue(value any) bool {
	switch typed := value.(type) {
	case nil, bool, int, int8, int16, int32, int64:
		return true
	case float32:
		return !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0)
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	default:
		return false
	}
}

func isOpaqueSmokeIdentity(value string) bool {
	// run_id 与 marker 是 runner 生成的低敏 opaque identity；报告层只执行公开 schema
	// 规定的长度边界，以免把 UUID 或其他合法 opaque 表达错误地拒绝。
	return len(value) >= minimumSmokeIdentitySize && len(value) <= maximumSmokeIdentitySize
}

func aggregateSmokeStatus(checks []BackendCheck, cleanup SmokeCleanup, privacyEvidence []privacySmokeReportEvidence) string {
	if cleanup.Status == "failed" {
		return "failed"
	}
	allSkipped := true
	for _, check := range checks {
		if check.Status == "failed" {
			return "failed"
		}
		if check.Status != "skipped" {
			allSkipped = false
		}
	}
	for _, item := range privacyEvidence {
		if item.Status == "failed" {
			return "failed"
		}
		allSkipped = false
	}
	if allSkipped {
		return "skipped"
	}
	return "passed"
}

func cloneChecks(inputs []BackendCheckInput) []BackendCheck {
	checks := make([]BackendCheck, len(inputs))
	for index, input := range inputs {
		checks[index] = BackendCheck{Backend: input.Backend, Status: input.Status, DurationMS: input.Duration.Milliseconds(), FailureStage: input.FailureStage, ErrorClass: input.ErrorClass, Evidence: cloneEvidence(input.Evidence)}
	}
	return checks
}

func cloneSafeChecks(inputs []BackendCheck) []BackendCheck {
	checks := make([]BackendCheck, len(inputs))
	for index, input := range inputs {
		checks[index] = BackendCheck{Backend: input.Backend, Status: input.Status, DurationMS: input.DurationMS, FailureStage: input.FailureStage, ErrorClass: input.ErrorClass, Evidence: cloneEvidence(input.Evidence)}
	}
	return checks
}

func cloneEvidence(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneCleanup(input SmokeCleanupInput) SmokeCleanup {
	return SmokeCleanup{Status: input.Status, ResidualResources: cloneResources(input.ResidualResources), TemporaryCredentials: input.TemporaryCredentials, TemporaryData: input.TemporaryData}
}

func cloneSafeCleanup(input SmokeCleanup) SmokeCleanup {
	return SmokeCleanup{Status: input.Status, ResidualResources: cloneResources(input.ResidualResources), TemporaryCredentials: input.TemporaryCredentials, TemporaryData: input.TemporaryData}
}

func cloneResources(input []string) []string {
	if input == nil {
		return nil
	}
	result := make([]string, len(input))
	copy(result, input)
	return result
}

func cloneVersions(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func contains(values map[string]struct{}, value string) bool {
	_, exists := values[value]
	return exists
}
