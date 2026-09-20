package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
)

func TestMountedConnectorHostExecutesOriginalPayloadWithoutApprovals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execution fixture")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bin, "wecom-cli")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("wecom", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, strict := range []bool{false, true} {
		for _, level := range []string{contracts.AccessLevelDefault, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
			ctx := bashExecutionContext(t.TempDir())
			ctx.Session.AccessLevel = level
			ctx.Session.ConnectorDirs = map[string]string{"wecom": root}
			ctx.Session.ConnectorBinDirs = []string{bin}
			ctx.Session.ConnectorCLIEntries = entries
			executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{ShellFeaturesEnabled: !strict}}}
			payload := "{\n\"content\":\"中午12点会议 /proc/self/environ rm -rf /　\"\n}"
			command := "wecom-cli message aibot send --json '" + payload + "'"
			result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command, "cwd": "/"}, ctx)
			if err != nil || result.Error != "" || result.ExitCode != 0 || !strings.Contains(result.Output, payload) {
				t.Fatalf("strict=%t level=%s: %+v %v", strict, level, result, err)
			}
			metadata, _ := result.Structured["accessPolicy"].(map[string]any)
			if metadata["decision"] != "allow" || metadata["approvalSource"] != nil {
				t.Fatalf("not direct execution: %+v", metadata)
			}
		}
	}
}

func TestMountedConnectorHostRechecksChangedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execution fixture")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bin, "wecom-cli")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf original"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("wecom", root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.ConnectorDirs = map[string]string{"wecom": root}
	ctx.Session.ConnectorBinDirs = []string{bin}
	ctx.Session.ConnectorCLIEntries = entries
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true}}}
	args := map[string]any{"command": "wecom-cli send"}
	if p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy); !p.ConnectorOnly {
		t.Fatalf("preflight failed: %+v", p)
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf changed"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" {
		t.Fatalf("changed entry executed: %+v %v", result, err)
	}
}

func TestHostOnlyResolvesCredentialsForInvokedConnector(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execution fixture")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "demo-cli"), []byte("#!/bin/sh\nprintf '%s' \"$DEMO_PRIVATE_KEY\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("demo", root)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo"}, StateRoot: stateRoot}
	stateDir, err := pkg.ConnectorStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "credentials.json"), []byte(`{"KEY":"scoped-test-private-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.AccessLevel = contracts.AccessLevelFullAccess
	ctx.Session.ConnectorDirs = map[string]string{"demo": root}
	ctx.Session.ConnectorBinDirs = []string{bin}
	ctx.Session.ConnectorCLIEntries = entries
	ctx.Session.ConnectorCredentials = []connector.CredentialEnvironment{
		{ID: "demo", Root: stateRoot, Mode: connector.AuthToken, Env: map[string]string{"DEMO_PRIVATE_KEY": "${KEY}"}},
		{ID: "unrelated", Root: stateRoot, Mode: connector.AuthDelegated},
	}
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true}}}
	invoke := func(command string) contracts.ToolExecutionResult {
		t.Helper()
		result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, ctx)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := invoke("echo ordinary"); result.Error != "" || result.ExitCode != 0 || !strings.Contains(result.Output, "ordinary") {
		t.Fatal("disabled mounted connector blocked ordinary shell", result)
	}
	if result := invoke("demo-cli read"); result.Error == "" {
		t.Fatal("disabled connector executed", result)
	}
	yes := true
	if _, err := pkg.UpdateConnection(&yes, &yes); err != nil {
		t.Fatal(err)
	}
	if result := invoke("demo-cli read"); result.Error != "" || result.ExitCode != 0 || !strings.Contains(result.Output, "scoped-test-private-key") {
		t.Fatal("unrelated disabled connector blocked selected CLI", result)
	}
	if result := invoke("printenv DEMO_PRIVATE_KEY"); result.Error != "" || strings.Contains(result.Output, "scoped-test-private-key") {
		t.Fatal("ordinary shell received unrelated connector secret", result)
	}
	no := false
	if _, err := pkg.UpdateConnection(nil, &no); err != nil {
		t.Fatal(err)
	}
	if result := invoke("demo-cli read"); result.Error == "" {
		t.Fatal("old Run bypassed disabled connection", result)
	}
	if result := invoke("echo after-disable"); result.Error != "" || result.ExitCode != 0 {
		t.Fatal("disable poisoned ordinary shell", result)
	}
}
