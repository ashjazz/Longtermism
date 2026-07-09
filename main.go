// 程序入口。
//
// 仅负责装配命令行入口并交给 gctx 执行；真正的 HTTP server、路由、
// 依赖装配都在 internal/cmd 中完成，便于未来拆分多命令（server/worker/evaluator）。
package main

import (
	"github.com/ashjazz/Longtermism/internal/cmd"

	"github.com/gogf/gf/v2/os/gctx"
)

func main() {
	cmd.Main.Run(gctx.GetInitCtx())
}
