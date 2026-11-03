//go:build !windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

// The Windows Node adapter must not change how Unix starts a connector entry:
// the entry runs itself through its shebang, whatever its file extension.
func TestUnixDirectConnectorEntryRunsThroughItsOwnShebang(t *testing.T) {
	packageRoot := filepath.Join(t.TempDir(), "ru-connectors", "demo", "digest")
	bin := filepath.Join(packageRoot, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tool.js"), []byte("#!/bin/sh\nprintf 'shell:%s' \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("demo", packageRoot)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.ConnectorDirs = map[string]string{"demo": packageRoot}
	ctx.Session.ConnectorBinDirs = []string{bin}
	ctx.Session.ConnectorCLIEntries = entries
	// Direct entry execution is independent of shell availability and grammar.
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true, ShellExecutable: "/missing/configured-shell"}}}
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "tool.js 'a & b'"}, ctx)
	if err != nil || result.Error != "" || result.ExitCode != 0 || !strings.Contains(result.Output, "shell:a & b") {
		t.Fatalf("connector entry was not started by its own shebang: %+v %v", result, err)
	}
	metadata, _ := result.Structured["accessPolicy"].(map[string]any)
	if metadata["ruleKey"] != "bash-access:connector" {
		t.Fatalf("entry did not use its frozen connector grant: %+v", metadata)
	}
}
