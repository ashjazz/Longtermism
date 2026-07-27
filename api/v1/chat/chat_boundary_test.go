package chat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestChatAPIPackageContainsOnlyDTODeclarations protects the GoFrame boundary: api/v1 owns
// request/response shapes and route metadata, while controller owns HTTP parsing and projection.
func TestChatAPIPackageContainsOnlyDTODeclarations(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(sourceFile), "chat.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse chat API source: %v", err)
	}

	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			t.Fatalf("api/v1/chat must not define behavior function %q", function.Name.Name)
		}
	}
}
