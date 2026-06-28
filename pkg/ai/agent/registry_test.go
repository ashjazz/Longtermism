package agent

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRegistryRegistersAndLooksUpTools(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	tool := newRegistryTool("search_docs", validToolSchema())

	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got, err := registry.Get("search_docs")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name() != tool.Name() {
		t.Fatalf("Get() tool name = %q, want %q", got.Name(), tool.Name())
	}

	llmTools := registry.LLMTools()
	if len(llmTools) != 1 {
		t.Fatalf("LLMTools() length = %d, want 1", len(llmTools))
	}
	if llmTools[0].Name != "search_docs" {
		t.Fatalf("LLMTools()[0].Name = %q, want search_docs", llmTools[0].Name)
	}
	if !llmTools[0].Strict {
		t.Fatal("LLMTools()[0].Strict = false, want true")
	}
	if !reflect.DeepEqual(llmTools[0].Parameters, validToolSchema()) {
		t.Fatalf("LLMTools()[0].Parameters = %#v, want valid schema", llmTools[0].Parameters)
	}
}

func TestRegistryRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tool    Tool
		wantErr string
	}{
		{
			name:    "error nil tool 有明确错误",
			tool:    nil,
			wantErr: "tool is required",
		},
		{
			name:    "error 空工具名有明确错误",
			tool:    newRegistryTool(" ", validToolSchema()),
			wantErr: "name is required",
		},
		{
			name:    "error 缺少 schema 有明确错误",
			tool:    newRegistryTool("search_docs", nil),
			wantErr: "parameters schema",
		},
		{
			name: "error schema 不是 object 有明确错误",
			tool: newRegistryTool("search_docs", map[string]any{
				"type": "array",
			}),
			wantErr: "object",
		},
		{
			name: "error 缺少 properties 有明确错误",
			tool: newRegistryTool("search_docs", map[string]any{
				"type": "object",
			}),
			wantErr: "properties",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewRegistry().Register(tt.tool)

			assertRegistryError(t, err, tt.wantErr)
		})
	}
}

func TestRegistryRejectsDuplicateToolName(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	if err := registry.Register(newRegistryTool("search_docs", validToolSchema())); err != nil {
		t.Fatalf("Register() first tool error = %v", err)
	}

	err := registry.Register(newRegistryTool("search_docs", validToolSchema()))

	assertRegistryError(t, err, "already registered")
}

func TestRegistryRejectsUnknownToolLookup(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry().Get("missing_tool")

	assertRegistryError(t, err, "unknown tool")
	assertRegistryError(t, err, "missing_tool")
}

func TestRegistryDoesNotExposeMutableSchemaState(t *testing.T) {
	t.Parallel()

	// 工具 schema 会被发送给模型供应商。注册后必须冻结住当时的声明，否则调用方复用
	// map 时可能悄悄改变线上 tool contract，导致 trace 和模型行为难以复现。
	schema := validToolSchema()
	registry := NewRegistry()
	if err := registry.Register(newRegistryTool("search_docs", schema)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	schema["type"] = "mutated"

	got := registry.LLMTools()
	if got[0].Parameters["type"] != "object" {
		t.Fatalf("LLMTools()[0].Parameters[type] = %#v, want object", got[0].Parameters["type"])
	}

	got[0].Parameters["type"] = "mutated again"
	gotAgain := registry.LLMTools()
	if gotAgain[0].Parameters["type"] != "object" {
		t.Fatalf("second LLMTools()[0].Parameters[type] = %#v, want object", gotAgain[0].Parameters["type"])
	}
}

func TestRegistrySupportsConcurrentReads(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	if err := registry.Register(newRegistryTool("search_docs", validToolSchema())); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	var wg sync.WaitGroup
	for index := 0; index < 20; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if _, err := registry.Get("search_docs"); err != nil {
				t.Errorf("Get() error = %v", err)
			}
			tools := registry.LLMTools()
			if len(tools) != 1 {
				t.Errorf("LLMTools() length = %d, want 1", len(tools))
			}
		}()
	}
	wg.Wait()
}

type registryTool struct {
	name       string
	parameters map[string]any
}

func newRegistryTool(name string, parameters map[string]any) registryTool {
	return registryTool{name: name, parameters: parameters}
}

func (t registryTool) Name() string {
	return t.name
}

func (registryTool) Description() string {
	return "Search project documents when the answer requires repository context."
}

func (t registryTool) Parameters() map[string]any {
	return t.parameters
}

func (registryTool) Invoke(ctx context.Context, args map[string]any) (string, error) {
	return "result", nil
}

func validToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func assertRegistryError(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("error = nil, want containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}
