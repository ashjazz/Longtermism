package prompt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	// ErrTemplateNotFound 表示 name/version 对应的模板资产不存在。
	//
	// 调用方可以通过 errors.Is 分类处理，例如启动检查可以快速失败，而未来的远端
	// Registry adapter 也可以保持相同语义，不需要依赖底层 os.PathError。
	ErrTemplateNotFound = errors.New("prompt template not found")

	// ErrUnsafeTemplatePath 表示 name/version 可能让文件访问逃逸 Registry 根目录。
	//
	// 这是输入校验错误而不是“模板不存在”：如果把二者混为一类，路径攻击会被普通
	// fallback 掩盖，既不利于审计，也可能让后续实现错误地尝试其他存储后端。
	ErrUnsafeTemplatePath = errors.New("unsafe prompt template path")
)

// FilesystemRegistry 从固定根目录读取版本化 prompt 模板。
//
// Registry 只负责安全定位和读取文件；模板解析、变量校验与 hash 计算属于渲染层，
// 由后续 render.go 实现。分开这两个职责后，未来替换为远端 Prompt 平台时，上层
// 仍然只依赖 Registry 和 Template 契约。
type FilesystemRegistry struct {
	root string
}

// NewFilesystemRegistry 创建文件系统 Registry。
//
// 构造函数不访问磁盘，便于应用在装配阶段先建立依赖；根目录不存在、权限不足等
// 环境问题会在 Get 时带上下文返回。root 本身只来自服务端配置，不能由请求参数控制。
func NewFilesystemRegistry(root string) *FilesystemRegistry {
	return &FilesystemRegistry{root: root}
}

// Get 按 <name>/<version>.tmpl 加载模板。
func (r *FilesystemRegistry) Get(ctx context.Context, name, version string) (Template, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}

	relativePath, err := templateRelativePath(name, version)
	if err != nil {
		return nil, err
	}

	source, err := readTemplateFile(r.root, relativePath)
	if err != nil {
		if errors.Is(err, ErrUnsafeTemplatePath) {
			return nil, fmt.Errorf("%w: name=%q version=%q", ErrUnsafeTemplatePath, name, version)
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: name=%q version=%q", ErrTemplateNotFound, name, version)
		}
		return nil, fmt.Errorf("load prompt template %q version %q: %w", name, version, err)
	}

	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	return newTextTemplate(version, string(source))
}

func templateRelativePath(name, version string) (string, error) {
	if !isTemplatePathSegment(name) || !isTemplatePathSegment(version) {
		return "", fmt.Errorf("%w: name=%q version=%q", ErrUnsafeTemplatePath, name, version)
	}

	// 先按约定拼出路径，再 Clean 并用 IsLocal 复核。前面的单段校验拒绝 ".."、
	// 绝对路径和嵌套路径；这里的复核为未来路径规则演进保留第二道边界。
	relativePath := filepath.Clean(filepath.Join(name, version+".tmpl"))
	if !filepath.IsLocal(relativePath) {
		return "", fmt.Errorf("%w: name=%q version=%q", ErrUnsafeTemplatePath, name, version)
	}
	return relativePath, nil
}

func isTemplatePathSegment(value string) bool {
	return value != "" &&
		value != "." &&
		value != ".." &&
		filepath.IsLocal(value) &&
		filepath.Base(value) == value
}

func readTemplateFile(rootPath, relativePath string) ([]byte, error) {
	// os.Root 不仅限制 ".." 和绝对路径，也会阻止根目录内的符号链接指向外部。
	// 相比“先 EvalSymlinks 再 ReadFile”，它把校验和打开文件绑定在同一个受限根上，
	// 减少检查后路径被替换所造成的 TOCTOU 风险。
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open prompt root: %w", err)
	}

	// Prompt 资产目录不需要 symlink 能力。逐段拒绝符号链接比尝试判断它最终是否仍在
	// root 内更保守，也让不同操作系统上的“路径逃逸”错误都映射为同一领域错误。
	if err := rejectSymlinkComponents(root, relativePath); err != nil {
		return nil, errors.Join(err, root.Close())
	}

	file, openErr := root.Open(relativePath)
	if openErr != nil {
		return nil, errors.Join(openErr, root.Close())
	}

	content, readErr := io.ReadAll(file)
	fileCloseErr := file.Close()
	rootCloseErr := root.Close()
	if err := errors.Join(readErr, fileCloseErr, rootCloseErr); err != nil {
		return nil, err
	}
	return content, nil
}

func rejectSymlinkComponents(root *os.Root, relativePath string) error {
	currentPath := ""
	for _, segment := range splitPath(relativePath) {
		currentPath = filepath.Join(currentPath, segment)
		info, err := root.Lstat(currentPath)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafeTemplatePath
		}
	}
	return nil
}

func splitPath(path string) []string {
	var segments []string
	for path != "." && path != "" {
		dir, file := filepath.Split(path)
		if file != "" {
			segments = append([]string{file}, segments...)
		}
		path = filepath.Clean(dir)
	}
	return segments
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
