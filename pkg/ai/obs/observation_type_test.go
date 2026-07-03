package obs

import (
	"strings"
	"testing"
)

func TestValidateObservationTypeAllowsKnownTypes(t *testing.T) {
	tests := []struct {
		name            string
		observationType ObservationType
		wantString      string
	}{
		{name: "generation", observationType: ObservationTypeGeneration, wantString: "generation"},
		{name: "retriever", observationType: ObservationTypeRetriever, wantString: "retriever"},
		{name: "tool", observationType: ObservationTypeTool, wantString: "tool"},
		{name: "agent", observationType: ObservationTypeAgent, wantString: "agent"},
		{name: "evaluator", observationType: ObservationTypeEvaluator, wantString: "evaluator"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateObservationType(tt.observationType); err != nil {
				t.Fatalf("ValidateObservationType(%q) error = %v, want nil", tt.observationType, err)
			}
			if got := tt.observationType.String(); got != tt.wantString {
				t.Fatalf("String() = %q, want %q", got, tt.wantString)
			}
		})
	}
}

func TestValidateObservationTypeRejectsUnknownTypes(t *testing.T) {
	tests := []struct {
		name            string
		observationType ObservationType
		wantInError     string
	}{
		{name: "empty", observationType: "", wantInError: "empty"},
		{name: "blank", observationType: "   ", wantInError: "empty"},
		{name: "unknown", observationType: "embedding", wantInError: "embedding"},
		{name: "case sensitive", observationType: "Generation", wantInError: "Generation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateObservationType(tt.observationType)
			if err == nil {
				t.Fatalf("ValidateObservationType(%q) error = nil, want rejection", tt.observationType)
			}
			if !strings.Contains(err.Error(), tt.wantInError) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.wantInError)
			}
		})
	}
}
