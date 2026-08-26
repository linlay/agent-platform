package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func TestInvokeGlobWithoutPathRequiresWorkspace(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).invokeGlob(context.Background(), map[string]any{
		"pattern": "*.go",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "workspace_unavailable" || result.ExitCode == 0 {
		t.Fatalf("expected workspace_unavailable, got %#v", result)
	}
	if !strings.Contains(result.Output, "pass path explicitly") || !strings.Contains(result.Output, "@chat") {
		t.Fatalf("expected actionable workspace_unavailable output, got %#v", result)
	}
}

func TestInvokeGlobRejectsRootQualifiedPatternBeforePathResolution(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).invokeGlob(context.Background(), map[string]any{
		"pattern": "/Users/u/models/*.yml",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "glob_invalid_pattern" || !strings.Contains(result.Output, "must be relative to path") {
		t.Fatalf("expected actionable rooted-pattern rejection, got %#v", result)
	}
}

func TestInvokeGlobBlocksFilesystemRootSearch(t *testing.T) {
	root := hostFilesystemRootForTest(t)
	executor := fileToolExecutor(root, false)
	execCtx := fileToolExecutionContext(root)
	cases := []struct {
		name string
		args map[string]any
	}{
		{name: "default workspace", args: map[string]any{"pattern": "*.yml"}},
		{name: "explicit root", args: map[string]any{"pattern": "*.yml", "path": root}},
		{name: "workspace alias", args: map[string]any{"pattern": "*.yml", "path": "@workspace"}},
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "root-link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatalf("symlink root: %v", err)
		}
		cases = append(cases, struct {
			name string
			args map[string]any
		}{name: "symlink to root", args: map[string]any{"pattern": "*.yml", "path": link}})
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, err := executor.invokeGlob(context.Background(), test.args, execCtx)
			if err != nil {
				t.Fatalf("invokeGlob: %v", err)
			}
			if result.Error != "glob_root_scan_blocked" || result.Structured["path"] != root {
				t.Fatalf("expected root search rejection, got %#v", result)
			}
		})
	}
}

func TestInvokeGlobAllowsNarrowAbsolutePathFromRootWorkspace(t *testing.T) {
	requireRipgrep(t)
	searchRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(searchRoot, "model.yml"), "key: model\n")
	root := hostFilesystemRootForTest(t)
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.yml",
		"path":    searchRoot,
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected narrow absolute search success, got %#v", result)
	}
}

func TestInvokeGlobReportsRipgrepStderrAndPartialResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script ripgrep fixture is Unix-only")
	}
	root := t.TempDir()
	binDir := t.TempDir()
	rgPath := filepath.Join(binDir, "rg")
	mustWriteFile(t, rgPath, "#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = \"--no-messages\" ]; then\n    printf '%s\\n' 'messages unexpectedly hidden' >&2\n    exit 9\n  fi\ndone\nprintf '%s\\n' 'partial.yml'\nprintf '%s\\n' 'permission denied' >&2\nexit 2\n")
	if err := os.Chmod(rgPath, 0o755); err != nil {
		t.Fatalf("chmod fake rg: %v", err)
	}
	t.Setenv("AP_BUILTINS_BIN", binDir)
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.yml",
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "glob_failed" || result.Structured["rgExitCode"] != 2 {
		t.Fatalf("expected rg failure diagnostics, got %#v", result)
	}
	if !strings.Contains(result.Structured["stderr"].(string), "permission denied") {
		t.Fatalf("expected visible rg stderr, got %#v", result.Structured)
	}
	if got := stringSliceResult(t, result.Structured["partialResults"]); len(got) != 1 || got[0] != "partial.yml" {
		t.Fatalf("expected partial result, got %#v", result.Structured)
	}
}

func TestInvokeGlobWithoutWorkspaceAllowsExplicitChatPath(t *testing.T) {
	requireRipgrep(t)
	chatDir := t.TempDir()
	mustWriteFile(t, filepath.Join(chatDir, "upload.xlsx"), "fixture\n")
	executor := fileToolExecutor(t.TempDir(), false)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{ChatDir: chatDir},
		},
	}}

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.xlsx",
		"path":    "@chat",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected explicit Chat glob success, got %#v", result)
	}
	results := stringSliceResult(t, result.Structured["results"])
	if len(results) != 1 || results[0] != filepath.Join(realPath(t, chatDir), "upload.xlsx") {
		t.Fatalf("unexpected explicit Chat glob results: %#v", results)
	}
}

func TestInvokeGlobMatchesHiddenNestedAndExcludesVCS(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "nested"),
		filepath.Join(root, ".hidden"),
		filepath.Join(root, ".git"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}
	}
	mustWriteFile(t, filepath.Join(root, "a.go"), "package main\n")
	mustWriteFile(t, filepath.Join(root, "b.txt"), "text\n")
	mustWriteFile(t, filepath.Join(root, "nested", "c.go"), "package nested\n")
	mustWriteFile(t, filepath.Join(root, ".hidden", "secret.go"), "package hidden\n")
	mustWriteFile(t, filepath.Join(root, ".git", "ignored.go"), "package ignored\n")
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.go",
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected glob success, got %#v", result)
	}

	realRoot := realPath(t, root)
	results := stringSliceResult(t, result.Structured["results"])
	want := map[string]bool{
		filepath.Join(realRoot, "a.go"):                 false,
		filepath.Join(realRoot, "nested", "c.go"):       false,
		filepath.Join(realRoot, ".hidden", "secret.go"): false,
	}
	for _, path := range results {
		if _, ok := want[path]; ok {
			want[path] = true
		}
		if path == filepath.Join(realRoot, "b.txt") || path == filepath.Join(realRoot, ".git", "ignored.go") {
			t.Fatalf("unexpected glob result %s in %#v", path, results)
		}
	}
	for path, seen := range want {
		if !seen {
			t.Fatalf("expected glob result %s in %#v", path, results)
		}
	}
	if got, ok := result.Structured["matchCount"].(int); !ok || got != len(want) {
		t.Fatalf("expected matchCount=%d, got %#v", len(want), result.Structured["matchCount"])
	}
}

func TestInvokeGlobPaginationUsesModifiedTimeOrder(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "a.go"),
		filepath.Join(root, "b.go"),
		filepath.Join(root, "c.go"),
	}
	base := time.Unix(1_700_000_000, 0)
	for idx, path := range paths {
		mustWriteFile(t, path, "package main\n")
		modTime := base.Add(time.Duration(idx) * time.Second)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes fixture: %v", err)
		}
	}
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern":    "*.go",
		"head_limit": 1,
		"offset":     1,
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected glob success, got %#v", result)
	}
	results := stringSliceResult(t, result.Structured["results"])
	want := filepath.Join(realPath(t, root), "b.go")
	if len(results) != 1 || results[0] != want {
		t.Fatalf("expected paged result %s, got %#v", want, results)
	}
	if result.Structured["truncated"] != true {
		t.Fatalf("expected truncated result, got %#v", result.Structured)
	}
	if got, ok := result.Structured["matchCount"].(int); !ok || got != 3 {
		t.Fatalf("expected matchCount=3, got %#v", result.Structured["matchCount"])
	}
}

func TestInvokeGlobNoMatchReturnsEmptySuccess(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.go"), "package main\n")
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.md",
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected empty glob success, got %#v", result)
	}
	if got, ok := result.Structured["matchCount"].(int); !ok || got != 0 {
		t.Fatalf("expected matchCount=0, got %#v", result.Structured["matchCount"])
	}
	if results := stringSliceResult(t, result.Structured["results"]); len(results) != 0 {
		t.Fatalf("expected empty results, got %#v", results)
	}
}

func TestInvokeGlobRejectsInvalidPattern(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "[",
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "glob_invalid_pattern" {
		t.Fatalf("expected invalid glob pattern error, got %#v", result.Structured)
	}
}

func TestInvokeGlobPathEscapeRequiresAndConsumesApproval(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "secret.go"), "package secret\n")
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.go",
		"path":    outside,
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_read_approval_required" {
		t.Fatalf("expected read approval requirement, got %#v", result.Structured)
	}

	execCtx := fileToolExecutionContext(root)
	plan := fileToolAccessPlan(t, executor, filetools.ReadAccess, outside)
	filetools.RegisterExactReadApproval(execCtx, plan.Fingerprint)
	approved, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.go",
		"path":    outside,
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke approved glob: %v", err)
	}
	if approved.Error != "" || approved.ExitCode != 0 {
		t.Fatalf("expected approved glob success, got %#v", approved)
	}
	results := stringSliceResult(t, approved.Structured["results"])
	want := filepath.Join(realPath(t, outside), "secret.go")
	if len(results) != 1 || results[0] != want {
		t.Fatalf("expected approved glob result %s, got %#v", want, results)
	}
}

func TestInvokeGlobRejectsMissingAndNonDirectoryPath(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	filePath := filepath.Join(root, "notes.txt")
	mustWriteFile(t, filePath, "notes\n")
	executor := fileToolExecutor(root, false)

	nonDirectory, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.txt",
		"path":    filePath,
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob non-directory: %v", err)
	}
	if nonDirectory.ExitCode == 0 || nonDirectory.Structured["error"] != "glob_invalid_path" {
		t.Fatalf("expected non-directory path error, got %#v", nonDirectory.Structured)
	}

	missing, err := executor.invokeGlob(context.Background(), map[string]any{
		"pattern": "*.txt",
		"path":    filepath.Join(root, "missing"),
	}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeGlob missing: %v", err)
	}
	if missing.ExitCode == 0 || missing.Structured["error"] != "glob_invalid_path" {
		t.Fatalf("expected missing path error, got %#v", missing.Structured)
	}
}
