package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
)

// Registry 管理 Agent 可调用工具的本地注册表。
//
// Agent executor 后续会同时面对两条路径：
//   - 运行时按 tool name 找到本地 Tool 并执行 Invoke。
//   - 调用 LLM 前把工具声明转成 llm.Tool 发送给 provider。
//
// 因此 Registry 既是执行路由表，也是工具契约快照。注册时必须复制 schema，读取时也必须
// 返回副本，避免调用方复用 map 后悄悄改掉线上 tool contract。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]registeredTool
}

// NewRegistry 创建空工具注册中心。
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]registeredTool),
	}
}

// Register 注册一个工具。
//
// P2 先要求 schema 至少是 JSON Schema object 且包含 properties。更细的 required、
// additionalProperties、字段类型校验可以在 schema validator 接入后加强；当前边界足以防止
// “没有工具输入契约也能注册成功”这种最危险的 Agent 失控入口。
func (r *Registry) Register(tool Tool) error {
	name, parameters, err := validateToolRegistration(tool)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]registeredTool)
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("agent tool %q already registered", name)
	}

	r.tools[name] = registeredTool{
		tool:       tool,
		name:       name,
		parameters: cloneSchema(parameters),
	}
	return nil
}

// Get 按名称查找工具。
//
// 返回值会保留原始 Invoke 行为，但 Parameters 返回注册时冻结的 schema 副本，防止执行器或测试
// 通过 Tool 接口绕过 Registry 修改内部声明。
func (r *Registry) Get(name string) (Tool, error) {
	normalizedName := strings.TrimSpace(name)

	r.mu.RLock()
	tool, ok := r.tools[normalizedName]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", normalizedName)
	}
	return tool.clone(), nil
}

// LLMTools 返回可发送给模型供应商的工具声明。
//
// 为了让测试、trace 和模型请求稳定可复现，这里按工具名排序输出。map 本身无序，若直接遍历，
// 同一批工具在不同运行中可能产生不同请求体，排查模型行为时会多一层噪声。
func (r *Registry) LLMTools() []llm.Tool {
	r.mu.RLock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	tools := make([]llm.Tool, 0, len(names))
	for _, name := range names {
		tool := r.tools[name]
		tools = append(tools, llm.Tool{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  cloneSchema(tool.parameters),
			Strict:      true,
		})
	}
	r.mu.RUnlock()
	return tools
}

func validateToolRegistration(tool Tool) (string, map[string]any, error) {
	if tool == nil {
		return "", nil, fmt.Errorf("agent tool is required")
	}

	name := strings.TrimSpace(tool.Name())
	if name == "" {
		return "", nil, fmt.Errorf("agent tool name is required")
	}

	parameters := tool.Parameters()
	if parameters == nil {
		return "", nil, fmt.Errorf("agent tool %q parameters schema is required", name)
	}
	if parameters["type"] != "object" {
		return "", nil, fmt.Errorf("agent tool %q parameters schema must be object", name)
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok || properties == nil {
		return "", nil, fmt.Errorf("agent tool %q parameters schema properties are required", name)
	}
	return name, parameters, nil
}

type registeredTool struct {
	tool       Tool
	name       string
	parameters map[string]any
}

func (t registeredTool) clone() registeredTool {
	return registeredTool{
		tool:       t.tool,
		name:       t.name,
		parameters: cloneSchema(t.parameters),
	}
}

func (t registeredTool) Name() string {
	return t.name
}

func (t registeredTool) Description() string {
	return t.tool.Description()
}

func (t registeredTool) Parameters() map[string]any {
	return cloneSchema(t.parameters)
}

func (t registeredTool) Invoke(ctx context.Context, args map[string]any) (string, error) {
	return t.tool.Invoke(ctx, args)
}

func cloneSchema(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}

	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneSchemaValue(value)
	}
	return cloned
}

func cloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSchema(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneSchemaValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
