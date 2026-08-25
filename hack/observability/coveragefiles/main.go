// coveragefiles 输出真正包含可执行函数体语句的 Go 文件。
//
// Go coverprofile 按设计省略只有 interface/type/const/var 声明的文件。覆盖率门禁必须
// 区分这种“零可执行分母”和“生产函数所在包没有进入 profile”，否则会制造假失败；
// 同时也不能用文件名 allowlist 跳过真实函数。
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
)

func main() {
	for _, path := range os.Args[1:] {
		if path == "--" {
			continue
		}
		hasStatements, err := fileHasExecutableStatements(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "coveragefiles: Go source cannot be inspected")
			os.Exit(2)
		}
		if hasStatements {
			fmt.Println(path)
		}
	}
}

func fileHasExecutableStatements(path string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return false, err
	}

	hasStatements := false
	ast.Inspect(file, func(node ast.Node) bool {
		if hasStatements {
			return false
		}
		switch function := node.(type) {
		case *ast.FuncDecl:
			hasStatements = function.Body != nil && len(function.Body.List) > 0
		case *ast.FuncLit:
			hasStatements = function.Body != nil && len(function.Body.List) > 0
		}
		return !hasStatements
	})
	return hasStatements, nil
}
