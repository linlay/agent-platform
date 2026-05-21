package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func TestInvokeGrepFilesWithMatchesGlob(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.go"), "package main\nfunc Alpha() {}\n")
	mustWriteFile(t, filepath.Join(root, "b.txt"), "Alpha in text\n")
	mustWriteFile(t, filepath.Join(root, "c.go"), "package main\nfunc Beta() {}\n")
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern": "Alpha",
		"glob":    "*.go",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeGrep: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected grep success, got %#v", result)
	}
	results := stringSliceResult(t, result.Structured["results"])
	if len(results) != 1 || results[0] != filepath.Join(realPath(t, root), "a.go") {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestInvokeGrepContentCountTypeAndPagination(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.go"), "package main\n// needle one\n// needle two\n")
	mustWriteFile(t, filepath.Join(root, "b.go"), "package main\n// needle three\n")
	executor := fileToolExecutor(root, false)

	content, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern":     "needle",
		"type":        "go",
		"output_mode": "content",
		"head_limit":  float64(1),
		"offset":      float64(1),
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("content grep: %v", err)
	}
	if content.Structured["truncated"] != true {
		t.Fatalf("expected truncated content, got %#v", content.Structured)
	}
	contentResults := stringSliceResult(t, content.Structured["results"])
	if len(contentResults) != 1 || !strings.Contains(contentResults[0], "a.go:2:// needle one") {
		t.Fatalf("unexpected content results: %#v", contentResults)
	}

	count, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern":     "needle",
		"type":        "go",
		"output_mode": "count",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("count grep: %v", err)
	}
	countResults := strings.Join(stringSliceResult(t, count.Structured["results"]), "\n")
	realRoot := realPath(t, root)
	if !strings.Contains(countResults, filepath.Join(realRoot, "a.go")+":2") || !strings.Contains(countResults, filepath.Join(realRoot, "b.go")+":1") {
		t.Fatalf("unexpected count results: %q", countResults)
	}
}

func TestInvokeGrepContextMultilineAndDashPattern(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "notes.txt"), "before\nneedle\n-after\nfoo\nbar\n")
	executor := fileToolExecutor(root, false)

	contextResult, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern":     "needle",
		"output_mode": "content",
		"-A":          float64(1),
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("context grep: %v", err)
	}
	if !strings.Contains(strings.Join(stringSliceResult(t, contextResult.Structured["results"]), "\n"), "-after") {
		t.Fatalf("expected context line, got %#v", contextResult.Structured)
	}

	multiline, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern":     "foo.*bar",
		"output_mode": "content",
		"multiline":   true,
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("multiline grep: %v", err)
	}
	if len(stringSliceResult(t, multiline.Structured["results"])) == 0 {
		t.Fatalf("expected multiline match, got %#v", multiline.Structured)
	}

	dash, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern":     "-after",
		"output_mode": "content",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("dash grep: %v", err)
	}
	if len(stringSliceResult(t, dash.Structured["results"])) == 0 {
		t.Fatalf("expected dash-pattern match, got %#v", dash.Structured)
	}
}

func TestInvokeGrepPathEscapeRequiresApproval(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "secret.txt"), "needle\n")
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    outside,
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeGrep: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_read_approval_required" {
		t.Fatalf("expected read approval requirement, got %#v", result.Structured)
	}
	if result.Structured["fingerprint"] == "" || result.Structured["ruleKey"] == "" {
		t.Fatalf("expected approval metadata, got %#v", result.Structured)
	}
}

func TestInvokeGrepAllowsSessionSkillsDir(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	skillsRoot := filepath.Join(t.TempDir(), "agent-a", "skills")
	skillDir := filepath.Join(skillsRoot, "schedule")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	mustWriteFile(t, filepath.Join(skillDir, "SKILL.md"), "# Schedule\n\ncalendar needle\n")
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{SkillsDir: skillsRoot},
		},
	}}

	result, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern":     "calendar needle",
		"path":        skillsRoot,
		"output_mode": "content",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeGrep: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected session skills grep success, got %#v", result)
	}
	if !strings.Contains(strings.Join(stringSliceResult(t, result.Structured["results"]), "\n"), "SKILL.md") {
		t.Fatalf("expected skill grep result, got %#v", result.Structured)
	}
	if len(execCtx.FileReadApprovals) != 0 || len(execCtx.FileReadRuleApprovals) != 0 {
		t.Fatalf("expected no read approvals consumed, exact=%#v rule=%#v", execCtx.FileReadApprovals, execCtx.FileReadRuleApprovals)
	}
}

func TestInvokeGrepConsumesReadPathApproval(t *testing.T) {
	requireRipgrep(t)
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "secret.txt"), "needle\n")
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	plan, err := filetools.BuildAccessPlan(executor.cfg.FileTools, filetools.ReadAccess, outside)
	if err != nil {
		t.Fatalf("build access plan: %v", err)
	}
	filetools.RegisterExactReadApproval(execCtx, plan.Fingerprint)

	result, err := executor.invokeGrep(context.Background(), map[string]any{
		"pattern":     "needle",
		"path":        outside,
		"output_mode": "content",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeGrep: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected approved grep, got %#v", result)
	}
	if !strings.Contains(strings.Join(stringSliceResult(t, result.Structured["results"]), "\n"), "secret.txt") {
		t.Fatalf("expected grep result, got %#v", result.Structured)
	}
}

func TestInvokeGrepRipgrepMissing(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "notes.txt"), "needle\n")
	executor := fileToolExecutor(root, false)
	t.Setenv("PATH", t.TempDir())

	result, err := executor.invokeGrep(context.Background(), map[string]any{"pattern": "needle"}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeGrep: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "grep_ripgrep_missing" {
		t.Fatalf("expected missing rg error, got %#v", result.Structured)
	}
}

func TestFindBundledRipgrep(t *testing.T) {
	binaryDir := filepath.Join(t.TempDir(), "backend")
	binDir := filepath.Join(binaryDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bundled bin dir: %v", err)
	}
	name := "rg"
	if filepath.Ext(os.Args[0]) == ".exe" {
		name = "rg.exe"
	}
	rgPath := filepath.Join(binDir, name)
	mustWriteFile(t, rgPath, "#!/bin/sh\n")
	if err := os.Chmod(rgPath, 0o755); err != nil {
		t.Fatalf("chmod bundled rg: %v", err)
	}

	got, err := findBundledRipgrep(binaryDir)
	if err != nil {
		t.Fatalf("findBundledRipgrep: %v", err)
	}
	if got != rgPath {
		t.Fatalf("expected %s, got %s", rgPath, got)
	}
}

func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func stringSliceResult(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]string)
	if ok {
		return items
	}
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("expected []string/[]any, got %#v", value)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("expected string item, got %#v", item)
		}
		out = append(out, text)
	}
	return out
}
