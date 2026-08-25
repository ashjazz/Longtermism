package observability

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	obsprivacy "github.com/ashjazz/Longtermism/internal/observability/privacy"
	obssmoke "github.com/ashjazz/Longtermism/internal/observability/smoke"
)

var (
	acceptanceChecklistItemPattern = regexp.MustCompile(`^- \[([ xX])\] (CHK([0-9]{3}))\b`)
	taskReferencePattern           = regexp.MustCompile("`task:(T[0-9]{3}[A-Z]?)`")
	testReferencePattern           = regexp.MustCompile("`test:([^`]+_test\\.go)::(Test[A-Za-z0-9_]+)`")
	fileReferencePattern           = regexp.MustCompile("`(?:asset|contract|adr):([^`]+)`")
	reportReferencePattern         = regexp.MustCompile("`report:([^`]+\\.json)`")
	liveReportRequiredItems        = map[string]struct{}{"CHK021": {}, "CHK031": {}, "CHK039": {}}
)

// TestRealBackendAcceptanceChecklistTracksAll41ItemsWithConcreteEvidence protects T166 from
// turning a requirements-quality checklist into a wall of unsupported checkmarks. Every closed
// item must point to a completed task and an executable test or version-controlled contract.
func TestRealBackendAcceptanceChecklistTracksAll41ItemsWithConcreteEvidence(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	checklist := readRealBackendAcceptanceChecklist(t, repoRoot)
	items := parseRealBackendAcceptanceItems(t, checklist)
	if len(items) != 41 {
		t.Fatalf("acceptance checklist item count = %d; want 41", len(items))
	}

	taskStatuses := readSpecTaskStatuses(t, repoRoot)
	reportValidator := newAcceptanceReportValidator(t, repoRoot)
	assertSafeRepositoryEvidencePath(t, repoRoot, "T166", "specs/003-real-observability-backends/evidence/manifest.sha256")
	for index, item := range items {
		wantID := fmt.Sprintf("CHK%03d", index+1)
		if item.id != wantID {
			t.Errorf("acceptance checklist item %d = %s; want %s", index+1, item.id, wantID)
		}
		if item.repositoryEvidence == "" {
			t.Errorf("%s has no structured repository evidence", item.id)
			continue
		}

		taskMatches := taskReferencePattern.FindAllStringSubmatch(item.repositoryEvidence, -1)
		if len(taskMatches) == 0 {
			t.Errorf("%s repository evidence has no concrete task reference", item.id)
		}
		for _, match := range taskMatches {
			status, exists := taskStatuses[match[1]]
			if !exists {
				t.Errorf("%s references unknown task %s", item.id, match[1])
				continue
			}
			if item.checked && !status {
				t.Errorf("%s is checked but references incomplete task %s", item.id, match[1])
			}
		}

		testMatches := testReferencePattern.FindAllStringSubmatch(item.repositoryEvidence, -1)
		fileMatches := fileReferencePattern.FindAllStringSubmatch(item.repositoryEvidence, -1)
		reportMatches := reportReferencePattern.FindAllStringSubmatch(item.liveEvidence, -1)
		if _, requiresLiveReport := liveReportRequiredItems[item.id]; item.checked && requiresLiveReport && len(reportMatches) == 0 {
			t.Errorf("%s is checked without a qualified version-controlled report", item.id)
		}
		if len(testMatches)+len(fileMatches)+len(reportMatches) == 0 {
			t.Errorf("%s repository evidence has no executable test or contract path", item.id)
		}
		for _, match := range testMatches {
			assertExecutableAcceptanceTestReference(t, repoRoot, item.id, match[1], match[2])
		}
		for _, match := range fileMatches {
			assertSafeRepositoryEvidencePath(t, repoRoot, item.id, match[1])
		}
		for _, match := range reportMatches {
			assertQualifiedAcceptanceReport(t, reportValidator, repoRoot, item.id, match[1])
		}
	}
}

// TestRealBackendAcceptanceChecklistKeepsUnprovenLiveRequirementsOpen records the evidence audit,
// not an aspirational target. These four items need a current live report or a missing hardening
// task; static assets, fake backends and historical v2 reports cannot close them.
func TestRealBackendAcceptanceChecklistKeepsUnprovenLiveRequirementsOpen(t *testing.T) {
	checklist := readRealBackendAcceptanceChecklist(t, observabilityRepoRoot(t))
	items := parseRealBackendAcceptanceItems(t, checklist)
	wantOpen := map[string][]string{
		"CHK021": {"scenario=privacy", "schema-v3", "passed report"},
		"CHK031": {"SigNoz", "3301", "8080"},
		"CHK039": {"scenario=alert", "firing", "resolved"},
		"CHK040": {"reset", "volume labels", "run-root"},
	}

	openCount := 0
	for _, item := range items {
		required, shouldRemainOpen := wantOpen[item.id]
		if shouldRemainOpen {
			openCount++
			if item.checked {
				t.Errorf("%s is checked without its missing live/hardening evidence", item.id)
			}
			if !strings.Contains(item.block, "**Live evidence**:") {
				t.Errorf("%s does not record its live evidence blocker", item.id)
			}
			for _, fragment := range required {
				if !strings.Contains(item.block, fragment) {
					t.Errorf("%s blocker missing %q", item.id, fragment)
				}
			}
			continue
		}
		if !item.checked {
			t.Errorf("%s unexpectedly remains open without an audited blocker", item.id)
		}
	}
	if openCount != len(wantOpen) {
		t.Errorf("audited open item count = %d; want %d", openCount, len(wantOpen))
	}

	requiredBoundary := []string{
		"当前接受的 SmokeReport schema 是 v3",
		"历史 schema v2",
		"不能关闭当前 v3 live acceptance",
		"evidence/manifest.sha256",
		"marker",
		"每个 backend check 的 `failure_stage`",
		"`cleanup.temporary_credentials`",
		"`cleanup.temporary_data`",
		"`cleanup.residual_resources`",
		"smoke 自建",
		"外部注入",
	}
	for _, fragment := range requiredBoundary {
		if !strings.Contains(checklist, fragment) {
			t.Errorf("acceptance evidence boundary missing %q", fragment)
		}
	}
	if strings.Contains(checklist, "`report:build/") {
		t.Error("ignored local build reports must not be registered as version-controlled acceptance evidence")
	}
}

type realBackendAcceptanceItem struct {
	id                 string
	checked            bool
	block              string
	repositoryEvidence string
	liveEvidence       string
}

func readRealBackendAcceptanceChecklist(t *testing.T, repoRoot string) string {
	t.Helper()
	path := filepath.Join(repoRoot, "specs", "003-real-observability-backends", "checklists", "real-backend-acceptance.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read real backend acceptance checklist: %v", err)
	}
	return string(content)
}

func parseRealBackendAcceptanceItems(t *testing.T, checklist string) []realBackendAcceptanceItem {
	t.Helper()
	items := make([]realBackendAcceptanceItem, 0, 41)
	var current *realBackendAcceptanceItem
	scanner := bufio.NewScanner(strings.NewReader(checklist))
	for scanner.Scan() {
		line := scanner.Text()
		match := acceptanceChecklistItemPattern.FindStringSubmatch(line)
		if match != nil {
			if current != nil {
				items = append(items, *current)
			}
			current = &realBackendAcceptanceItem{
				id:      match[2],
				checked: strings.EqualFold(match[1], "x"),
				block:   line,
			}
			continue
		}
		if current != nil {
			if strings.HasPrefix(line, "#") {
				items = append(items, *current)
				current = nil
				continue
			}
			current.block += "\n" + line
			if strings.HasPrefix(line, "  - **Repository evidence**:") {
				if current.repositoryEvidence != "" {
					t.Fatalf("%s has duplicate repository evidence lines", current.id)
				}
				current.repositoryEvidence = line
			}
			if strings.HasPrefix(line, "  - **Live evidence**:") {
				if current.liveEvidence != "" {
					t.Fatalf("%s has duplicate live evidence lines", current.id)
				}
				current.liveEvidence = line
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan real backend acceptance checklist: %v", err)
	}
	if current != nil {
		items = append(items, *current)
	}
	return items
}

func readSpecTaskStatuses(t *testing.T, repoRoot string) map[string]bool {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repoRoot, "specs", "003-real-observability-backends", "tasks.md"))
	if err != nil {
		t.Fatalf("read feature tasks: %v", err)
	}
	pattern := regexp.MustCompile(`(?m)^- \[([ xX])\] (T[0-9]{3}[A-Z]?)\b`)
	statuses := make(map[string]bool)
	for _, match := range pattern.FindAllStringSubmatch(string(content), -1) {
		statuses[match[2]] = strings.EqualFold(match[1], "x")
	}
	return statuses
}

func assertExecutableAcceptanceTestReference(t *testing.T, repoRoot, itemID, relativePath, testName string) {
	t.Helper()
	path := assertSafeRepositoryEvidencePath(t, repoRoot, itemID, relativePath)
	source, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s cannot read test evidence %s: %v", itemID, relativePath, err)
		return
	}
	if !sourceDeclaresGoTest(source, testName) {
		t.Errorf("%s references non-executable Go test %s::%s", itemID, relativePath, testName)
	}
}

func assertSafeRepositoryEvidencePath(t *testing.T, repoRoot, itemID, relativePath string) string {
	t.Helper()
	if filepath.IsAbs(relativePath) || filepath.Clean(relativePath) != relativePath || relativePath == "." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		t.Errorf("%s has unsafe evidence path %q", itemID, relativePath)
		return ""
	}
	if err := rejectSymlinkPathComponents(repoRoot, relativePath); err != nil {
		t.Errorf("%s evidence path is unsafe: %v", itemID, err)
		return ""
	}
	path := filepath.Join(repoRoot, relativePath)
	relativeToRoot, err := filepath.Rel(repoRoot, path)
	if err != nil || relativeToRoot != relativePath {
		t.Errorf("%s evidence path escapes repository: %q", itemID, relativePath)
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("%s references missing evidence path %q: %v", itemID, relativePath, err)
		return path
	}
	if !info.Mode().IsRegular() {
		t.Errorf("%s evidence path %q is not a regular file", itemID, relativePath)
	}
	return path
}

func rejectSymlinkPathComponents(repoRoot, relativePath string) error {
	current := repoRoot
	for _, part := range strings.Split(filepath.Clean(relativePath), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("path component is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path must not traverse symbolic links")
		}
	}
	return nil
}

// TestAcceptanceReportQualificationRejectsNonEvidence keeps the future live-evidence path from
// becoming vacuous while the current checklist intentionally contains no qualifying report. A
// schema-valid document is only the first gate: a closing artifact must also prove every backend
// check passed. Privacy can prove its closed eight surfaces; alert remains fail-closed until the
// wire format identifies each distinct alert class instead of exposing aggregate counts only.
func TestAcceptanceReportQualificationRejectsNonEvidence(t *testing.T) {
	repoRoot := observabilityRepoRoot(t)
	validator := newAcceptanceReportValidator(t, repoRoot)

	if err := qualifyAcceptanceReportJSON(validator, "CHK021", marshalAcceptanceFixture(t, validAcceptanceReportFixture("privacy"))); err != nil {
		t.Fatalf("complete privacy report rejected: %v", err)
	}
	if err := qualifyAcceptanceReportJSON(validator, "CHK031", marshalAcceptanceFixture(t, validSignozAcceptanceReportFixture())); err != nil {
		t.Fatalf("complete SigNoz report rejected: %v", err)
	}
	for index, backend := range []string{"signoz_traces", "signoz_logs", "signoz_metrics"} {
		t.Run("SigNoz zero evidence "+backend, func(t *testing.T) {
			document := validSignozAcceptanceReportFixture()
			check := document["checks"].([]map[string]any)[index]
			for key := range check["evidence"].(map[string]any) {
				check["evidence"].(map[string]any)[key] = 0
			}
			if err := qualifyAcceptanceReportJSON(validator, "CHK031", marshalAcceptanceFixture(t, document)); err == nil {
				t.Fatal("zero SigNoz query evidence was accepted")
			}
		})
	}

	tests := []struct {
		name     string
		itemID   string
		scenario string
		mutate   func(map[string]any)
	}{
		{name: "missing marker", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) { delete(document, "marker") }},
		{name: "old schema", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) { document["schema_version"] = "2" }},
		{name: "secret-like identity", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) { document["marker"] = "sk-syntheticvalue-t166" }},
		{name: "aggregate alert evidence lacks class identity", itemID: "CHK039", scenario: "alert", mutate: func(map[string]any) {}},
		{name: "failed overall status", itemID: "CHK021", scenario: "privacy", mutate: makeAcceptanceReportFailed},
		{name: "skipped backend check", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) {
			document["checks"] = append(document["checks"].([]map[string]any), map[string]any{"backend": "api", "status": "skipped", "duration_ms": 0, "failure_stage": "none"})
		}},
		{name: "missing failure stage", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) { delete(document["checks"].([]map[string]any)[0], "failure_stage") }},
		{name: "cleanup failed", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) {
			cleanup := document["cleanup"].(map[string]any)
			cleanup["status"] = "failed"
			cleanup["residual_resources"] = []string{"alert-condition-active"}
		}},
		{name: "residual resource", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) {
			document["cleanup"].(map[string]any)["residual_resources"] = []string{"alert-condition-active"}
		}},
		{name: "temporary credential cleanup failed", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) {
			document["cleanup"].(map[string]any)["temporary_credentials"] = "failed"
		}},
		{name: "temporary data cleanup failed", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) { document["cleanup"].(map[string]any)["temporary_data"] = "failed" }},
		{name: "privacy local profile", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) { document["profile"] = "local" }},
		{name: "privacy without privacy backend check", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) {
			document["checks"] = []map[string]any{{"backend": "api", "status": "passed", "duration_ms": 1, "failure_stage": "none", "evidence": map[string]any{"marker_seen": true}}}
		}},
		{name: "privacy surface failed", itemID: "CHK021", scenario: "privacy", mutate: func(document map[string]any) { document["privacy_evidence"].([]map[string]any)[0]["status"] = "failed" }},
		{name: "SigNoz requirement cannot use Grafana report", itemID: "CHK031", scenario: "infra", mutate: func(map[string]any) {}},
		{name: "zero aggregate alert evidence also lacks class identity", itemID: "CHK039", scenario: "alert", mutate: func(document map[string]any) {
			document["checks"].([]map[string]any)[0]["evidence"].(map[string]any)["alerts_firing"] = 0
			document["checks"].([]map[string]any)[0]["evidence"].(map[string]any)["alerts_resolved"] = 0
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validAcceptanceReportFixture(test.scenario)
			test.mutate(document)
			if err := qualifyAcceptanceReportJSON(validator, test.itemID, marshalAcceptanceFixture(t, document)); err == nil {
				t.Fatal("non-qualifying acceptance report was accepted")
			}
		})
	}
}

func newAcceptanceReportValidator(t *testing.T, repoRoot string) *obssmoke.SmokeReportSchemaValidator {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join(repoRoot, "specs", "003-real-observability-backends", "contracts", "smoke-report.schema.json"))
	if err != nil {
		t.Fatalf("read smoke report schema: %v", err)
	}
	validator, err := obssmoke.NewSmokeReportSchemaValidator(schema)
	if err != nil {
		t.Fatal("compile version-controlled smoke report schema")
	}
	return validator
}

func assertQualifiedAcceptanceReport(t *testing.T, validator *obssmoke.SmokeReportSchemaValidator, repoRoot, itemID, relativePath string) {
	t.Helper()
	path, err := resolveAcceptanceReportPath(repoRoot, relativePath)
	if err != nil {
		t.Errorf("%s has invalid report evidence path: %v", itemID, err)
		return
	}
	document, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s cannot read report evidence: %v", itemID, err)
		return
	}
	if err := verifyAcceptanceReportManifest(repoRoot, relativePath, document); err != nil {
		t.Errorf("%s report is not registered in the reviewed evidence manifest: %v", itemID, err)
		return
	}
	if err := qualifyAcceptanceReportJSON(validator, itemID, document); err != nil {
		t.Errorf("%s report evidence is not qualified: %v", itemID, err)
	}
}

func resolveAcceptanceReportPath(repoRoot, relativePath string) (string, error) {
	const evidenceRoot = "specs/003-real-observability-backends/evidence/"
	cleanSlashPath := filepath.ToSlash(relativePath)
	if filepath.IsAbs(relativePath) || filepath.Clean(relativePath) != relativePath || !strings.HasPrefix(cleanSlashPath, evidenceRoot) || !strings.HasSuffix(cleanSlashPath, ".json") {
		return "", fmt.Errorf("report must be a clean repository-relative path below %s", evidenceRoot)
	}
	path := filepath.Join(repoRoot, relativePath)
	if err := rejectSymlinkPathComponents(repoRoot, relativePath); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("report must be an existing regular file")
	}
	return path, nil
}

func verifyAcceptanceReportManifest(repoRoot, relativePath string, document []byte) error {
	manifestPath := filepath.Join(repoRoot, "specs", "003-real-observability-backends", "evidence", "manifest.sha256")
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("evidence manifest is unavailable")
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("evidence manifest is unreadable")
	}
	return acceptanceReportManifestContains(manifest, relativePath, document)
}

func acceptanceReportManifestContains(manifest []byte, relativePath string, document []byte) error {
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(document))
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return fmt.Errorf("evidence manifest contains an invalid entry")
		}
		if fields[1] == relativePath && strings.EqualFold(fields[0], wantDigest) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("evidence manifest cannot be scanned")
	}
	return fmt.Errorf("report path and digest are not registered")
}

type acceptanceReportSummary struct {
	Marker   string `json:"marker"`
	Profile  string `json:"profile"`
	Scenario string `json:"scenario"`
	Status   string `json:"status"`
	Checks   []struct {
		Backend      string         `json:"backend"`
		Status       string         `json:"status"`
		FailureStage string         `json:"failure_stage"`
		Evidence     map[string]any `json:"evidence"`
	} `json:"checks"`
	PrivacyEvidence []struct {
		Attempted bool   `json:"attempted"`
		Status    string `json:"status"`
	} `json:"privacy_evidence"`
	Cleanup struct {
		Status               string   `json:"status"`
		ResidualResources    []string `json:"residual_resources"`
		TemporaryCredentials string   `json:"temporary_credentials"`
		TemporaryData        string   `json:"temporary_data"`
	} `json:"cleanup"`
}

func qualifyAcceptanceReportJSON(validator *obssmoke.SmokeReportSchemaValidator, itemID string, document []byte) error {
	if err := validator.ValidateJSON(document); err != nil {
		return fmt.Errorf("schema validation failed")
	}
	scanner, err := obsprivacy.NewScanner([]string{"t166-report-canary"})
	if err != nil {
		return fmt.Errorf("privacy scanner initialization failed")
	}
	scanResult, err := scanner.Scan([]obsprivacy.SurfaceText{{Surface: obsprivacy.SurfaceReport, Text: string(document)}})
	if err != nil || len(scanResult.Counts) != 0 {
		return fmt.Errorf("report contains sensitive-pattern evidence")
	}
	var report acceptanceReportSummary
	if err := json.Unmarshal(document, &report); err != nil {
		return fmt.Errorf("report summary decode failed")
	}
	if report.Marker == "" || report.Status != "passed" || len(report.Checks) == 0 {
		return fmt.Errorf("report does not carry a passed marker-bound check set")
	}
	for _, check := range report.Checks {
		if check.Status != "passed" || check.FailureStage != "none" {
			return fmt.Errorf("report contains a non-passing backend check")
		}
	}
	if report.Cleanup.Status != "completed" && report.Cleanup.Status != "not_required" {
		return fmt.Errorf("cleanup is not successful")
	}
	if len(report.Cleanup.ResidualResources) != 0 || !stringInSet(report.Cleanup.TemporaryCredentials, "not_created", "revoked", "deleted") || !stringInSet(report.Cleanup.TemporaryData, "not_created", "deleted") {
		return fmt.Errorf("cleanup retains resources or failed temporary asset cleanup")
	}

	switch itemID {
	case "CHK031":
		if report.Profile != "signoz" || report.Scenario != "infra" || !reportHasPositiveBackendEvidence(report, "signoz_traces", "matched_spans") || !reportHasPositiveBackendEvidence(report, "signoz_logs", "matched_logs") || !reportHasPositiveBackendEvidence(report, "signoz_metrics", "metric_delta") {
			return fmt.Errorf("SigNoz acceptance requires real three-signal query evidence")
		}
	case "CHK021":
		if report.Profile != "grafana" || report.Scenario != "privacy" || len(report.PrivacyEvidence) != 8 || !reportHasPassedBackend(report, "privacy") {
			return fmt.Errorf("privacy acceptance requires the eight-surface privacy scenario")
		}
		for _, evidence := range report.PrivacyEvidence {
			if !evidence.Attempted || evidence.Status != "passed" {
				return fmt.Errorf("privacy acceptance contains an unverified surface")
			}
		}
	case "CHK039":
		// v3 只汇总 firing/resolved 数量，不能证明 HTTP、exporter、queue 与 storage
		// 四个 distinct class 都经历了完整生命周期。升级为逐类闭集证据前必须 fail closed。
		return fmt.Errorf("alert acceptance requires per-class firing and resolved evidence")
	}
	return nil
}

func reportHasPassedBackend(report acceptanceReportSummary, backend string) bool {
	for _, check := range report.Checks {
		if check.Backend == backend && check.Status == "passed" && check.FailureStage == "none" {
			return true
		}
	}
	return false
}

func reportHasPositiveBackendEvidence(report acceptanceReportSummary, backend, evidenceKey string) bool {
	for _, check := range report.Checks {
		if check.Backend != backend || check.Status != "passed" || check.FailureStage != "none" {
			continue
		}
		value, ok := check.Evidence[evidenceKey].(float64)
		return ok && value >= 1
	}
	return false
}

func stringInSet(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validAcceptanceReportFixture(scenario string) map[string]any {
	check := map[string]any{
		"backend":       "api",
		"status":        "passed",
		"duration_ms":   1,
		"failure_stage": "none",
		"evidence":      map[string]any{"marker_seen": true},
	}
	if scenario == "alert" {
		check = map[string]any{
			"backend":       "grafana",
			"status":        "passed",
			"duration_ms":   1,
			"failure_stage": "none",
			"evidence":      map[string]any{"alerts_firing": 4, "alerts_resolved": 4},
		}
	}
	if scenario == "privacy" {
		check = map[string]any{
			"backend":       "privacy",
			"status":        "passed",
			"duration_ms":   1,
			"failure_stage": "none",
			"evidence":      map[string]any{"forbidden_marker_hits": 0, "scanned_surface_count": 8, "finding_count": 0},
		}
	}
	document := map[string]any{
		"schema_version": "3",
		"run_id":         "run-t166-0001",
		"marker":         "mark-t166-0001",
		"profile":        "grafana",
		"scenario":       scenario,
		"started_at":     "2026-08-21T01:02:03Z",
		"finished_at":    "2026-08-21T01:02:04Z",
		"status":         "passed",
		"checks":         []map[string]any{check},
		"cleanup": map[string]any{
			"status":                "completed",
			"residual_resources":    []string{},
			"temporary_credentials": "revoked",
			"temporary_data":        "deleted",
		},
	}
	if scenario == "privacy" {
		document["privacy_evidence"] = validAcceptancePrivacyEvidence()
	}
	return document
}

func validSignozAcceptanceReportFixture() map[string]any {
	document := validAcceptanceReportFixture("infra")
	document["profile"] = "signoz"
	document["checks"] = []map[string]any{
		{"backend": "signoz_traces", "status": "passed", "duration_ms": 1, "failure_stage": "none", "evidence": map[string]any{"matched_spans": 1}},
		{"backend": "signoz_logs", "status": "passed", "duration_ms": 1, "failure_stage": "none", "evidence": map[string]any{"matched_logs": 1}},
		{"backend": "signoz_metrics", "status": "passed", "duration_ms": 1, "failure_stage": "none", "evidence": map[string]any{"metric_delta": 1}},
	}
	return document
}

func validAcceptancePrivacyEvidence() []map[string]any {
	counts := func() map[string]any {
		return map[string]any{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
	}
	return []map[string]any{
		{"surface": "api", "evidence_method": "bounded_memory_scan", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts()},
		{"surface": "application_log", "evidence_method": "projection_and_exact_query", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts()},
		{"surface": "collector_queue", "evidence_method": "configuration_and_telemetry", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts(), "runtime_config_digest_verified": true, "prequeue_artifact_hash_verified": true, "component_identity_verified": true, "export_admission_correlated": true},
		{"surface": "tempo", "evidence_method": "bounded_trace_document", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts()},
		{"surface": "loki", "evidence_method": "exact_structured_query", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts()},
		{"surface": "langfuse_trace", "evidence_method": "bounded_platform_document", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts()},
		{"surface": "langfuse_score", "evidence_method": "bounded_platform_document", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts()},
		{"surface": "report", "evidence_method": "contained_artifact_scan", "attempted": true, "status": "passed", "scanner_policy_version": "1", "counts": counts()},
	}
}

func makeAcceptanceReportFailed(document map[string]any) {
	document["status"] = "failed"
	check := document["checks"].([]map[string]any)[0]
	check["status"] = "failed"
	check["failure_stage"] = "query"
	check["error_class"] = "query_failed"
}

func marshalAcceptanceFixture(t *testing.T, document map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal("encode acceptance report fixture")
	}
	return encoded
}

func TestAcceptanceReportReferencePathPolicy(t *testing.T) {
	for _, invalid := range []string{
		"build/observability/smoke-reports/report.json",
		"specs/003-real-observability-backends/evidence/../report.json",
		"/tmp/report.json",
		"specs/003-real-observability-backends/evidence/report.md",
	} {
		t.Run(invalid, func(t *testing.T) {
			if _, err := resolveAcceptanceReportPath("/repository", invalid); err == nil {
				t.Fatal("unsafe or non-evidence report path was accepted")
			}
		})
	}
}

func TestAcceptanceReportManifestRequiresReviewedPathAndDigest(t *testing.T) {
	document := []byte(`{"schema_version":"3"}`)
	relativePath := "specs/003-real-observability-backends/evidence/privacy.json"
	digest := fmt.Sprintf("%x", sha256.Sum256(document))
	if err := acceptanceReportManifestContains([]byte(digest+"  "+relativePath+"\n"), relativePath, document); err != nil {
		t.Fatalf("reviewed manifest entry rejected: %v", err)
	}

	for _, manifest := range []string{
		"",
		digest + "  specs/003-real-observability-backends/evidence/other.json\n",
		strings.Repeat("0", sha256.Size*2) + "  " + relativePath + "\n",
		"malformed-entry\n",
	} {
		if err := acceptanceReportManifestContains([]byte(manifest), relativePath, document); err == nil {
			t.Fatal("unreviewed or digest-mismatched report was accepted")
		}
	}
}

func TestAcceptanceChecklistEvidenceDoesNotBleedAcrossHeadings(t *testing.T) {
	checklist := strings.Join([]string{
		"- [X] CHK001 synthetic item",
		"  - **Repository evidence**: no structured reference",
		"## Unrelated appendix",
		"`task:T164`; `asset:specs/003-real-observability-backends/spec.md`; `report:specs/003-real-observability-backends/evidence/unrelated.json`",
	}, "\n")
	items := parseRealBackendAcceptanceItems(t, checklist)
	if len(items) != 1 {
		t.Fatalf("parsed items = %d; want 1", len(items))
	}
	if taskReferencePattern.MatchString(items[0].repositoryEvidence) || fileReferencePattern.MatchString(items[0].repositoryEvidence) {
		t.Fatal("global appendix evidence bled into the final checklist item")
	}
	if reportReferencePattern.MatchString(items[0].liveEvidence) {
		t.Fatal("global appendix report bled into the final checklist item")
	}
}

func TestAcceptanceChecklistParsesReportOnlyFromLiveEvidenceLine(t *testing.T) {
	report := "specs/003-real-observability-backends/evidence/privacy.json"
	checklist := strings.Join([]string{
		"- [X] CHK021 synthetic live item",
		"  - **Repository evidence**: `task:T198`; `asset:specs/003-real-observability-backends/spec.md`",
		"  - unrelated `report:specs/003-real-observability-backends/evidence/ignored.json`",
		"  - **Live evidence**: `report:" + report + "`",
	}, "\n")
	items := parseRealBackendAcceptanceItems(t, checklist)
	if len(items) != 1 {
		t.Fatalf("parsed items = %d; want 1", len(items))
	}
	matches := reportReferencePattern.FindAllStringSubmatch(items[0].liveEvidence, -1)
	if len(matches) != 1 || matches[0][1] != report {
		t.Fatalf("live report matches = %v; want %s", matches, report)
	}
}

func TestRepositoryEvidencePathRejectsFinalAndIntermediateSymlinks(t *testing.T) {
	repoRoot := t.TempDir()
	regularDir := filepath.Join(repoRoot, "specs")
	if err := os.MkdirAll(regularDir, 0o700); err != nil {
		t.Fatal("create regular evidence directory")
	}
	regularPath := filepath.Join(regularDir, "contract.md")
	if err := os.WriteFile(regularPath, []byte("contract"), 0o600); err != nil {
		t.Fatal("create regular evidence file")
	}
	if err := rejectSymlinkPathComponents(repoRoot, "specs/contract.md"); err != nil {
		t.Fatalf("regular evidence path rejected: %v", err)
	}

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal("create external fixture")
	}
	if err := os.Symlink(outsideFile, filepath.Join(repoRoot, "final-link.md")); err != nil {
		t.Skip("symbolic links are unavailable on this platform")
	}
	if err := rejectSymlinkPathComponents(repoRoot, "final-link.md"); err == nil {
		t.Fatal("final evidence symlink was accepted")
	}
	if err := os.Symlink(outside, filepath.Join(repoRoot, "directory-link")); err != nil {
		t.Fatal("create intermediate symlink fixture")
	}
	if err := rejectSymlinkPathComponents(repoRoot, "directory-link/outside.md"); err == nil {
		t.Fatal("intermediate evidence symlink was accepted")
	}
}

// strconv is used here to keep a compile-time assertion that checklist numeric IDs stay decimal;
// it prevents a future regexp edit from silently accepting malformed CHK identifiers.
func TestAcceptanceChecklistIdentifierPatternIsDecimalAndBounded(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: "- [X] CHK001 valid", want: true},
		{line: "- [ ] CHK041 valid", want: true},
		{line: "- [X] CHK42 invalid", want: false},
		{line: "  - [X] CHK001 nested", want: false},
	}
	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			match := acceptanceChecklistItemPattern.FindStringSubmatch(test.line)
			if (match != nil) != test.want {
				t.Fatalf("match = %v; want %v", match != nil, test.want)
			}
			if match == nil {
				return
			}
			value, err := strconv.Atoi(match[3])
			if err != nil || value < 1 || value > 41 {
				t.Fatalf("identifier numeric value = %d, %v; want 1..41", value, err)
			}
		})
	}
}
