package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
)

func TestMountedPrivateNPMConnectorHostExecutesVerifiedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX npm symlink fixture; Windows uses its Node cmd shim fixture")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node required for the npm entry fixture")
	}
	root := t.TempDir()
	state := filepath.Join(root, ".state")
	installation := filepath.Join(state, "connectors", "wecom", "installations", "1.3.3", "digest")
	bin := filepath.Join(installation, "bin")
	packageRoot := filepath.Join(root, "ru-connectors", "wecom", "digest")
	script := filepath.Join(installation, "lib", "node_modules", "@wecom", "cli", "bin", "wecom.js")
	for _, dir := range []string{bin, packageRoot, filepath.Dir(script)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(script, []byte("#!/usr/bin/env node\nconsole.log(JSON.stringify(process.argv.slice(2)));\n"), 0700); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(bin, script)
	if err := os.Symlink(rel, filepath.Join(bin, "wecom-cli")); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntriesFromBin("wecom", installation, bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{contracts.AccessLevelDefault, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		ctx := bashExecutionContext(t.TempDir())
		ctx.Session.AccessLevel = level
		ctx.Session.ConnectorDirs = map[string]string{"wecom": packageRoot}
		ctx.Session.ConnectorBinDirs = []string{bin}
		ctx.Session.ConnectorCLIEntries = entries
		executor := &RuntimeToolExecutor{cfg: config.Config{
			Paths: config.PathsConfig{StateDir: state},
			Bash:  config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true},
		}}
		payload := `{"text":"literal %PATH% & | $(touch nope) 中文"}`
		result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "wecom-cli --json '" + payload + "'"}, ctx)
		if err != nil || result.Error != "" || result.ExitCode != 0 || !strings.Contains(result.Output, "literal %PATH% & | $(touch nope) 中文") {
			t.Fatalf("private npm entry failed at %s: %+v %v", level, result, err)
		}
		metadata, _ := result.Structured["accessPolicy"].(map[string]any)
		if metadata["decision"] != "allow" || metadata["ruleKey"] != "bash-access:connector" {
			t.Fatalf("entry did not use its frozen connector grant: %+v", metadata)
		}
		blocked, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat '" + script + "'"}, ctx)
		if err != nil || blocked.Error != "bash_access_blocked" {
			t.Fatalf("ordinary shell read entered private installation at %s: %+v %v", level, blocked, err)
		}
	}
}
