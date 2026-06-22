// Package health 实现健康检查 controller。
//
// controller 是「薄层」：只做参数→logic 的转发与响应映射，
// 不写业务逻辑（遵循全局 rules/web/patterns.md 的 Container/Presentational 分离）。
package health

// ControllerV1 实现 health 接口（各 *_v1_*.go 文件提供方法）。
type ControllerV1 struct{}

// NewV1 构造 controller。未来依赖 logic 时在此注入。
func NewV1() *ControllerV1 {
	return &ControllerV1{}
}
