// Package cmd 是应用命令入口层。
//
// 这里完成 HTTP server 的装配：中间件、路由分组、版本前缀(/v1)以及
// 各 controller 的注册。AI 能力由 pkg/ai 提供，应用层只做"胶水"。
package cmd

import (
	"context"

	"github.com/jazzash/ashjazz-aiagent/internal/controller/health"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcmd"
)

var (
	// Main 是默认主命令。未来可扩展为多命令（gcmd.CommandWithOpts），
	// 例如 worker 子命令消费消息队列做异步 Agent 任务。
	Main = gcmd.Command{
		Name:  "ashjazz-aiagent",
		Usage: "ashjazz-aiagent",
		Brief: "生产级 AI Agent 框架（GoFrame v2）",
		Func: func(ctx context.Context, parser *gcmd.Parser) (err error) {
			s := g.Server()

			s.Group("/api", func(group *ghttp.RouterGroup) {
				// MiddlewareHandlerResponse 提供统一响应信封 {code, message, data}，
				// 对应全局 rules/common/patterns.md 的「API 响应格式」约定。
				group.Middleware(ghttp.MiddlewareHandlerResponse)

				group.Group("/v1", func(v1 *ghttp.RouterGroup) {
					// 新增 controller 在此注册。命名对应 api/<domain>/v1 目录。
					v1.Bind(health.NewV1())
				})
			})

			s.Run()
			return nil
		},
	}
)
