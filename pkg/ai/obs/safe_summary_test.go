package obs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewSafeSummaryAppliesDiagnosticFields(t *testing.T) {
	summary := NewSafeSummary(
		WithSummaryHash("sha256:query-safe"),
		WithSummaryLength(42),
		WithSummaryCategory("zh-CN"),
		WithSummaryCount(3),
		WithSummaryScore(0.91),
		WithSummaryStatus("retrieval_miss"),
		WithSummaryErrorClass("upstream_failure"),
	)

	if summary.Hash != "sha256:query-safe" {
		t.Fatalf("Hash = %q, want sha256:query-safe", summary.Hash)
	}
	if summary.Length != 42 {
		t.Fatalf("Length = %d, want 42", summary.Length)
	}
	if summary.Category != "zh-CN" {
		t.Fatalf("Category = %q, want zh-CN", summary.Category)
	}
	if summary.Count != 3 {
		t.Fatalf("Count = %d, want 3", summary.Count)
	}
	if summary.Score == nil || *summary.Score != 0.91 {
		t.Fatalf("Score = %#v, want 0.91", summary.Score)
	}
	if summary.Status != "retrieval_miss" {
		t.Fatalf("Status = %q, want retrieval_miss", summary.Status)
	}
	if summary.ErrorClass != "upstream_failure" {
		t.Fatalf("ErrorClass = %q, want upstream_failure", summary.ErrorClass)
	}
}

func TestSafeSummaryDoesNotRetainRawContent(t *testing.T) {
	const rawQuery = "请查询身份证号 110101199001011234 的账户余额"

	summary := NewSafeSummary(
		WithSummaryHash("sha256:query-digest"),
		WithSummaryLength(len([]rune(rawQuery))),
		WithSummaryCategory("contains_pii"),
		WithSummaryStatus("blocked"),
	)

	payload, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("json.Marshal(SafeSummary) error = %v", err)
	}
	rawPayload := string(payload)

	for _, forbidden := range []string{
		rawQuery,
		"110101199001011234",
		"raw",
		"content",
		"query_text",
	} {
		if strings.Contains(rawPayload, forbidden) {
			t.Fatalf("SafeSummary payload leaked forbidden content marker %q: %s", forbidden, rawPayload)
		}
	}
}

func TestApplySafeSummaryOptionsDoesNotMutateBaseSummary(t *testing.T) {
	score := 0.7
	base := SafeSummary{
		Hash:       "sha256:base",
		Length:     10,
		Category:   "base",
		Count:      1,
		Score:      &score,
		Status:     "success",
		ErrorClass: "",
	}

	derived := ApplySafeSummaryOptions(
		base,
		WithSummaryHash("sha256:derived"),
		WithSummaryScore(0.95),
		WithSummaryStatus("degraded"),
	)

	if base.Hash != "sha256:base" || base.Status != "success" {
		t.Fatalf("base summary mutated: %#v", base)
	}
	if base.Score == nil || *base.Score != 0.7 {
		t.Fatalf("base Score = %#v, want unchanged 0.7", base.Score)
	}

	*derived.Score = 0.1
	if base.Score == nil || *base.Score != 0.7 {
		t.Fatalf("mutating derived score changed base Score to %#v", base.Score)
	}
}

func TestApplySafeSummaryOptionsIgnoresNilOptions(t *testing.T) {
	base := SafeSummary{Hash: "sha256:nil-option"}

	summary := ApplySafeSummaryOptions(base, nil, WithSummaryStatus("success"))

	if summary.Hash != "sha256:nil-option" {
		t.Fatalf("Hash = %q, want sha256:nil-option", summary.Hash)
	}
	if summary.Status != "success" {
		t.Fatalf("Status = %q, want success", summary.Status)
	}
}
