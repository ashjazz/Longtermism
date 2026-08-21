package observability

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	acceptanceChecklistItemPattern = regexp.MustCompile(`^- \[([ xX])\] (CHK([0-9]{3}))\b`)
	taskReferencePattern           = regexp.MustCompile("`task:(T[0-9]{3}[A-Z]?)`")
	testReferencePattern           = regexp.MustCompile("`test:([^`]+_test\\.go)::(Test[A-Za-z0-9_]+)`")
	fileReferencePattern           = regexp.MustCompile("`(?:asset|contract|adr):([^`]+)`")
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
	for index, item := range items {
		wantID := fmt.Sprintf("CHK%03d", index+1)
		if item.id != wantID {
			t.Errorf("acceptance checklist item %d = %s; want %s", index+1, item.id, wantID)
		}
		if !strings.Contains(item.block, "**Repository evidence**:") {
			t.Errorf("%s has no structured repository evidence", item.id)
			continue
		}

		taskMatches := taskReferencePattern.FindAllStringSubmatch(item.block, -1)
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

		testMatches := testReferencePattern.FindAllStringSubmatch(item.block, -1)
		fileMatches := fileReferencePattern.FindAllStringSubmatch(item.block, -1)
		if len(testMatches)+len(fileMatches) == 0 {
			t.Errorf("%s repository evidence has no executable test or contract path", item.id)
		}
		for _, match := range testMatches {
			assertExecutableAcceptanceTestReference(t, repoRoot, item.id, match[1], match[2])
		}
		for _, match := range fileMatches {
			assertSafeRepositoryEvidencePath(t, repoRoot, item.id, match[1])
		}
	}
}

// TestRealBackendAcceptanceChecklistKeepsUnprovenLiveRequirementsOpen records the evidence audit,
// not an aspirational target. These five items need a current live report or a missing hardening
// task; static assets, fake backends and historical v2 reports cannot close them.
func TestRealBackendAcceptanceChecklistKeepsUnprovenLiveRequirementsOpen(t *testing.T) {
	checklist := readRealBackendAcceptanceChecklist(t, observabilityRepoRoot(t))
	items := parseRealBackendAcceptanceItems(t, checklist)
	wantOpen := map[string][]string{
		"CHK019": {"pending:T167", "release gate"},
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
	id      string
	checked bool
	block   string
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
			current.block += "\n" + line
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
