// Package prompt 实现「Prompt as Code」（准备清单 §9.1）。
//
// 核心原则：prompt 不硬编码在业务代码里，而是模板化、版本化、可追溯、可回滚。
// 每次渲染产出带 hash 的完整 prompt，写入 trace（§8），用于分析「哪个版本表现更好」。
//
// 后续演进路径（§9.1 三层次）：Git 管理 → 配置中心热更新 → 专门平台（LangFuse/Braintrust）。
package prompt

import "context"

// Template 是一个可渲染的 prompt 模板。
type Template interface {
	// Version 返回模板版本号，用于 trace 与 A/B（§9.1）。
	Version() string
	// Render 用 vars 渲染最终 prompt 字符串。
	Render(ctx context.Context, vars map[string]any) (Rendered, error)
}

// Rendered 是渲染结果，携带内容与可用于追踪的 hash。
type Rendered struct {
	Content string // 渲染后的完整 prompt
	Version string // 模板版本
	Hash    string // Content 的短 hash，写入 trace
}

// Registry 按 name+version 解析模板。实现可从文件系统、配置中心或远端平台加载。
type Registry interface {
	Get(ctx context.Context, name, version string) (Template, error)
}
