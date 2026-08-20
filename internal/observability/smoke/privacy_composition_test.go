package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	t192Canary = "T192_SYNTHETIC_CANARY"
	t192Raw    = "t192-raw-platform-body"
	t192Secret = "Authorization: Bearer t192-secret"
)

// TestPrivacyCompositionCarriesTheFixtureIdentityAndWindowVerbatim locks the one-way
// composition boundary. The protected fixture creates the only trustworthy identity/window;
// the runner may use a clock for finished_at, but never to reconstruct query targets.
func TestPrivacyCompositionCarriesTheFixtureIdentityAndWindowVerbatim(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond).Add(321 * time.Nanosecond)
	deadline := startedAt.Add(47*time.Second + 23*time.Nanosecond)
	fixture := t192Fixture(startedAt, deadline)
	events := make([]string, 0, 10)
	surfaces := newT192Surfaces()
	surfaces.events = &events
	clock := &t192CompositionClock{times: []time.Time{startedAt.Add(-time.Hour), deadline.Add(-time.Nanosecond)}, events: &events}
	fixtureRunner := &t192FixtureRunner{result: fixture, events: &events}

	report, err := runPrivacyCompositionForTest(context.Background(), privacyCompositionRequest{
		Profile: "grafana", ForbiddenCanary: t192Canary, RunID: fixture.RunID, Marker: fixture.Marker,
		StartedAt: startedAt, Deadline: deadline, SurfaceTimeout: 250 * time.Millisecond,
	}, privacyCompositionDependencies{Fixture: fixtureRunner, Surfaces: surfaces, Clock: clock})
	if err != nil {
		t.Fatalf("runPrivacyCompositionForTest() error class = %q", t192Class(err))
	}
	if report == nil || report.Status() != "passed" {
		t.Fatal("eight sealed zero-count proofs did not produce a passed report")
	}
	wantOrder := []PrivacySmokeSurface{
		PrivacySmokeSurfaceAPI, PrivacySmokeSurfaceApplicationLog, PrivacySmokeSurfaceCollectorQueue,
		PrivacySmokeSurfaceReport, PrivacySmokeSurfaceTempo, PrivacySmokeSurfaceLoki,
		PrivacySmokeSurfaceLangfuseTrace, PrivacySmokeSurfaceLangfuseScore,
	}
	if !reflect.DeepEqual(surfaces.order, wantOrder) {
		t.Fatalf("surface execution order = %v, want all local artifacts before remote transports", surfaces.order)
	}
	for _, target := range surfaces.targets {
		if target.RunID != fixture.RunID || target.Marker != fixture.Marker || target.ForbiddenCanary != t192Canary ||
			target.RequestID != fixture.RequestID || target.AITraceID != fixture.AITraceID ||
			target.ServiceTraceID != fixture.ServiceTraceID || target.SpanID != fixture.SpanID ||
			target.ManifestRef != fixture.ManifestRef || !target.StartedAt.Equal(fixture.StartedAt) ||
			!target.Deadline.Equal(fixture.Deadline) || target.Limit != 100 {
			t.Fatalf("surface %q did not receive the fixture result verbatim", target.Surface)
		}
	}
	assertT192SurfaceBudgets(t, surfaces)
	assertT192PrivacyReport(t, report, "passed")
	if clock.calls != 1 {
		t.Fatalf("clock calls = %d, want only finished_at observation after scans", clock.calls)
	}
	if fixtureRunner.calls != 1 || fixtureRunner.request.RunID != fixture.RunID || fixtureRunner.request.Marker != fixture.Marker ||
		fixtureRunner.request.ForbiddenCanary != t192Canary || !fixtureRunner.request.StartedAt.Equal(startedAt) ||
		!fixtureRunner.request.Deadline.Equal(deadline) {
		t.Fatal("composition did not invoke the protected fixture exactly once with the original request window")
	}
	if strings.Join(events, ",") != "fixture,api,application_log,collector_queue,report,tempo,loki,langfuse_trace,langfuse_score,clock" {
		t.Fatalf("composition events = %v, want fixture then local reads then remote queries then report construction", events)
	}
}

// Contexts must be created immediately before each call. Pre-allocating all eight short
// deadlines looks bounded in fast unit tests, but makes later production queries inherit time
// already consumed by earlier surfaces.
func TestPrivacyCompositionCreatesEachSurfaceBudgetJustInTime(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	surfaces := newT192Surfaces()
	surfaces.delays[PrivacySmokeSurfaceAPI] = 80 * time.Millisecond
	request := t192Request(fixture)
	request.SurfaceTimeout = 300 * time.Millisecond
	report, err := runPrivacyCompositionForTest(context.Background(), request, privacyCompositionDependencies{
		Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
		Clock: &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}},
	})
	if err != nil || report == nil {
		t.Fatal("composition failed while checking fresh surface budgets")
	}
	if len(surfaces.remaining) != 8 {
		t.Fatal("composition omitted a surface budget")
	}
	for index, remaining := range surfaces.remaining {
		if remaining <= 0 || remaining > 310*time.Millisecond {
			t.Fatalf("surface %d entered with budget %v; contexts were likely created eagerly", index, remaining)
		}
		if surfaces.contexts[index].Err() == nil {
			t.Fatalf("surface %d child context was not canceled after its call", index)
		}
	}
	if separation := surfaces.deadlines[1].Sub(surfaces.deadlines[0]); separation < 60*time.Millisecond {
		t.Fatalf("second surface deadline advanced by only %v; all contexts were likely created eagerly", separation)
	}
	if surfaces.uncanceledPrior {
		t.Fatal("a previous surface context remained live when the next query began")
	}
}

func TestPrivacyCompositionValidatesTheWholeGraphBeforeTriggering(t *testing.T) {
	startedAt := time.Now().UTC()
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	valid := func() privacyCompositionDependencies {
		return privacyCompositionDependencies{Fixture: &t192FixtureRunner{result: fixture}, Surfaces: newT192Surfaces(), Clock: &t192CompositionClock{}}
	}
	tests := []func(*privacyCompositionDependencies){
		func(deps *privacyCompositionDependencies) { deps.Fixture = nil },
		func(deps *privacyCompositionDependencies) { deps.Surfaces = nil },
		func(deps *privacyCompositionDependencies) { deps.Clock = nil },
	}
	for _, mutate := range tests {
		deps := valid()
		runner, _ := deps.Fixture.(*t192FixtureRunner)
		mutate(&deps)
		report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), deps)
		if report != nil || err == nil {
			t.Fatal("incomplete production graph was accepted")
		}
		if runner != nil && runner.calls != 0 {
			t.Fatal("preflight failure triggered the fixture")
		}
		assertT192LowSensitive(t, err)
	}
}

func TestPrivacyCompositionRejectsAChangedFixtureResultBeforeSurfaceReads(t *testing.T) {
	startedAt := time.Now().UTC()
	want := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	tests := []struct {
		name   string
		mutate func(*PrivacyFixtureResult)
	}{
		{"run", func(result *PrivacyFixtureResult) { result.RunID = "foreign-run-t192" }},
		{"marker", func(result *PrivacyFixtureResult) { result.Marker = "foreign-marker-t192" }},
		{"start", func(result *PrivacyFixtureResult) { result.StartedAt = result.StartedAt.Add(time.Nanosecond) }},
		{"deadline", func(result *PrivacyFixtureResult) { result.Deadline = result.Deadline.Add(time.Nanosecond) }},
		{"unsafe manifest", func(result *PrivacyFixtureResult) { result.ManifestRef = "../foreign-manifest.json" }},
		{"request not sent", func(result *PrivacyFixtureResult) { result.RequestSent = false }},
		{"chat not successful", func(result *PrivacyFixtureResult) { result.ChatSucceeded = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := want
			tt.mutate(&result)
			surfaces := newT192Surfaces()
			report, err := runPrivacyCompositionForTest(context.Background(), t192Request(want), privacyCompositionDependencies{
				Fixture: &t192FixtureRunner{result: result}, Surfaces: surfaces, Clock: &t192CompositionClock{},
			})
			if report != nil || err == nil || len(surfaces.order) != 0 {
				t.Fatal("changed fixture result reached surface reads")
			}
			assertT192LowSensitive(t, err)
		})
	}
}

func TestPrivacyCompositionRejectsZeroWithoutASealedSurfaceProof(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	for _, surface := range t192SchemaSurfaceOrder() {
		t.Run(string(surface), func(t *testing.T) {
			surfaces := newT192Surfaces()
			surfaces.results[surface] = privacyCompositionSurfaceEvidence{}
			report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
				Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
				Clock: &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}},
			})
			if report != nil || err == nil {
				t.Fatal("default-zero evidence impersonated an attempted scan")
			}
			if surfaces.callsAfter(surface) != 0 {
				t.Fatal("composition continued after an unsealed surface result")
			}
			assertT192LowSensitive(t, err)
		})
	}
}

func TestPrivacyCompositionRejectsMismatchedOrIncompleteSurfaceProofs(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	zeroCounts := map[string]int{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
	tests := []struct {
		name     string
		surface  PrivacySmokeSurface
		evidence privacyCompositionSurfaceEvidence
	}{
		{"wrong surface", PrivacySmokeSurfaceAPI, t192Evidence(PrivacySmokeSurfaceLoki)},
		{"wrong method", PrivacySmokeSurfaceAPI, newPrivacyCompositionSurfaceEvidenceForTest(PrivacySmokeSurfaceAPI, "exact_structured_query", "1", zeroCounts, privacyCompositionCollectorBindings{})},
		{"wrong policy", PrivacySmokeSurfaceAPI, newPrivacyCompositionSurfaceEvidenceForTest(PrivacySmokeSurfaceAPI, "bounded_memory_scan", "2", zeroCounts, privacyCompositionCollectorBindings{})},
		{"missing category", PrivacySmokeSurfaceAPI, newPrivacyCompositionSurfaceEvidenceForTest(PrivacySmokeSurfaceAPI, "bounded_memory_scan", "1", map[string]int{"synthetic_canary": 0}, privacyCompositionCollectorBindings{})},
		{"negative category", PrivacySmokeSurfaceAPI, newPrivacyCompositionSurfaceEvidenceForTest(PrivacySmokeSurfaceAPI, "bounded_memory_scan", "1", map[string]int{"synthetic_canary": -1, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}, privacyCompositionCollectorBindings{})},
		{"collector binding missing", PrivacySmokeSurfaceCollectorQueue, newPrivacyCompositionSurfaceEvidenceForTest(PrivacySmokeSurfaceCollectorQueue, "configuration_and_telemetry", "1", zeroCounts, privacyCompositionCollectorBindings{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			surfaces := newT192Surfaces()
			surfaces.results[tt.surface] = tt.evidence
			report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
				Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
				Clock: &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}},
			})
			if report != nil || err == nil || surfaces.callsAfter(tt.surface) != 0 {
				t.Fatal("mismatched surface proof entered the report")
			}
			assertT192LowSensitive(t, err)
		})
	}
}

func TestPrivacyCompositionStopsBeforeRemoteOrFinishesTheStartedRemoteStage(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	for _, failSurface := range []PrivacySmokeSurface{
		PrivacySmokeSurfaceAPI, PrivacySmokeSurfaceApplicationLog, PrivacySmokeSurfaceCollectorQueue,
		PrivacySmokeSurfaceReport, PrivacySmokeSurfaceTempo, PrivacySmokeSurfaceLoki,
		PrivacySmokeSurfaceLangfuseTrace, PrivacySmokeSurfaceLangfuseScore,
	} {
		t.Run(string(failSurface), func(t *testing.T) {
			surfaces := newT192Surfaces()
			if t192ExecutionIndex(failSurface) >= t192ExecutionIndex(PrivacySmokeSurfaceTempo) {
				surfaces.results[failSurface] = privacyCompositionSurfaceEvidence{}
				surfaces.errors[failSurface] = newPrivacyCompositionAttemptedFailureForTest(failSurface, "query_failed")
			} else {
				surfaces.errors[failSurface] = errors.New(t192Raw + " " + t192Secret)
			}
			report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
				Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
				Clock: &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}},
			})
			if failSurface == PrivacySmokeSurfaceAPI || failSurface == PrivacySmokeSurfaceApplicationLog ||
				failSurface == PrivacySmokeSurfaceCollectorQueue || failSurface == PrivacySmokeSurfaceReport {
				if report != nil || err == nil {
					t.Fatal("local artifact failure produced an unprovable report or lost its failure")
				}
				if surfaces.callsAfter(failSurface) != 0 || surfaces.remoteCalls() != 0 {
					t.Fatal("local artifact failure was followed by remote transport")
				}
				assertT192LowSensitive(t, err)
			} else {
				if err != nil || report == nil || report.Status() != "failed" {
					t.Fatal("attempted remote failure did not become a report-owned failure")
				}
				if surfaces.callsAfter(failSurface) != len(t192ExecutionOrder())-t192ExecutionIndex(failSurface)-1 {
					t.Fatal("a remote failure suppressed later independent remote evidence collection")
				}
				assertT192PrivacyReportEvidence(t, report, "failed", failSurface, "")
			}
		})
	}

	t.Run("remote failure before a sealed attempt", func(t *testing.T) {
		surfaces := newT192Surfaces()
		surfaces.results[PrivacySmokeSurfaceTempo] = privacyCompositionSurfaceEvidence{}
		surfaces.errors[PrivacySmokeSurfaceTempo] = errors.New(t192Raw + " " + t192Secret)
		report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
			Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
			Clock: &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}},
		})
		if report != nil || err == nil {
			t.Fatal("unsealed remote error was promoted to an attempted report entry")
		}
		assertT192LowSensitive(t, err)
	})

	t.Run("fixture failure", func(t *testing.T) {
		surfaces := newT192Surfaces()
		runner := &t192FixtureRunner{err: errors.New(t192Raw + " " + t192Secret)}
		report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
			Fixture: runner, Surfaces: surfaces, Clock: &t192CompositionClock{},
		})
		if report != nil || err == nil || len(surfaces.order) != 0 || runner.calls != 1 {
			t.Fatal("fixture failure reached a surface")
		}
		assertT192LowSensitive(t, err)
	})
}

// A confirmed leak anywhere in the run must dominate an earlier, unrelated query failure in
// the aggregate check: secondary diagnostics can never hide the primary security fact.
func TestPrivacyCompositionKeepsLeakPriorityOverEarlierQueryFailures(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	surfaces := newT192Surfaces()
	surfaces.results[PrivacySmokeSurfaceTempo] = privacyCompositionSurfaceEvidence{}
	surfaces.errors[PrivacySmokeSurfaceTempo] = newPrivacyCompositionAttemptedFailureForTest(PrivacySmokeSurfaceTempo, "backend_timeout")
	counts := map[string]int{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
	counts["synthetic_canary"] = 1
	surfaces.results[PrivacySmokeSurfaceLoki] = newPrivacyCompositionSurfaceEvidenceForTest(
		PrivacySmokeSurfaceLoki, "exact_structured_query", "1", counts, privacyCompositionCollectorBindings{},
	)
	report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
		Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
		Clock: &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}},
	})
	if err != nil || report == nil || report.Status() != "failed" {
		t.Fatalf("composition result = (%v,%v), want complete failed machine report", report, err)
	}
	encoded, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var document map[string]any
	if json.Unmarshal(encoded, &document) != nil {
		t.Fatal("report did not encode as JSON")
	}
	checks, ok := document["checks"].([]any)
	if !ok || len(checks) != 1 {
		t.Fatal("privacy report omitted the aggregate check")
	}
	check := checks[0].(map[string]any)
	if check["error_class"] != "unexpected_evidence" {
		t.Fatalf("aggregate check error_class = %v, want confirmed leak priority over earlier query failure", check["error_class"])
	}
}

func TestPrivacyCompositionKeepsConfirmedLeakPriorityInTheCompleteReport(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	for _, surface := range t192SchemaSurfaceOrder() {
		for _, category := range []string{"synthetic_canary", "credential", "authorization", "token", "recognized_pii"} {
			t.Run(string(surface)+"/"+category, func(t *testing.T) {
				surfaces := newT192Surfaces()
				counts := map[string]int{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
				counts[category] = 1
				surfaces.results[surface] = newPrivacyCompositionSurfaceEvidenceForTest(
					surface, t192EvidenceMethod(surface), "1", counts, t192EvidenceBindings(surface),
				)
				report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
					Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
					Clock: &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}},
				})
				if err != nil || report == nil || report.Status() != "failed" {
					t.Fatalf("confirmed %s/%s result = (%v,%v), want a complete failed machine report", surface, category, report, err)
				}
				if len(surfaces.order) != 8 {
					t.Fatal("confirmed leakage suppressed the remaining sealed surface proofs")
				}
				assertT192PrivacyReportEvidence(t, report, "failed", surface, category)
			})
		}
	}
}

// Report is a pre-existing typed chat fixture artifact. The current privacy report is built only
// after that surface has completed, then marshaled once as a separate serialization guard.
func TestPrivacyCompositionDoesNotUseTheCurrentReportAsTheReportSurface(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	fixture := t192Fixture(startedAt, startedAt.Add(30*time.Second))
	events := make([]string, 0, 10)
	surfaces := newT192Surfaces()
	surfaces.events = &events
	clock := &t192CompositionClock{times: []time.Time{fixture.Deadline.Add(-time.Nanosecond)}, events: &events}
	report, err := runPrivacyCompositionForTest(context.Background(), t192Request(fixture), privacyCompositionDependencies{
		Fixture: &t192FixtureRunner{result: fixture}, Surfaces: surfaces,
		Clock: clock,
	})
	if err != nil || report == nil {
		t.Fatal("composition failed")
	}
	if surfaces.callsFor(PrivacySmokeSurfaceReport) != 1 {
		t.Fatal("serialization guard replaced or duplicated the report surface")
	}
	if strings.Index(strings.Join(events, ","), "report") > strings.Index(strings.Join(events, ","), "clock") {
		t.Fatal("current privacy report existed before the prior chat report surface was scanned")
	}
	targetType := reflect.TypeOf(PrivacySmokeTarget{})
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		if field.Type == reflect.TypeOf(SmokeReport{}) || field.Type == reflect.TypeOf(&SmokeReport{}) || field.Type.Kind() == reflect.Slice {
			t.Fatal("report surface input can accept the current privacy report or its encoded bytes")
		}
	}
	assertT192PrivacyReport(t, report, "passed")
}

func t192Fixture(startedAt, deadline time.Time) PrivacyFixtureResult {
	return PrivacyFixtureResult{
		RunID: "privacy-run-t192", Marker: "privacy-marker-t192", RequestID: "request-t192", AITraceID: "ai-trace-t192",
		ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef",
		ManifestRef: "manifest-t192.json", APISummaryRef: "api-t192.json", ApplicationLogRef: "application-t192.json",
		CollectorArtifactRef: "collector-t192.json", ChatReportRef: "chat-report-t192.json",
		StartedAt: startedAt, Deadline: deadline, RequestSent: true, ChatSucceeded: true,
	}
}

func t192Request(fixture PrivacyFixtureResult) privacyCompositionRequest {
	return privacyCompositionRequest{
		Profile: "grafana", ForbiddenCanary: t192Canary, RunID: fixture.RunID, Marker: fixture.Marker,
		StartedAt: fixture.StartedAt, Deadline: fixture.Deadline, SurfaceTimeout: 250 * time.Millisecond,
	}
}

type t192FixtureRunner struct {
	calls   int
	request PrivacyFixtureRequest
	result  PrivacyFixtureResult
	err     error
	events  *[]string
}

func (runner *t192FixtureRunner) Run(_ context.Context, request PrivacyFixtureRequest) (PrivacyFixtureResult, error) {
	runner.calls++
	if runner.events != nil {
		*runner.events = append(*runner.events, "fixture")
	}
	runner.request = request
	return runner.result, runner.err
}

type t192CompositionClock struct {
	times  []time.Time
	calls  int
	events *[]string
}

func (clock *t192CompositionClock) Now() time.Time {
	clock.calls++
	if clock.events != nil {
		*clock.events = append(*clock.events, "clock")
	}
	if len(clock.times) == 0 {
		return time.Now().UTC()
	}
	index := clock.calls - 1
	if index >= len(clock.times) {
		index = len(clock.times) - 1
	}
	return clock.times[index]
}

func (*t192CompositionClock) Wait(context.Context, time.Duration) error { return nil }

type t192CompositionSurfaces struct {
	mu              sync.Mutex
	order           []PrivacySmokeSurface
	targets         []PrivacySmokeTarget
	deadlines       []time.Time
	contexts        []context.Context
	remaining       []time.Duration
	delays          map[PrivacySmokeSurface]time.Duration
	uncanceledPrior bool
	results         map[PrivacySmokeSurface]privacyCompositionSurfaceEvidence
	errors          map[PrivacySmokeSurface]error
	events          *[]string
}

func newT192Surfaces() *t192CompositionSurfaces {
	results := make(map[PrivacySmokeSurface]privacyCompositionSurfaceEvidence, 8)
	for _, surface := range t192SchemaSurfaceOrder() {
		results[surface] = t192Evidence(surface)
	}
	return &t192CompositionSurfaces{results: results, errors: make(map[PrivacySmokeSurface]error), delays: make(map[PrivacySmokeSurface]time.Duration)}
}

func (surfaces *t192CompositionSurfaces) Scan(ctx context.Context, target PrivacySmokeTarget) (privacyCompositionSurfaceEvidence, error) {
	surfaces.mu.Lock()
	defer surfaces.mu.Unlock()
	if count := len(surfaces.contexts); count > 0 && surfaces.contexts[count-1].Err() == nil {
		surfaces.uncanceledPrior = true
	}
	surfaces.order = append(surfaces.order, target.Surface)
	if surfaces.events != nil {
		*surfaces.events = append(*surfaces.events, string(target.Surface))
	}
	surfaces.targets = append(surfaces.targets, target)
	surfaces.contexts = append(surfaces.contexts, ctx)
	deadline, ok := ctx.Deadline()
	if !ok {
		return privacyCompositionSurfaceEvidence{}, errors.New("unbounded surface")
	}
	surfaces.deadlines = append(surfaces.deadlines, deadline)
	surfaces.remaining = append(surfaces.remaining, time.Until(deadline))
	if delay := surfaces.delays[target.Surface]; delay > 0 {
		time.Sleep(delay)
	}
	return surfaces.results[target.Surface], surfaces.errors[target.Surface]
}

func (surfaces *t192CompositionSurfaces) callsFor(surface PrivacySmokeSurface) int {
	count := 0
	for _, current := range surfaces.order {
		if current == surface {
			count++
		}
	}
	return count
}

func (surfaces *t192CompositionSurfaces) callsAfter(surface PrivacySmokeSurface) int {
	for index, current := range surfaces.order {
		if current == surface {
			return len(surfaces.order) - index - 1
		}
	}
	return 0
}

func (surfaces *t192CompositionSurfaces) remoteCalls() int {
	return surfaces.callsFor(PrivacySmokeSurfaceTempo) + surfaces.callsFor(PrivacySmokeSurfaceLoki) +
		surfaces.callsFor(PrivacySmokeSurfaceLangfuseTrace) + surfaces.callsFor(PrivacySmokeSurfaceLangfuseScore)
}

func t192Evidence(surface PrivacySmokeSurface) privacyCompositionSurfaceEvidence {
	bindings := privacyCompositionCollectorBindings{}
	if surface == PrivacySmokeSurfaceCollectorQueue {
		bindings = privacyCompositionCollectorBindings{RuntimeConfigDigestVerified: true, PrequeueArtifactHashVerified: true, ComponentIdentityVerified: true, ExportAdmissionCorrelated: true}
	}
	return newPrivacyCompositionSurfaceEvidenceForTest(surface, t192EvidenceMethod(surface), "1", map[string]int{
		"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0,
	}, bindings)
}

func t192EvidenceMethod(surface PrivacySmokeSurface) string {
	return map[PrivacySmokeSurface]string{
		PrivacySmokeSurfaceAPI: "bounded_memory_scan", PrivacySmokeSurfaceApplicationLog: "projection_and_exact_query",
		PrivacySmokeSurfaceCollectorQueue: "configuration_and_telemetry", PrivacySmokeSurfaceTempo: "bounded_trace_document",
		PrivacySmokeSurfaceLoki: "exact_structured_query", PrivacySmokeSurfaceLangfuseTrace: "bounded_platform_document",
		PrivacySmokeSurfaceLangfuseScore: "bounded_platform_document", PrivacySmokeSurfaceReport: "contained_artifact_scan",
	}[surface]
}

func t192EvidenceBindings(surface PrivacySmokeSurface) privacyCompositionCollectorBindings {
	if surface == PrivacySmokeSurfaceCollectorQueue {
		return privacyCompositionCollectorBindings{RuntimeConfigDigestVerified: true, PrequeueArtifactHashVerified: true, ComponentIdentityVerified: true, ExportAdmissionCorrelated: true}
	}
	return privacyCompositionCollectorBindings{}
}

func t192SchemaSurfaceOrder() []PrivacySmokeSurface {
	return []PrivacySmokeSurface{
		PrivacySmokeSurfaceAPI, PrivacySmokeSurfaceApplicationLog, PrivacySmokeSurfaceCollectorQueue,
		PrivacySmokeSurfaceTempo, PrivacySmokeSurfaceLoki, PrivacySmokeSurfaceLangfuseTrace,
		PrivacySmokeSurfaceLangfuseScore, PrivacySmokeSurfaceReport,
	}
}

func t192ExecutionOrder() []PrivacySmokeSurface {
	return []PrivacySmokeSurface{
		PrivacySmokeSurfaceAPI, PrivacySmokeSurfaceApplicationLog, PrivacySmokeSurfaceCollectorQueue,
		PrivacySmokeSurfaceReport, PrivacySmokeSurfaceTempo, PrivacySmokeSurfaceLoki,
		PrivacySmokeSurfaceLangfuseTrace, PrivacySmokeSurfaceLangfuseScore,
	}
}

func t192ExecutionIndex(surface PrivacySmokeSurface) int {
	for index, candidate := range t192ExecutionOrder() {
		if candidate == surface {
			return index
		}
	}
	return -1
}

func assertT192SurfaceBudgets(t *testing.T, surfaces *t192CompositionSurfaces) {
	t.Helper()
	if len(surfaces.deadlines) != 8 || len(surfaces.contexts) != 8 {
		t.Fatal("every surface needs an independent bounded context")
	}
	for index := range surfaces.deadlines {
		if surfaces.deadlines[index].After(surfaces.targets[index].Deadline) {
			t.Fatal("surface context extended the fixture deadline")
		}
		remaining := surfaces.remaining[index]
		if remaining <= 0 || remaining > 300*time.Millisecond {
			t.Fatalf("surface budget = %v, want a fresh short timeout", remaining)
		}
		for previous := 0; previous < index; previous++ {
			if surfaces.contexts[index] == surfaces.contexts[previous] {
				t.Fatal("surface contexts were shared")
			}
		}
	}
}

func assertT192PrivacyReport(t *testing.T, report *SmokeReport, status string) {
	t.Helper()
	assertT192PrivacyReportEvidence(t, report, status, "", "")
}

func assertT192PrivacyReportEvidence(t *testing.T, report *SmokeReport, status string, failedSurface PrivacySmokeSurface, failedCategory string) {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(encoded, &document) != nil || document["scenario"] != "privacy" || document["status"] != status {
		t.Fatal("privacy report did not preserve its scenario/status")
	}
	validator, err := NewSmokeReportSchemaValidator(loadSmokeReportSchema(t))
	if err != nil {
		t.Fatalf("NewSmokeReportSchemaValidator() error = %v", err)
	}
	if err := validator.ValidateJSON(encoded); err != nil {
		t.Fatalf("privacy report is not schema-valid: %v", err)
	}
	evidence, ok := document["privacy_evidence"].([]any)
	if !ok || len(evidence) != 8 {
		t.Fatal("privacy report omitted the closed eight-surface proof set")
	}
	for index, surface := range t192SchemaSurfaceOrder() {
		item := evidence[index].(map[string]any)
		if item["surface"] != string(surface) || item["attempted"] != true || item["scanner_policy_version"] != "1" {
			t.Fatal("privacy report evidence topology is not fixed")
		}
		counts, ok := item["counts"].(map[string]any)
		if !ok || len(counts) != 5 {
			t.Fatal("privacy report omitted closed category counts")
		}
		wantStatus := "passed"
		if surface == failedSurface {
			wantStatus = "failed"
		}
		if item["status"] != wantStatus {
			t.Fatalf("surface %q status = %v, want %s", surface, item["status"], wantStatus)
		}
		for category, value := range counts {
			want := float64(0)
			if surface == failedSurface && category == failedCategory {
				want = 1
			}
			if value != want {
				t.Fatalf("surface %q category %q = %v, want %v", surface, category, value, want)
			}
		}
	}
	for _, forbidden := range []string{t192Canary, t192Raw, t192Secret, "manifest-t192.json", "chat-report-t192.json"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("privacy report exposed a source fact or artifact capability")
		}
	}
}

func assertT192LowSensitive(t *testing.T, err error) {
	t.Helper()
	for _, forbidden := range []string{t192Canary, t192Raw, t192Secret, "manifest-t192.json", "api-t192.json", "application-t192.json", "collector-t192.json", "chat-report-t192.json", "privacy-run-t192", "privacy-marker-t192", "request-t192", "ai-trace-t192", "0123456789abcdef"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatal("composition error exposed a sensitive or correlating fact")
		}
	}
}

func t192Class(err error) string {
	type classified interface{ Class() string }
	if value, ok := err.(classified); ok {
		return value.Class()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
