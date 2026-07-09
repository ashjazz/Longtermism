// Package consts 存放全局常量、错误码、枚举映射。
package consts

// 框架级元信息。用于健康检查、可观测性标签。
const (
	AppName    = "longtermism"
	AppVersion = "0.1.0"
)

// 业务错误码段。预留位段以便后续按模块划分（见 docs/adr）。
const (
	CodeOK            = 0
	CodeInternalError = 50000
	CodeUpstreamDown  = 50300 // 上游 LLM / 向量库不可用，对应准备清单 §10 降级
)
