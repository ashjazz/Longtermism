package agent

import (
	"context"
	"reflect"
	"testing"
)

type fakeTool struct{}

func (fakeTool) Name() string {
	return "search_docs"
}

func (fakeTool) Description() string {
	return "Search project documents when the answer requires repository context."
}

func (fakeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func (fakeTool) Invoke(ctx context.Context, args map[string]any) (string, error) {
	return "result", nil
}

func TestToLLMTool(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
	}{
		{name: "converts native tool declaration", tool: fakeTool{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToLLMTool(tt.tool)
			if got.Name != tt.tool.Name() {
				t.Fatalf("Name = %q, want %q", got.Name, tt.tool.Name())
			}
			if got.Description != tt.tool.Description() {
				t.Fatalf("Description = %q, want %q", got.Description, tt.tool.Description())
			}
			if !got.Strict {
				t.Fatal("Strict = false, want true")
			}
			if !reflect.DeepEqual(got.Parameters, tt.tool.Parameters()) {
				t.Fatalf("Parameters = %#v, want %#v", got.Parameters, tt.tool.Parameters())
			}
		})
	}
}
