// Package observability contains the thin HTTP controller for infra-only smoke traffic.
package observability

import (
	"context"
	"net/http"

	v1 "github.com/ashjazz/Longtermism/api/v1/observability"
	logicobservability "github.com/ashjazz/Longtermism/internal/logic/observability"
	"github.com/gogf/gf/v2/util/gvalid"
)

const (
	infraSmokeInvalidRequestMessage = "invalid infra smoke request"
	infraSmokeDisabledMessage       = "infra smoke disabled"
	infraSmokeInternalErrorMessage  = "internal server error"
)

// InfraSmokeControllerDependencies keeps HTTP-specific concerns at the application edge. The
// runner receives only validated low-sensitive identities, so it never parses HTTP input itself.
type InfraSmokeControllerDependencies struct {
	SmokeEnabled         bool
	Runner               logicobservability.InfraSmokeRunner
	RequestIDFromContext func(context.Context) string
}

// ControllerV1 is intentionally small: route binding occurs in T052 and all infrastructure
// telemetry behavior remains in the T050 usecase.
type ControllerV1 struct {
	smokeEnabled         bool
	runner               logicobservability.InfraSmokeRunner
	requestIDFromContext func(context.Context) string
}

func NewV1(dependencies InfraSmokeControllerDependencies) *ControllerV1 {
	requestIDFromContext := dependencies.RequestIDFromContext
	if requestIDFromContext == nil {
		requestIDFromContext = func(context.Context) string { return "" }
	}
	return &ControllerV1{
		smokeEnabled:         dependencies.SmokeEnabled,
		runner:               dependencies.Runner,
		requestIDFromContext: requestIDFromContext,
	}
}

// InfraSmoke validates the transport contract, delegates to the pure usecase, and maps only
// stable public errors. In particular, provider/exporter error details must never enter the API.
func (c *ControllerV1) InfraSmoke(ctx context.Context, request *v1.InfraSmokeReq) (*v1.InfraSmokeSuccessEnvelope, error) {
	requestID := c.requestIDFromContext(ctx)
	if !c.smokeEnabled {
		return nil, newInfraSmokeControllerError(http.StatusNotFound, infraSmokeDisabledMessage, requestID, "")
	}
	if request == nil || gvalid.New().Data(request).Run(ctx) != nil {
		return nil, newInfraSmokeControllerError(http.StatusBadRequest, infraSmokeInvalidRequestMessage, requestID, "")
	}
	if c.runner == nil {
		return nil, newInfraSmokeControllerError(http.StatusInternalServerError, infraSmokeInternalErrorMessage, requestID, request.SmokeRunID)
	}

	result, err := c.runner.Run(ctx, logicobservability.InfraSmokeInput{
		RequestID:  requestID,
		SmokeRunID: request.SmokeRunID,
	})
	if err != nil || result.Status != logicobservability.InfraSmokeStatusOK {
		return nil, newInfraSmokeControllerError(http.StatusInternalServerError, infraSmokeInternalErrorMessage, requestID, request.SmokeRunID)
	}
	return &v1.InfraSmokeSuccessEnvelope{
		Code:    0,
		Message: "OK",
		Data:    v1.InfraSmokeData{Status: v1.InfraSmokeStatusOK},
		Meta:    infraSmokeMeta(requestID, request.SmokeRunID),
	}, nil
}

// InfraSmokeControllerError contains the pre-sanitized envelope that T052 can write directly.
// Its Error method deliberately duplicates only the public message, never the wrapped failure.
type InfraSmokeControllerError struct {
	statusCode int
	envelope   v1.InfraSmokeErrorEnvelope
}

func (e InfraSmokeControllerError) Error() string {
	return e.envelope.Message
}

func (e InfraSmokeControllerError) StatusCode() int {
	return e.statusCode
}

func (e InfraSmokeControllerError) Envelope() v1.InfraSmokeErrorEnvelope {
	return e.envelope
}

func newInfraSmokeControllerError(statusCode int, message, requestID, smokeRunID string) error {
	return InfraSmokeControllerError{
		statusCode: statusCode,
		envelope: v1.InfraSmokeErrorEnvelope{
			Code:    statusCode,
			Message: message,
			Data:    nil,
			Meta:    infraSmokeMeta(requestID, smokeRunID),
		},
	}
}

func infraSmokeMeta(requestID, smokeRunID string) v1.InfraSmokeMeta {
	return v1.InfraSmokeMeta{RequestID: requestID, SmokeRunID: smokeRunID}
}

var _ error = InfraSmokeControllerError{}
