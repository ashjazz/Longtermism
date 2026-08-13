package smoke

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

const (
	smokeReportSchemaURI            = "https://longtermism.local/schemas/observability-smoke-report.json"
	draft2020SchemaURI              = "https://json-schema.org/draft/2020-12/schema"
	maximumSmokeReportSchemaBytes   = 256 * 1024
	maximumSmokeReportDocumentBytes = 1024 * 1024
	maximumSmokeJSONDepth           = 64
	maximumSmokeSchemaNodes         = 10_000
)

var (
	errInvalidSmokeReportSchema   = errors.New("invalid smoke report schema")
	errInvalidSmokeReportDocument = errors.New("invalid smoke report document")
	errExternalSchemaReference    = errors.New("external smoke report schema references are not allowed")
	arrayIndexPattern             = regexp.MustCompile(`^[0-9]+$`)
	safeSmokeReportPathTokens     = map[string]struct{}{
		"schema_version": {}, "run_id": {}, "marker": {}, "profile": {}, "scenario": {}, "started_at": {}, "finished_at": {}, "status": {}, "request_id": {}, "ai_trace_id": {}, "versions": {}, "checks": {}, "privacy_evidence": {}, "cleanup": {},
		"backend": {}, "duration_ms": {}, "failure_stage": {}, "error_class": {}, "evidence": {}, "surface": {}, "evidence_method": {}, "attempted": {}, "scanner_policy_version": {}, "counts": {}, "synthetic_canary": {}, "credential": {}, "token": {}, "recognized_pii": {}, "runtime_config_digest_verified": {}, "prequeue_artifact_hash_verified": {}, "component_identity_verified": {}, "export_admission_correlated": {}, "residual_resources": {}, "temporary_credentials": {}, "temporary_data": {},
		// 这些是 schema 明确禁止且测试需要定位的低敏字段名；绝不输出它们的值。
		"authorization": {}, "raw_payload": {}, "temporary_credential_value": {},
	}
)

// SmokeReportSchemaValidator validates the version-controlled report contract entirely in
// process. It deliberately owns a compiled schema only: callers cannot supply a URL that could
// turn smoke validation into an unexpected network request.
type SmokeReportSchemaValidator struct {
	schema *jsonschema.Schema
}

// NewSmokeReportSchemaValidator compiles one local draft-2020-12 schema. The preflight rejects
// non-local references before the library sees them, while the deny-all loader is a second line of
// defence against future schema changes accidentally reintroducing remote resolution.
func NewSmokeReportSchemaValidator(schemaJSON []byte) (*SmokeReportSchemaValidator, error) {
	if !isBoundedSmokeJSON(schemaJSON, maximumSmokeReportSchemaBytes) {
		return nil, errInvalidSmokeReportSchema
	}
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, errInvalidSmokeReportSchema
	}
	if err := validateLocalSchemaReferences(schemaDocument); err != nil {
		return nil, err
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(denyExternalSchemaLoader{})
	if err := compiler.AddResource(smokeReportSchemaURI, schemaDocument); err != nil {
		return nil, errInvalidSmokeReportSchema
	}
	schema, err := compiler.Compile(smokeReportSchemaURI)
	if err != nil {
		return nil, errInvalidSmokeReportSchema
	}
	return &SmokeReportSchemaValidator{schema: schema}, nil
}

// ValidateJSON returns only a JSON path and a stable category. Validation errors must not echo a
// smoke marker, raw payload, credential, or other report value because callers may log them.
func (v *SmokeReportSchemaValidator) ValidateJSON(documentJSON []byte) error {
	if v == nil || v.schema == nil {
		return errInvalidSmokeReportDocument
	}
	if !isBoundedSmokeJSON(documentJSON, maximumSmokeReportDocumentBytes) {
		return errInvalidSmokeReportDocument
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(documentJSON))
	if err != nil {
		return errInvalidSmokeReportDocument
	}
	if err := v.schema.Validate(document); err != nil {
		var validationError *jsonschema.ValidationError
		if !errors.As(err, &validationError) {
			return errInvalidSmokeReportDocument
		}
		return fmt.Errorf("%w at %s", errInvalidSmokeReportDocument, validationErrorPath(validationError))
	}
	return nil
}

type denyExternalSchemaLoader struct{}

func (denyExternalSchemaLoader) Load(string) (any, error) {
	return nil, errExternalSchemaReference
}

func validateLocalSchemaReferences(value any) error {
	pending := []any{value}
	for visited := 0; len(pending) > 0; visited++ {
		if visited >= maximumSmokeSchemaNodes {
			return errInvalidSmokeReportSchema
		}
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]

		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "$schema" && child != draft2020SchemaURI {
					return errExternalSchemaReference
				}
				if key == "$ref" {
					reference, ok := child.(string)
					if !ok || !strings.HasPrefix(reference, "#") {
						return errExternalSchemaReference
					}
				}
				pending = append(pending, child)
			}
		case []any:
			pending = append(pending, typed...)
		}
	}
	return nil
}

func isBoundedSmokeJSON(document []byte, maximumBytes int) bool {
	if len(document) == 0 || len(document) > maximumBytes {
		return false
	}

	depth := 0
	inString := false
	escaped := false
	for _, character := range document {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}

		switch character {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maximumSmokeJSONDepth {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return depth == 0 && !inString
}

func validationErrorPath(validationError *jsonschema.ValidationError) string {
	leaf := firstValidationLeaf(validationError)
	path := "$"
	for _, token := range leaf.InstanceLocation {
		path = appendJSONPathToken(path, token)
	}

	// required/additionalProperties errors point at the parent object. Their relevant property
	// name is structured metadata, so take it directly instead of formatting Error(), which can
	// include arbitrary instance values.
	switch errorKind := leaf.ErrorKind.(type) {
	case *kind.Required:
		if len(errorKind.Missing) == 1 {
			path = appendJSONPathToken(path, errorKind.Missing[0])
		}
	case *kind.AdditionalProperties:
		if len(errorKind.Properties) == 1 {
			path = appendJSONPathToken(path, errorKind.Properties[0])
		}
	}
	return path
}

func appendJSONPathToken(path, token string) string {
	if arrayIndexPattern.MatchString(token) {
		return path + "[" + token + "]"
	}
	if _, allowed := safeSmokeReportPathTokens[token]; !allowed {
		return path + ".<redacted>"
	}
	return path + "." + token
}

func firstValidationLeaf(validationError *jsonschema.ValidationError) *jsonschema.ValidationError {
	if len(validationError.Causes) == 0 {
		return validationError
	}
	for _, cause := range validationError.Causes {
		if cause != nil {
			return firstValidationLeaf(cause)
		}
	}
	return validationError
}
