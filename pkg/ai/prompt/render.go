package prompt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"text/template"
)

const renderedHashBytes = 16

// textTemplate 是已经完成语法解析、可以重复渲染的 prompt 模板。
//
// text/template.Template 在解析完成后可以被多个 goroutine 并发 Execute，只要每次
// 使用独立 Writer。因此这里在加载阶段只解析一次，请求阶段仅创建局部 buffer，
// 避免每次 LLM 调用前重复解析模板源码。
type textTemplate struct {
	version  string
	template *template.Template
}

func newTextTemplate(version, source string) (*textTemplate, error) {
	// missingkey=error 是 Prompt as Code 的关键边界。Go 模板默认会把缺失 map key
	// 渲染为 "<no value>"，这会让配置错误悄悄进入模型请求，并产生难以复现的输出。
	parsed, err := template.New("prompt").
		Option("missingkey=error").
		Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse prompt template version %q: %w", version, err)
	}

	return &textTemplate{
		version:  version,
		template: parsed,
	}, nil
}

func (t *textTemplate) Version() string {
	return t.version
}

func (t *textTemplate) Render(ctx context.Context, vars map[string]any) (Rendered, error) {
	if err := contextErr(ctx); err != nil {
		return Rendered{}, err
	}

	var output bytes.Buffer
	if err := t.template.Execute(&output, vars); err != nil {
		// Execute 失败时 buffer 可能已经包含部分内容。这里绝不返回半成品 prompt，
		// 防止调用方忽略错误后把不完整指令发送给模型。
		return Rendered{}, fmt.Errorf("render prompt template version %q: %w", t.version, err)
	}
	if err := contextErr(ctx); err != nil {
		return Rendered{}, err
	}

	content := output.String()
	return Rendered{
		Content: content,
		Version: t.version,
		Hash:    shortContentHash(content),
	}, nil
}

func shortContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))

	// 使用 SHA-256 前 16 字节，即 128 bit，并编码为 32 个十六进制字符。
	// 它比完整 64 字符更适合进入 trace 字段，同时仍保留足够的内容身份空间。
	return hex.EncodeToString(sum[:renderedHashBytes])
}
