package prompt

import (
	"context"
	"strings"
	"testing"
)

func TestTextTemplateRender(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		source      string
		vars        map[string]any
		wantContent string
	}{
		{
			name:        "渲染字符串变量并保留模板版本",
			version:     "v1",
			source:      "Question: {{ .Question }}\nContext: {{ .Context }}",
			vars:        map[string]any{"Question": "What is RAG?", "Context": "Retrieval augmented generation."},
			wantContent: "Question: What is RAG?\nContext: Retrieval augmented generation.",
		},
		{
			name:        "渲染非字符串变量",
			version:     "v2",
			source:      "Maximum steps: {{ .MaxSteps }}",
			vars:        map[string]any{"MaxSteps": 8},
			wantContent: "Maximum steps: 8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := mustNewTextTemplate(t, tt.version, tt.source)

			rendered, err := template.Render(context.Background(), tt.vars)
			if err != nil {
				t.Fatalf("Render() unexpected error: %v", err)
			}
			if rendered.Content != tt.wantContent {
				t.Errorf("Render().Content = %q, want %q", rendered.Content, tt.wantContent)
			}
			if rendered.Version != tt.version {
				t.Errorf("Render().Version = %q, want %q", rendered.Version, tt.version)
			}
			if rendered.Hash == "" {
				t.Error("Render().Hash is empty, want content identity for trace correlation")
			}
		})
	}
}

func TestTextTemplateRenderFailsWhenVariableIsMissing(t *testing.T) {
	template := mustNewTextTemplate(
		t,
		"v1",
		"Question: {{ .Question }}\nContext: {{ .Context }}",
	)

	// Prompt 变量缺失如果被静默渲染为 "<no value>"，模型仍可能给出看似合理但不可复现的回答。
	// 因此这里把 missingkey=error 固化为生产契约，让配置问题在调用模型前快速失败。
	rendered, err := template.Render(context.Background(), map[string]any{
		"Question": "What is RAG?",
	})
	if err == nil {
		t.Fatalf("Render() error = nil, rendered = %#v; want missing variable failure", rendered)
	}
	if !strings.Contains(err.Error(), "Context") {
		t.Errorf("Render() error = %q, want missing variable name Context", err)
	}
}

func TestRenderedHashUsesRenderedContent(t *testing.T) {
	firstTemplate := mustNewTextTemplate(t, "v1", "Answer: {{ .Answer }}")
	secondTemplate := mustNewTextTemplate(t, "v2", "{{ .Prefix }}{{ .Answer }}")

	first := mustRender(t, firstTemplate, map[string]any{"Answer": "42"})
	repeated := mustRender(t, firstTemplate, map[string]any{"Answer": "42"})
	sameContentDifferentTemplate := mustRender(t, secondTemplate, map[string]any{
		"Prefix": "Answer: ",
		"Answer": "42",
	})
	differentContent := mustRender(t, firstTemplate, map[string]any{"Answer": "43"})

	// hash 是 trace 中的内容身份：相同最终 prompt 必须稳定命中同一身份，
	// 即使它来自不同模板版本；最终内容变化时则必须产生不同身份。
	if first.Content != sameContentDifferentTemplate.Content {
		t.Fatalf("test setup produced different content: %q != %q", first.Content, sameContentDifferentTemplate.Content)
	}
	if first.Hash == "" {
		t.Fatal("Render().Hash is empty, want stable content hash")
	}
	if first.Hash != repeated.Hash {
		t.Errorf("same rendered content hashes differ: %q != %q", first.Hash, repeated.Hash)
	}
	if first.Hash != sameContentDifferentTemplate.Hash {
		t.Errorf("same content from different templates hashes differ: %q != %q", first.Hash, sameContentDifferentTemplate.Hash)
	}
	if first.Hash == differentContent.Hash {
		t.Errorf("different rendered content shares hash %q", first.Hash)
	}
}

func mustNewTextTemplate(t *testing.T, version, source string) Template {
	t.Helper()

	template, err := newTextTemplate(version, source)
	if err != nil {
		t.Fatalf("newTextTemplate() unexpected error: %v", err)
	}
	return template
}

func mustRender(t *testing.T, template Template, vars map[string]any) Rendered {
	t.Helper()

	rendered, err := template.Render(context.Background(), vars)
	if err != nil {
		t.Fatalf("Render() unexpected error: %v", err)
	}
	return rendered
}
