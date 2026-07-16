// Package privacy 提供 smoke 与平台适配器共用的低敏泄漏扫描边界。
package privacy

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

const (
	maximumCanaries         = 16
	maximumCanaryBytes      = 128
	maximumSurfaceTexts     = 16
	maximumSurfaceTextBytes = 1 << 20

	categoryAPIKey          = "api_key"
	categoryAuthorization   = "authorization"
	categoryPII             = "pii"
	categorySyntheticCanary = "synthetic_canary"
	categoryToken           = "token"
)

var (
	errNoSyntheticCanary      = errors.New("synthetic canary is required")
	errInvalidSyntheticCanary = errors.New("synthetic canary is invalid")
	errScannerNotInitialized  = errors.New("privacy scanner is not initialized")
	errScanInputTooLarge      = errors.New("privacy scan input exceeds limits")

	redactedValuePattern   = regexp.MustCompile(`(?i)\[redacted\]|<redacted>|\*{3,}`)
	syntheticCanaryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,127}$`)
	apiKeyPattern          = regexp.MustCompile(`(?i)\b(?:x-)?api[_ -]?key\s*[:=]\s*[^\s,;]+|\bsk-[A-Za-z0-9_-]+`)
	authorizationPattern   = regexp.MustCompile(`(?i)\bauthorization\s*:\s*(?:bearer|basic|token)\s+[^\s,;]+`)
	tokenPattern           = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[^\s,;]+|\btoken\s*[:=]\s*[A-Za-z0-9._~+/-]+|\beyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	piiPatterns            = []*regexp.Regexp{
		regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
		regexp.MustCompile(`\b1[3-9][0-9]{9}\b`),
		regexp.MustCompile(`\b[0-9]{17}[0-9Xx]\b`),
	}
)

// Surface 是可能持久化或外发观测内容的边界。它不会进入结果，避免扫描报告反过来
// 暴露泄漏位置或后端拓扑。
type Surface string

const (
	SurfaceAPI     Surface = "api"
	SurfaceLog     Surface = "log"
	SurfaceQueue   Surface = "queue"
	SurfaceBackend Surface = "backend"
	SurfaceReport  Surface = "report"
)

// SurfaceText 是待扫描的单个输出面快照；Scan 不会修改它。
type SurfaceText struct {
	Surface Surface
	Text    string
}

// ScanResult 只输出类别与命中输出面的数量，不能携带原文、字段名或 surface。
type ScanResult struct {
	Counts map[string]int `json:"counts"`
}

// scanner 是不可变扫描器。它不导出，强制调用方只能通过 NewScanner 取得完成
// 校验的实例，防止零值 scanner 悄然把泄漏误报为“零命中”。
type scanner struct {
	canaries    []string
	initialized bool
}

// NewScanner 创建统一扫描器并复制 canary，防止调用方后续修改 slice 改写扫描策略。
func NewScanner(canaries []string) (scanner, error) {
	if len(canaries) > maximumCanaries {
		return scanner{}, errInvalidSyntheticCanary
	}
	unique := make(map[string]struct{}, len(canaries))
	for _, canary := range canaries {
		normalized := strings.TrimSpace(canary)
		if normalized == "" {
			continue
		}
		if len(normalized) > maximumCanaryBytes || !syntheticCanaryPattern.MatchString(normalized) {
			return scanner{}, errInvalidSyntheticCanary
		}
		unique[normalized] = struct{}{}
	}
	if len(unique) == 0 {
		return scanner{}, errNoSyntheticCanary
	}

	values := make([]string, 0, len(unique))
	for canary := range unique {
		values = append(values, canary)
	}
	sort.Strings(values)
	return scanner{canaries: values, initialized: true}, nil
}

// Scan 在每个输出面至多为每个类别计一次。这样报告能回答“哪个类别仍有泄漏”，却
// 不会因重复内容放大计数，也不会把原文、来源或 payload mode 写进结果。
func (scanner scanner) Scan(surfaces []SurfaceText) (ScanResult, error) {
	if !scanner.initialized {
		return ScanResult{}, errScannerNotInitialized
	}
	if !withinScanLimits(surfaces) {
		return ScanResult{}, errScanInputTooLarge
	}
	counts := make(map[string]int)
	for _, surface := range surfaces {
		for category := range scanner.categoriesFor(surface.Text) {
			counts[category]++
		}
	}
	return ScanResult{Counts: counts}, nil
}

func withinScanLimits(surfaces []SurfaceText) bool {
	if len(surfaces) > maximumSurfaceTexts {
		return false
	}
	for _, surface := range surfaces {
		if len(surface.Text) > maximumSurfaceTextBytes {
			return false
		}
	}
	return true
}

func (scanner scanner) categoriesFor(text string) map[string]struct{} {
	// 已明确去敏的占位值不是泄漏；先擦除它也避免 authorization/token 规则将
	// "[REDACTED]" 误判成凭据，导致安全 smoke 假阳性。
	masked := redactedValuePattern.ReplaceAllString(text, "")
	categories := make(map[string]struct{})
	if apiKeyPattern.MatchString(masked) {
		categories[categoryAPIKey] = struct{}{}
	}
	hasAuthorization := authorizationPattern.MatchString(masked)
	if hasAuthorization {
		categories[categoryAuthorization] = struct{}{}
	}
	if !hasAuthorization && tokenPattern.MatchString(masked) {
		categories[categoryToken] = struct{}{}
	}
	if hasPII(masked) {
		categories[categoryPII] = struct{}{}
	}
	for _, canary := range scanner.canaries {
		if strings.Contains(masked, canary) {
			categories[categorySyntheticCanary] = struct{}{}
			break
		}
	}
	return categories
}

func hasPII(text string) bool {
	for _, pattern := range piiPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}
