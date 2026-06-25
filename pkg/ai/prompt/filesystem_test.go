package prompt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemRegistryGet(t *testing.T) {
	root := t.TempDir()
	writePromptTemplate(t, root, "p0_smoke", "v1", "Answer: {{ .Question }}")

	tests := []struct {
		name            string
		templateName    string
		version         string
		wantVersion     string
		wantErr         error
		wantErrContains []string
	}{
		{
			name:         "正常加载已存在的模板版本",
			templateName: "p0_smoke",
			version:      "v1",
			wantVersion:  "v1",
		},
		{
			name:            "模板目录不存在时返回可分类错误",
			templateName:    "missing_prompt",
			version:         "v1",
			wantErr:         ErrTemplateNotFound,
			wantErrContains: []string{"missing_prompt", "v1"},
		},
		{
			name:            "模板存在但版本不存在时返回可分类错误",
			templateName:    "p0_smoke",
			version:         "v2",
			wantErr:         ErrTemplateNotFound,
			wantErrContains: []string{"p0_smoke", "v2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewFilesystemRegistry(root)

			template, err := registry.Get(context.Background(), tt.templateName, tt.version)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Get() error = %v, want errors.Is(_, %v)", err, tt.wantErr)
				}
				for _, fragment := range tt.wantErrContains {
					if !strings.Contains(err.Error(), fragment) {
						t.Errorf("Get() error = %q, want it to contain %q", err, fragment)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("Get() unexpected error: %v", err)
			}
			if template == nil {
				t.Fatal("Get() template = nil, want loaded template")
			}
			if got := template.Version(); got != tt.wantVersion {
				t.Errorf("Template.Version() = %q, want %q", got, tt.wantVersion)
			}
		})
	}
}

func TestFilesystemRegistryRejectsPathTraversal(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "prompts")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create prompt root: %v", err)
	}

	// 在 registry 根目录之外放置一个真实文件，确保测试验证的是“主动拒绝越界”，
	// 而不是越界后碰巧因为目标文件不存在而返回普通 not found。
	writePromptTemplate(t, parent, "outside", "v1", "sensitive content")

	tests := []struct {
		name         string
		templateName string
		version      string
	}{
		{
			name:         "模板名称不能逃逸根目录",
			templateName: "../outside",
			version:      "v1",
		},
		{
			name:         "版本不能通过父目录片段逃逸根目录",
			templateName: "safe",
			version:      "../../outside/v1",
		},
		{
			name:         "模板名称不能使用绝对路径",
			templateName: filepath.Join(parent, "outside"),
			version:      "v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewFilesystemRegistry(root)

			template, err := registry.Get(context.Background(), tt.templateName, tt.version)
			if template != nil {
				t.Errorf("Get() template = %#v, want nil for unsafe path", template)
			}
			if !errors.Is(err, ErrUnsafeTemplatePath) {
				t.Fatalf("Get() error = %v, want errors.Is(_, ErrUnsafeTemplatePath)", err)
			}
		})
	}
}

func TestFilesystemRegistryRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "prompts")
	outside := filepath.Join(parent, "outside")
	writePromptTemplate(t, outside, "secret", "v1", "sensitive content")

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create prompt root: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	// filepath.Clean/Join 只能证明字符串路径仍在 root 下，无法识别 root/linked
	// 实际指向外部目录；这个用例要求实现同时防御文件系统层面的 symlink 逃逸。
	registry := NewFilesystemRegistry(root)
	template, err := registry.Get(context.Background(), "linked", "v1")
	if template != nil {
		t.Errorf("Get() template = %#v, want nil for symlink escape", template)
	}
	if !errors.Is(err, ErrUnsafeTemplatePath) {
		t.Fatalf("Get() error = %v, want errors.Is(_, ErrUnsafeTemplatePath)", err)
	}
}

func TestP0SmokePromptAsset(t *testing.T) {
	// Go 会以当前 package 目录运行测试，因此这里从 pkg/ai/prompt 回到仓库根目录，
	// 直接验证真实 prompt 资产。这样模板变量改名、版本文件误删等问题会在本地门禁暴露。
	promptRoot := filepath.Join("..", "..", "..", "resource", "prompt")
	registry := NewFilesystemRegistry(promptRoot)

	template, err := registry.Get(context.Background(), "p0_smoke", "v1")
	if err != nil {
		t.Fatalf("Get() p0_smoke/v1 unexpected error: %v", err)
	}

	rendered, err := template.Render(context.Background(), map[string]any{
		"Question": "什么是 RAG？",
		"Context":  "RAG 会先检索相关上下文，再让模型基于上下文生成回答。",
	})
	if err != nil {
		t.Fatalf("Render() p0_smoke/v1 unexpected error: %v", err)
	}
	if rendered.Version != "v1" {
		t.Errorf("Render().Version = %q, want v1", rendered.Version)
	}
	if rendered.Hash == "" {
		t.Error("Render().Hash is empty, want traceable prompt content identity")
	}
	for _, fragment := range []string{
		"什么是 RAG？",
		"RAG 会先检索相关上下文，再让模型基于上下文生成回答。",
	} {
		if !strings.Contains(rendered.Content, fragment) {
			t.Errorf("Render().Content = %q, want it to contain %q", rendered.Content, fragment)
		}
	}
	if strings.Contains(rendered.Content, "{{") {
		t.Errorf("Render().Content = %q, want all template actions evaluated", rendered.Content)
	}
}

func writePromptTemplate(t *testing.T, root, name, version, content string) {
	t.Helper()

	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create template directory: %v", err)
	}
	path := filepath.Join(dir, version+".tmpl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write template file: %v", err)
	}
}
