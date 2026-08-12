package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

var errChatSmokeFailed = errors.New("chat smoke verification failed")

const (
	chatBaselineTimeout     = 2 * time.Second
	chatQueryTimeout        = 2 * time.Second
	maximumChatObservations = 100
)

type ChatSmokeBackend interface {
	QueryTempoChat(context.Context, ChatSmokeTarget) ([]ChatObservation, error)
	QueryLokiChat(context.Context, ChatSmokeTarget) ([]ChatObservation, error)
	QueryLangfuseChat(context.Context, ChatSmokeTarget) ([]ChatObservation, error)
	BaselineLLMRequestCount(context.Context) (int64, error)
	LLMRequestCount(context.Context) (int64, error)
}

type ChatSmokeIdentity struct{ RunID, Marker string }

type ChatSmokeAPIResult struct {
	RequestID, AITraceID, ServiceTraceID, SpanID string
}

type ChatSmokeTarget struct {
	Marker, RequestID, AITraceID, ServiceTraceID, SpanID string
	StartedAt, Deadline                                  time.Time
	Limit                                                int
}

type ChatObservation struct {
	Marker, RequestID, AITraceID, ServiceTraceID, SpanID string
	ObservedAt                                           time.Time
}

type ChatSmokeRequest struct {
	Deadline time.Time
	Profile  string
}

type ChatSmokeRunnerDependencies struct {
	Backend         ChatSmokeBackend
	Clock           PollerClock
	PollInterval    time.Duration
	IdentityFactory func(context.Context) (ChatSmokeIdentity, error)
	Trigger         func(context.Context, ChatSmokeIdentity) (ChatSmokeAPIResult, error)
}

// RunChatSmoke keeps the model result and telemetry verification in separate error domains.
// A completed API attempt always yields a low-sensitivity report; only the original model error
// is returned to the caller, while delayed or missing telemetry remains report-owned evidence.
func RunChatSmoke(ctx context.Context, request ChatSmokeRequest, deps ChatSmokeRunnerDependencies) (*SmokeReport, error) {
	startedAt, identity, bounded, cancel, err := prepareChatSmoke(ctx, request, deps)
	if err != nil {
		return nil, err
	}
	defer cancel()
	type metricResult struct {
		count int64
		err   error
	}
	baselineResult := make(chan metricResult, 1)
	baselineCtx, stopBaseline := context.WithTimeout(bounded, chatBaselineTimeout)
	go func() {
		count, queryErr := deps.Backend.BaselineLLMRequestCount(baselineCtx)
		baselineResult <- metricResult{count: count, err: queryErr}
	}()
	result, triggerErr := deps.Trigger(bounded, identity)
	checks := []BackendCheckInput{outcomeCheck("api", triggerErr == nil, "api", triggerErrorClass(triggerErr), map[string]any{"response_status": int64(boolToInt(triggerErr == nil))})}
	if triggerErr != nil {
		stopBaseline()
		report, buildErr := buildChatSmokeReport(identity, safeFailedChatResult(result), request, startedAt, deps.Clock.Now(), checks)
		if buildErr != nil {
			return nil, errors.Join(triggerErr, errChatSmokeFailed)
		}
		return report, triggerErr
	}
	if !validChatAPIResult(result) {
		stopBaseline()
		checks[0] = outcomeCheck("api", false, "api", "malformed_response", map[string]any{"response_status": int64(1)})
		return buildChatSmokeReport(identity, ChatSmokeAPIResult{}, request, startedAt, deps.Clock.Now(), checks)
	}

	target := ChatSmokeTarget{Marker: identity.Marker, RequestID: result.RequestID, AITraceID: result.AITraceID, ServiceTraceID: result.ServiceTraceID, SpanID: result.SpanID, StartedAt: startedAt, Deadline: request.Deadline, Limit: maximumChatObservations}
	baseline := <-baselineResult
	stopBaseline()
	afterCtx, stopAfter := context.WithTimeout(bounded, chatBaselineTimeout)
	after, afterErr := deps.Backend.LLMRequestCount(afterCtx)
	stopAfter()
	checks = append(checks, pollChatBackends(bounded, target, deps)...)
	delta := int64(0)
	if baseline.err == nil && afterErr == nil {
		delta = after - baseline.count
	}
	checks = append(checks, outcomeCheck("prometheus", baseline.err == nil && afterErr == nil && delta > 0, "query", metricErrorClass(baseline.err, afterErr, delta), map[string]any{"metric_delta": delta}))
	return buildChatSmokeReport(identity, result, request, startedAt, deps.Clock.Now(), checks)
}

func prepareChatSmoke(ctx context.Context, request ChatSmokeRequest, deps ChatSmokeRunnerDependencies) (time.Time, ChatSmokeIdentity, context.Context, context.CancelFunc, error) {
	if ctx == nil || deps.Backend == nil || deps.Clock == nil || deps.Trigger == nil || deps.PollInterval <= 0 || !contains(allowedProfiles, request.Profile) {
		return time.Time{}, ChatSmokeIdentity{}, nil, nil, errChatSmokeFailed
	}
	startedAt := deps.Clock.Now().UTC()
	if request.Deadline.IsZero() || !request.Deadline.After(startedAt) || request.Deadline.Sub(startedAt) > time.Minute {
		return time.Time{}, ChatSmokeIdentity{}, nil, nil, errChatSmokeFailed
	}
	factory := deps.IdentityFactory
	if factory == nil {
		factory = newChatSmokeIdentity
	}
	identity, err := factory(ctx)
	if err != nil || !isSafePollMarker(identity.RunID) || !isSafePollMarker(identity.Marker) {
		return time.Time{}, ChatSmokeIdentity{}, nil, nil, errChatSmokeFailed
	}
	if ctx.Err() != nil {
		return time.Time{}, ChatSmokeIdentity{}, nil, nil, errChatSmokeFailed
	}
	// PollerClock is the authoritative clock for smoke windows. A custom deadline carrier keeps
	// deterministic/replayed windows valid even when their timestamps are historical wall time;
	// parent cancellation is still propagated through Done and Err.
	bounded, cancel := boundedChatContext(ctx, request.Deadline)
	return startedAt, identity, bounded, cancel, nil
}

func boundedChatContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.After(time.Now()) {
		return context.WithDeadline(parent, deadline)
	}
	// Historical windows occur only in deterministic replay/tests. They remain bounded by the
	// injected clock, while production windows use context.WithDeadline above to cancel I/O.
	return chatDeadlineContext{Context: parent, deadline: deadline}, func() {}
}

type chatDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c chatDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

type chatPollState struct {
	backend, evidenceKey string
	query                func(context.Context, ChatSmokeTarget) ([]ChatObservation, error)
	passed, sawMismatch  bool
	lastErr              error
}

func pollChatBackends(ctx context.Context, target ChatSmokeTarget, deps ChatSmokeRunnerDependencies) []BackendCheckInput {
	states := []*chatPollState{
		{backend: "tempo", evidenceKey: "matched_spans", query: deps.Backend.QueryTempoChat},
		{backend: "loki", evidenceKey: "matched_logs", query: deps.Backend.QueryLokiChat},
		{backend: "langfuse_trace", evidenceKey: "matched_traces", query: deps.Backend.QueryLangfuseChat},
	}
	for {
		allPassed := true
		for _, state := range states {
			if !state.passed {
				pollChatBackend(ctx, target, state)
			}
			allPassed = allPassed && state.passed
		}
		if allPassed || !deps.Clock.Now().Before(target.Deadline) {
			return chatPollChecks(states)
		}
		if err := deps.Clock.Wait(ctx, minimumDuration(deps.PollInterval, target.Deadline.Sub(deps.Clock.Now()))); err != nil {
			for _, state := range states {
				if !state.passed {
					state.lastErr = err
				}
			}
			return chatPollChecks(states)
		}
	}
}

func pollChatBackend(ctx context.Context, target ChatSmokeTarget, state *chatPollState) {
	queryCtx, cancel := context.WithTimeout(ctx, chatQueryTimeout)
	observations, err := state.query(queryCtx, target)
	cancel()
	if err != nil {
		state.lastErr = err
		return
	}
	if len(observations) > maximumChatObservations {
		state.lastErr = errors.New("chat smoke query returned too many observations")
		return
	}
	for _, observation := range observations {
		if observation.ObservedAt.Before(target.StartedAt) || observation.ObservedAt.After(target.Deadline) || observation.Marker != target.Marker {
			continue
		}
		if chatObservationMatches(observation, target) {
			state.passed = true
			return
		}
		state.sawMismatch = true
	}
}

func chatPollChecks(states []*chatPollState) []BackendCheckInput {
	checks := make([]BackendCheckInput, 0, len(states))
	for _, state := range states {
		class := "backend_timeout"
		if state.lastErr != nil {
			class = markerErrorClass(state.lastErr)
		} else if state.sawMismatch {
			class = "identity_mismatch"
		}
		checks = append(checks, outcomeCheck(state.backend, state.passed, "query", class, map[string]any{state.evidenceKey: int64(boolToInt(state.passed))}))
	}
	return checks
}

func chatObservationMatches(observation ChatObservation, target ChatSmokeTarget) bool {
	return observation.RequestID == target.RequestID && observation.AITraceID == target.AITraceID && observation.ServiceTraceID == target.ServiceTraceID && observation.SpanID == target.SpanID
}

func validChatAPIResult(result ChatSmokeAPIResult) bool {
	return isSafePollMarker(result.RequestID) && isSafePollMarker(result.AITraceID) && isSafePollMarker(result.ServiceTraceID) && isSafePollMarker(result.SpanID)
}

func safeFailedChatResult(result ChatSmokeAPIResult) ChatSmokeAPIResult {
	if isSafePollMarker(result.RequestID) && isSafePollMarker(result.AITraceID) {
		return ChatSmokeAPIResult{RequestID: result.RequestID, AITraceID: result.AITraceID}
	}
	return ChatSmokeAPIResult{}
}

func triggerErrorClass(err error) string {
	if err == nil {
		return ""
	}
	var classified interface{ Class() string }
	if errors.As(err, &classified) && contains(allowedErrorClasses, classified.Class()) {
		return classified.Class()
	}
	return "backend_unavailable"
}

func metricErrorClass(baselineErr, afterErr error, delta int64) string {
	if baselineErr != nil || afterErr != nil {
		return "query_failed"
	}
	if delta <= 0 {
		return "metric_delta_missing"
	}
	return ""
}

func buildChatSmokeReport(identity ChatSmokeIdentity, result ChatSmokeAPIResult, request ChatSmokeRequest, startedAt, finishedAt time.Time, checks []BackendCheckInput) (*SmokeReport, error) {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	if finishedAt.After(request.Deadline) {
		finishedAt = request.Deadline
	}
	return BuildSmokeReport(SmokeReportInput{RunID: identity.RunID, Marker: identity.Marker, Profile: request.Profile, Scenario: "chat", RequestID: result.RequestID, AITraceID: result.AITraceID, StartedAt: startedAt, Deadline: request.Deadline, FinishedAt: finishedAt, Checks: checks, Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"}})
}

func newChatSmokeIdentity(context.Context) (ChatSmokeIdentity, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return ChatSmokeIdentity{}, err
	}
	nonce := hex.EncodeToString(value)
	return ChatSmokeIdentity{RunID: "chat-run-" + nonce, Marker: "chat-marker-" + nonce}, nil
}
