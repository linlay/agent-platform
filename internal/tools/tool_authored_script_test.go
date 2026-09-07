package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestFileToolsAuthoredScriptExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix real interpreter test")
	}
	dir := t.TempDir()
	executor := fileToolExecutor(dir, true)
	executor.cfg.Bash = config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true, ShellExecutable: "bash"}
	ctx := fileToolExecutionContext(dir)
	ctx.Session.AgentKey = "ordinary"
	ctx.Session.RunID = "run"
	path := filepath.Join(dir, "task.sh")
	written, err := executor.invokeWrite(context.Background(), map[string]any{"file_path": path, "content": "#!/bin/sh\nprintf 'authored\\n'\nexit 7\n"}, ctx)
	if err != nil || written.Error != "" {
		t.Fatalf("write: %+v %v", written, err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"bash task.sh", "sh task.sh", "./task.sh", "'" + path + "'"} {
		result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, ctx)
		if err != nil || result.ExitCode != 7 || result.Structured["stdout"] != "authored\n" {
			t.Fatalf("%s: %+v %v", command, result, err)
		}
		metadata, _ := result.Structured["accessPolicy"].(map[string]any)
		if metadata["decision"] != "allow" || metadata["ruleKey"] != "bash-access:authored-script" {
			t.Fatalf("wrong provenance audit: %#v", metadata)
		}
	}
	edited, err := executor.invokeEdit(context.Background(), map[string]any{"file_path": path, "old_string": "authored", "new_string": "edited"}, ctx)
	if err != nil || edited.Error != "" {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	result, _ := executor.invokeHostBash(context.Background(), map[string]any{"command": "sh task.sh"}, ctx)
	if result.Structured["stdout"] != "edited\n" || result.ExitCode != 7 {
		t.Fatalf("authored edit: %+v", result)
	}
	if err := os.WriteFile(path, []byte("echo external"), 0700); err != nil {
		t.Fatal(err)
	}
	result, _ = executor.invokeHostBash(context.Background(), map[string]any{"command": "sh task.sh"}, ctx)
	if result.Error != "bash_access_approval_required" {
		t.Fatalf("external change: %+v", result)
	}
}

func TestFileEditExternalAndFailedWriteDoNotCreateProof(t *testing.T) {
	dir := t.TempDir()
	executor := fileToolExecutor(dir, true)
	ctx := fileToolExecutionContext(dir)
	path := filepath.Join(dir, "foreign.sh")
	if err := os.WriteFile(path, []byte("echo external"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, _ := executor.invokeRead(map[string]any{"file_path": path}, ctx); result.Error != "" {
		t.Fatal(result)
	}
	if result, _ := executor.invokeEdit(context.Background(), map[string]any{"file_path": path, "old_string": "external", "new_string": "edited"}, ctx); result.Error != "" {
		t.Fatal(result)
	}
	if ctx.AuthoredScripts.Matches(ctx.ScriptOwner(), path) {
		t.Fatal("local edit of foreign script created proof")
	}
	result, _ := executor.invokeWrite(context.Background(), map[string]any{"file_path": filepath.Join(dir, "failed.sh"), "content": strings.Repeat("x", 2048)}, ctx)
	if result.Error == "" {
		t.Fatal("oversized write unexpectedly succeeded")
	}
	if ctx.AuthoredScripts.Matches(ctx.ScriptOwner(), filepath.Join(dir, "failed.sh")) {
		t.Fatal("failed write created proof")
	}
}

func TestAuthoredProofUsesEncodedBytes(t *testing.T) {
	dir := t.TempDir()
	executor := fileToolExecutor(dir, true)
	ctx := fileToolExecutionContext(dir)
	p := filepath.Join(dir, "encoded.sh")
	result, _ := executor.invokeWrite(context.Background(), map[string]any{"file_path": p, "content": "echo 中文", "encoding": "gb18030"}, ctx)
	if result.Error != "" {
		t.Fatal(result)
	}
	if !ctx.AuthoredScripts.Matches(ctx.ScriptOwner(), p) {
		t.Fatal("proof used source UTF-8 rather than written gb18030 bytes")
	}
}
