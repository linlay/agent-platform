package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeNeverDependsOnServerTransport(t *testing.T) {
	root := repositoryRoot(t)
	assertNoImports(t, root, []string{"internal/runtime"}, map[string]bool{
		"agent-platform/internal/server": true,
	})
}

func TestServerDoesNotOwnManagedProxyExecution(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repositoryRoot(t), "internal/server/*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(node ast.Node) bool {
			switch decl := node.(type) {
			case *ast.FuncDecl:
				switch decl.Name.Name {
				case "launchPreparedProxyRun", "runProxySSE", "runProxyInboundChannel",
					"handleProxyQuery", "handleProxyWebSocketQuery", "handleProxyQueryNonStream", "executePreparedProxyCompatibility":
					t.Errorf("Server still owns managed Proxy execution: %s in %s", decl.Name.Name, path)
				}
			case *ast.TypeSpec:
				// DTO/recorder aliases remain valid at the legacy HTTP boundary.
				if _, ownsState := decl.Type.(*ast.StructType); ownsState {
					switch decl.Name.Name {
					case "proxyEventRecorder", "proxyUsageTracker", "internalQueryCapture", "queryResponseBuffer", "queryEventCollector":
						t.Errorf("Server still owns Proxy recording state: %s in %s", decl.Name.Name, path)
					}
				}
			}
			return true
		})
	}
}

func TestDomainServicesNeverDependOnServerTransport(t *testing.T) {
	root := repositoryRoot(t)
	assertNoImports(t, root, []string{
		"internal/adminsource",
		"internal/automation",
		"internal/chatresource",
		"internal/conversation",
		"internal/project",
		"internal/runops",
	}, map[string]bool{
		"agent-platform/internal/server": true,
	})
}

func TestCoreRuntimePackagesAreTransportNeutral(t *testing.T) {
	root := repositoryRoot(t)
	assertNoImports(t, root, []string{
		"internal/runtime/query",
		"internal/runtime/runexec",
		"internal/runtime/orchestration",
		"internal/runtime/session",
	}, map[string]bool{
		"net/http":                   true,
		"agent-platform/internal/ws": true,
	})
}

func TestServerDoesNotDependOnExecutionImplementations(t *testing.T) {
	root := repositoryRoot(t)
	assertNoImports(t, root, []string{"internal/server"}, map[string]bool{
		"agent-platform/internal/llm":           true,
		"agent-platform/internal/tools":         true,
		"agent-platform/internal/agent/coder":   true,
		"agent-platform/internal/agent/kbase":   true,
		"agent-platform/internal/agent/team":    true,
		"agent-platform/internal/runtime":       true,
		"agent-platform/internal/runtime/query": true,
	})
}

func TestRuntimeCommandsDoNotDependOnExternalDTOs(t *testing.T) {
	root := repositoryRoot(t)
	assertNoImports(t, root, []string{
		"internal/runtime/types",
		"internal/runtime/session",
		"internal/runtime/reference",
		"internal/runtime/query",
		"internal/runtime/runexec",
		"internal/runtime/orchestration",
		"internal/runtime/proxy",
	}, map[string]bool{
		"agent-platform/internal/api": true,
	})
}

func assertNoImports(t *testing.T, root string, relativeDirs []string, forbidden map[string]bool) {
	t.Helper()
	for _, relativeDir := range relativeDirs {
		dir := filepath.Join(root, filepath.FromSlash(relativeDir))
		info, err := os.Stat(dir)
		if errorsIsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("architecture target is not a directory: %s", dir)
		}
		err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, spec := range file.Imports {
				value, unquoteErr := strconv.Unquote(spec.Path.Value)
				if unquoteErr != nil {
					return unquoteErr
				}
				if forbidden[value] {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s imports forbidden dependency %q", filepath.ToSlash(rel), value)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect %s: %v", relativeDir, err)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test location")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

func TestConnectorExecutionDoesNotOwnApplicationsOrChats(t *testing.T) {
	assertNoImports(t, repositoryRoot(t), []string{"internal/connectorops"}, map[string]bool{
		"agent-platform/internal/webapp":       true,
		"agent-platform/internal/server":       true,
		"agent-platform/internal/chat":         true,
		"agent-platform/internal/chatresource": true,
		"agent-platform/internal/api":          true,
	})
	assertNoImports(t, repositoryRoot(t), []string{"internal/chatresource"}, map[string]bool{
		"agent-platform/internal/connectorops": true,
	})
}

// Migration guards cover ownership, not just import direction: wrapping a
// Server method in an injected function must not recreate the old runtime seam.
func TestServerDoesNotOwnNativeQueryBusiness(t *testing.T) {
	root := repositoryRoot(t)
	forbidden := map[string]bool{
		"prepareQueryAdmissionRequest": true, "completeQueryPreparation": true,
		"BuildQuerySession": true, "startPreparedLocalRun": true,
		"localRunExecutorParams": true, "executePreparedLocalQuery": true,
		"startAwaitingContinuation": true, "startAwaitingContinuationWithAdmission": true,
		"startRunContinuation": true, "registerRecoveredAwaitingRun": true,
		"resolveSubmit": true, "resolveDeferredSubmit": true, "hydrateDeferredAwaitings": true,
	}
	entries, err := os.ReadDir(filepath.Join(root, "internal/server"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, "internal/server", entry.Name())
		tree, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && forbidden[fn.Name.Name] {
				t.Errorf("Server still owns native query business: %s in %s", fn.Name.Name, path)
			}
		}
	}
	tree, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "internal/runtime/query/service.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(tree, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Dependencies" {
			return true
		}
		fields, ok := spec.Type.(*ast.StructType)
		if !ok {
			t.Fatal("query.Dependencies must be a struct")
		}
		for _, field := range fields.Fields.List {
			if _, ok := field.Type.(*ast.FuncType); ok {
				t.Errorf("query dependencies must inject components, not Server callback functions: %v", field.Names)
			}
		}
		return false
	})
}
