package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// T117 契约的 GREEN 实现：告警触发与恢复查询。
//
// 生产约束（FR-009）：告警验收必须来自真实 API 的 firing/resolved 时间
// 证据，而不是 rule 文件存在性。契约固定：
// - 四类告警逐一执行 TriggerAlertClass → 受限窗口轮询 firing →
//   ResolveAlertClass → 受限窗口轮询 normal；
// - 每段轮询窗口从触发/恢复时刻起算，上界 120 秒且不得越过
//   request.Deadline（FR-012 受限时间窗口）；
// - 触发超时 → alert_not_firing、恢复超时 → alert_not_resolved，两类
//   超时都必须恢复告警条件；
// - stale/late 观察（ObservedAt 越过窗口）必须被隔离，不得冒充 firing；
// - 本 runner 契约中不存在 rule 文件检查：Backend 只提供告警状态查询。

var errAlertSmokeFailed = errors.New("alert smoke verification failed")

// alertSmokeRecoveryWindow 是 firing 段与 resolved 段各自轮询窗口的上界。
const alertSmokeRecoveryWindow = 120 * time.Second

type AlertSmokeClass string

const (
	AlertClassHTTPError       AlertSmokeClass = "http_error_rate"
	AlertClassExporterFailure AlertSmokeClass = "exporter_delivery_failure"
	AlertClassQueuePressure   AlertSmokeClass = "queue_pressure"
	AlertClassStoragePressure AlertSmokeClass = "storage_pressure"
)

type AlertStateObservation struct {
	AlertUID   string
	State      string
	ObservedAt time.Time
}

type AlertQueryWindow struct {
	StartedAt time.Time
	Deadline  time.Time
}

type AlertSmokeBackend interface {
	AlertStates(context.Context, AlertQueryWindow) ([]AlertStateObservation, error)
}

type AlertSmokeInjector interface {
	TriggerAlertClass(context.Context, AlertSmokeClass) error
	ResolveAlertClass(context.Context, AlertSmokeClass) error
}

type AlertSmokeIdentity struct{ RunID, Marker string }

type AlertSmokeIdentityFactory func(context.Context) (AlertSmokeIdentity, error)

type AlertSmokeRequest struct {
	Deadline time.Time
	Profile  string
	Classes  []AlertSmokeClass
}

type AlertSmokeDependencies struct {
	Backend         AlertSmokeBackend
	Injector        AlertSmokeInjector
	Clock           PollerClock
	IdentityFactory AlertSmokeIdentityFactory
	PollInterval    time.Duration
}

// RunAlertSmoke 对 request.Classes 逐类验证 firing/resolved 时间证据。
func RunAlertSmoke(ctx context.Context, request AlertSmokeRequest, deps AlertSmokeDependencies) (*SmokeReport, error) {
	if err := validateAlertRun(ctx, request, deps); err != nil {
		return nil, err
	}
	clock := deps.Clock
	startedAt := clock.Now().UTC()

	identityFactory := deps.IdentityFactory
	if identityFactory == nil {
		identityFactory = newAlertSmokeIdentity
	}
	identity, err := identityFactory(ctx)
	if err != nil || !isOpaqueSmokeIdentity(identity.RunID) || !isSafePollMarker(identity.Marker) {
		return nil, errAlertSmokeFailed
	}

	firingCount := 0
	resolvedCount := 0
	cleanupStatus := "completed"
	var residualResources []string
	var failedCheck BackendCheckInput

	for _, class := range request.Classes {
		if err := deps.Injector.TriggerAlertClass(ctx, class); err != nil {
			failedCheck = BackendCheckInput{Backend: "grafana", Status: "failed", FailureStage: "export", ErrorClass: "export_failed"}
			break
		}
		fired, queryErr := pollAlertState(ctx, deps, request, "firing")
		if queryErr != nil {
			_ = deps.Injector.ResolveAlertClass(ctx, class)
			failedCheck = BackendCheckInput{Backend: "grafana", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"}
			break
		}
		if !fired {
			_ = deps.Injector.ResolveAlertClass(ctx, class)
			failedCheck = BackendCheckInput{Backend: "grafana", Status: "failed", FailureStage: "query", ErrorClass: "alert_not_firing"}
			break
		}
		firingCount++

		if err := deps.Injector.ResolveAlertClass(ctx, class); err != nil {
			cleanupStatus = "failed"
			residualResources = []string{"alert-condition-active"}
			break
		}
		resolved, queryErr := pollAlertState(ctx, deps, request, "normal")
		if queryErr != nil {
			failedCheck = BackendCheckInput{Backend: "grafana", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"}
			break
		}
		if !resolved {
			failedCheck = BackendCheckInput{Backend: "grafana", Status: "failed", FailureStage: "query", ErrorClass: "alert_not_resolved"}
			break
		}
		resolvedCount++
	}

	checks := make([]BackendCheckInput, 0, 1)
	if failedCheck.Status != "" {
		checks = append(checks, failedCheck)
	} else {
		checks = append(checks, BackendCheckInput{
			Backend:      "grafana",
			Status:       "passed",
			FailureStage: "none",
			Evidence: map[string]any{
				"alerts_firing":   int64(firingCount),
				"alerts_resolved": int64(resolvedCount),
			},
		})
	}
	return buildAlertReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, checks)
}

// pollAlertState 在受限窗口内轮询 wanted 状态。窗口从本次轮询开始时刻起算，
// 上界 120 秒且以 request.Deadline 为上界；ObservedAt 越过窗口的观察被隔离。
func pollAlertState(ctx context.Context, deps AlertSmokeDependencies, request AlertSmokeRequest, wanted string) (bool, error) {
	pollStart := deps.Clock.Now()
	pollEnd := request.Deadline
	if windowEnd := pollStart.Add(alertSmokeRecoveryWindow); windowEnd.Before(pollEnd) {
		pollEnd = windowEnd
	}
	window := AlertQueryWindow{StartedAt: pollStart, Deadline: pollEnd}
	for deps.Clock.Now().Before(pollEnd) {
		observations, err := deps.Backend.AlertStates(ctx, window)
		if err != nil {
			return false, err
		}
		for _, observation := range observations {
			if observation.State != wanted {
				continue
			}
			if observation.ObservedAt.Before(pollStart) || observation.ObservedAt.After(pollEnd) {
				continue
			}
			return true, nil
		}
		if err := deps.Clock.Wait(ctx, deps.PollInterval); err != nil {
			return false, err
		}
	}
	return false, nil
}

func validateAlertRun(ctx context.Context, request AlertSmokeRequest, deps AlertSmokeDependencies) error {
	if ctx == nil {
		return errAlertSmokeFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deps.Backend == nil || deps.Injector == nil || deps.Clock == nil || deps.PollInterval <= 0 {
		return errAlertSmokeFailed
	}
	if !contains(allowedProfiles, request.Profile) {
		return errAlertSmokeFailed
	}
	if request.Deadline.IsZero() || !request.Deadline.After(deps.Clock.Now()) {
		return errAlertSmokeFailed
	}
	if len(request.Classes) == 0 {
		return errAlertSmokeFailed
	}
	for _, class := range request.Classes {
		switch class {
		case AlertClassHTTPError, AlertClassExporterFailure, AlertClassQueuePressure, AlertClassStoragePressure:
		default:
			return errAlertSmokeFailed
		}
	}
	return nil
}

func buildAlertReport(request AlertSmokeRequest, identity AlertSmokeIdentity, startedAt, finishedAt time.Time, cleanupStatus string, residualResources []string, checks []BackendCheckInput) (*SmokeReport, error) {
	return BuildSmokeReport(SmokeReportInput{
		RunID:      identity.RunID,
		Marker:     identity.Marker,
		Profile:    request.Profile,
		Scenario:   "alert",
		StartedAt:  startedAt,
		Deadline:   request.Deadline,
		FinishedAt: finishedAt,
		Checks:     checks,
		Cleanup: SmokeCleanupInput{
			Status:               cleanupStatus,
			ResidualResources:    residualResources,
			TemporaryCredentials: "not_created",
			TemporaryData:        "not_created",
		},
	})
}

func newAlertSmokeIdentity(context.Context) (AlertSmokeIdentity, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return AlertSmokeIdentity{}, err
	}
	encoded := hex.EncodeToString(bytes)
	return AlertSmokeIdentity{RunID: "run-" + encoded, Marker: "marker-" + encoded}, nil
}
