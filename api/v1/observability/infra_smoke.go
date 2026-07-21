// Package observability defines the transport contract for the infra-only smoke endpoint.
package observability

import (
	"net/http"
	"regexp"

	"github.com/gogf/gf/v2/frame/g"
)

var smokeRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,128}$`)

// IsValidSmokeRunID is the shared boundary rule for the optional correlation marker. Keeping
// this beside the transport contract prevents HTTP middleware from projecting a value that the
// controller would later reject into JSONL or trace attributes.
func IsValidSmokeRunID(value string) bool {
	return value == "" || smokeRunIDPattern.MatchString(value)
}

const (
	SmokeRunIDHeader             = "X-Observability-Smoke-Run-ID"
	InfraSmokeStatusOK           = "ok"
	InfraSmokeDisabledHTTPStatus = http.StatusNotFound
)

// InfraSmokeReq deliberately accepts only an optional, opaque run marker from a header.
// It never accepts a request body/query input, so a caller cannot smuggle observability payloads
// or choose an arbitrary backend query expression at the HTTP boundary.
type InfraSmokeReq struct {
	g.Meta     `path:"/observability/infra-smoke" method:"get" tags:"Observability" summary:"Infrastructure smoke"`
	SmokeRunID string `in:"header" p:"X-Observability-Smoke-Run-ID" json:"-" v:"length:0,128|regex:^([A-Za-z0-9._-]{8,128})?$"`
}

type InfraSmokeData struct {
	Status string `json:"status"`
}

// InfraSmokeMeta deliberately has no AI/eval fields: infra smoke must not manufacture AI facts.
type InfraSmokeMeta struct {
	RequestID  string `json:"request_id"`
	SmokeRunID string `json:"smoke_run_id,omitempty"`
}

type InfraSmokeSuccessEnvelope struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    InfraSmokeData `json:"data"`
	Meta    InfraSmokeMeta `json:"meta"`
}

type InfraSmokeErrorEnvelope struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    any            `json:"data"`
	Meta    InfraSmokeMeta `json:"meta"`
}
