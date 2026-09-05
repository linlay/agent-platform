package architecture

import (
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
