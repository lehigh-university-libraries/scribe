package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The API process accepts requests and commits durable outbox/job rows. Only
// cmd/worker may consume those rows or run maintenance loops. Scan every
// non-test source file in this command so moving a dispatcher into an API
// helper cannot silently collapse that deployment boundary again.
func TestAPIProcessDoesNotStartBackgroundDispatchers(t *testing.T) {
	t.Parallel()

	forbidden := map[string]struct{}{
		"StartTranscriptionWorker":             {},
		"StartWebhookDispatcher":               {},
		"StartProviderCallAuditRetention":      {},
		"StartExternalRequestRetention":        {},
		"StartAnnotationMirrorDispatcher":      {},
		"StartResourceCleanupDispatcher":       {},
		"StartProviderSecretCleanupDispatcher": {},
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve API command source directory")
	}
	sources, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "*.go"))
	if err != nil {
		t.Fatalf("list API command sources: %v", err)
	}
	fset := token.NewFileSet()
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, source, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", source, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, prohibited := forbidden[selector.Sel.Name]; prohibited {
				t.Errorf("API command starts worker-only dispatcher %s in %s", selector.Sel.Name, source)
			}
			return true
		})
	}
}
