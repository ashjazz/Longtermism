// Package health 定义健康检查相关的对外接口契约。
//
// 接口定义（Req/Res）与实现分离：本文件只描述「是什么」，
// controller 与 logic 负责实现。这是 GoFrame 的接口优先约定。
package health

import (
	"github.com/gogf/gf/v2/frame/g"
)

// PingReq 健康探针请求，无入参。
type PingReq struct {
	g.Meta `path:"/health/ping" method:"get" tags:"Health" summary:"存活探针"`
}

// PingRes 健康探针响应。
type PingRes struct {
	App     string `json:"app"     dc:"应用名"`
	Version string `json:"version" dc:"版本号"`
	Ok      bool   `json:"ok"      dc:"是否存活"`
}
