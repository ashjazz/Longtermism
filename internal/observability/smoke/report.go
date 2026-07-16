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
	allowedBackends                    = stringSet("api", "collector", "tempo", "loki", "prometheus", "grafana", "langfuse_trace", "langfuse_score", "signoz", "privacy")
	allowedStatuses                    = stringSet("passed", "failed", "skipped")
	allowedFailureStages               = stringSet("none", "preflight", "api", "export", "query", "cleanup")
	allowedCleanupStatuses             = stringSet("not_required", "completed", "failed")
	allowedTemporaryCredentialStatuses = stringSet("not_created", "revoked", "deleted", "failed")
	allowedTemporaryDataStatuses       = stringSet("not_created", "deleted", "failed")
	allowedErrorClasses                = stringSet("backend_timeout", "temporary_credential_revoke_failed", "backend_unavailable", "export_failed", "query_failed", "storage_unavailable", "queue_full", "alert_not_firing")
	allowedVersionKeys                 = stringSet("api", "collector", "grafana", "langfuse", "loki", "prometheus", "schema", "signoz", "smoke_runner", "tempo")
	allowedResidualResources           = stringSet("run-directory", "temporary-debug-data", "temporary-queue-data")
	allowedEvidenceKeysByBackend       = map[string]map[string]struct{}{
		"api":            stringSet("cleanup_attempted", "marker_seen", "response_status"),
		"collector":      stringSet("exporter_failed", "exporter_sent", "marker_received", "queue_depth"),
		"tempo":          stringSet("matched_spans"),
		"loki":           stringSet("matched_logs"),
		"prometheus":     stringSet("metric_delta", "target_up"),
		"grafana":        stringSet("datasource_healthy", "query_succeeded"),
		"langfuse_trace": stringSet("matched_traces"),
		"langfuse_score": stringSet("matched_scores"),
		"signoz":         stringSet("matched_logs", "matched_spans"),
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
	StartedAt  time.Time
	Deadline   time.Time
	FinishedAt time.Time
	Versions   map[string]string
	Checks     []BackendCheckInput
	Cleanup    SmokeCleanupInput
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

// SmokeReport is immutable after construction. The unexported state prevents callers from
// mutating a completed report between validation and persistence; accessors return copies.
type SmokeReport struct {
	runID      string
	marker     string
	profile    string
	scenario   string
	startedAt  time.Time
	finishedAt time.Time
	status     string
	versions   map[string]string
	checks     []BackendCheck
	cleanup    SmokeCleanup
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
	SchemaVersion string            `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Marker        string            `json:"marker"`
	Profile       string            `json:"profile"`
	Scenario      string            `json:"scenario"`
	StartedAt     string            `json:"started_at"`
	FinishedAt    string            `json:"finished_at"`
	Status        string            `json:"status"`
	Versions      map[string]string `json:"versions,omitempty"`
	Checks        []BackendCheck    `json:"checks"`
	Cleanup       SmokeCleanup      `json:"cleanup"`
}

func BuildSmokeReport(input SmokeReportInput) (*SmokeReport, error) {
	if err := validateSmokeReportInput(input); err != nil {
		return nil, err
	}

	checks := cloneChecks(input.Checks)
	cleanup := cloneCleanup(input.Cleanup)
	return &SmokeReport{
		runID:      input.RunID,
		marker:     input.Marker,
		profile:    input.Profile,
		scenario:   input.Scenario,
		startedAt:  input.StartedAt.UTC(),
		finishedAt: input.FinishedAt.UTC(),
		status:     aggregateSmokeStatus(checks, cleanup),
		versions:   cloneVersions(input.Versions),
		checks:     checks,
		cleanup:    cleanup,
	}, nil
}

func (r SmokeReport) MarshalJSON() ([]byte, error) {
	return json.Marshal(smokeReportJSON{
		SchemaVersion: smokeReportSchemaVersion,
		RunID:         r.runID,
		Marker:        r.marker,
		Profile:       r.profile,
		Scenario:      r.scenario,
		StartedAt:     r.startedAt.Format(time.RFC3339Nano),
		FinishedAt:    r.finishedAt.Format(time.RFC3339Nano),
		Status:        r.status,
		Versions:      r.Versions(),
		Checks:        r.Checks(),
		Cleanup:       r.Cleanup(),
	})
}

func (r SmokeReport) Checks() []BackendCheck { return cloneSafeChecks(r.checks) }

func (r SmokeReport) Cleanup() SmokeCleanup { return cloneSafeCleanup(r.cleanup) }

func (r SmokeReport) Versions() map[string]string { return cloneVersions(r.versions) }

func validateSmokeReportInput(input SmokeReportInput) error {
	if !isOpaqueSmokeIdentity(input.RunID) || !isOpaqueSmokeIdentity(input.Marker) || !contains(allowedProfiles, input.Profile) || !contains(allowedScenarios, input.Scenario) || input.StartedAt.IsZero() || input.Deadline.IsZero() || input.FinishedAt.IsZero() || input.FinishedAt.After(input.Deadline) || input.FinishedAt.Before(input.StartedAt) || len(input.Checks) == 0 {
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
	return validateCleanup(input.Cleanup)
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
	if !contains(allowedBackends, check.Backend) || !contains(allowedStatuses, check.Status) || check.Duration < 0 || !contains(allowedFailureStages, check.FailureStage) {
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
	if !contains(allowedCleanupStatuses, cleanup.Status) || !contains(allowedTemporaryCredentialStatuses, cleanup.TemporaryCredentials) || !contains(allowedTemporaryDataStatuses, cleanup.TemporaryData) {
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

func aggregateSmokeStatus(checks []BackendCheck, cleanup SmokeCleanup) string {
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
