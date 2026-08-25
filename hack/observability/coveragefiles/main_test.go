package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileHasExecutableStatements(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "interface and value declarations only", body: "package sample\n\ntype Boundary interface { Value() int }\nvar Name string\n", want: false},
		{name: "empty function has no coverage block", body: "package sample\n\nfunc Empty() {}\n", want: false},
		{name: "function statement is executable", body: "package sample\n\nfunc Value() int { return 1 }\n", want: true},
		{name: "package function literal is executable", body: "package sample\n\nvar Value = func() int { return 1 }\n", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.go")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write source fixture: %v", err)
			}
			got, err := fileHasExecutableStatements(path)
			if err != nil {
				t.Fatalf("fileHasExecutableStatements() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("fileHasExecutableStatements() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileHasExecutableStatementsRejectsInvalidGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.go")
	if err := os.WriteFile(path, []byte("package"), 0o600); err != nil {
		t.Fatalf("write invalid fixture: %v", err)
	}
	if _, err := fileHasExecutableStatements(path); err == nil {
		t.Fatal("fileHasExecutableStatements() error = nil, want parser failure")
	}
}
