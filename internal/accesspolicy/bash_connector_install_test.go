//go:build !windows

package accesspolicy

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
)

func TestInstalledConnectorEntryIsBoundToItsInstallationAndMount(t *testing.T) {
	prefix := t.TempDir()
	script := filepath.Join(prefix, "lib", "node_modules", "demo-cli", "cli.js")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	// npm links global commands into prefix/bin.
	if err := os.Symlink("../lib/node_modules/demo-cli/cli.js", filepath.Join(bin, "demo-cli")); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotInstalledCLIEntries("demo", prefix)
	if err != nil || len(entries) != 1 || entries[0].Root == "" {
		t.Fatal("installed entry not frozen", entries, err)
	}
	vars := map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	session := QuerySession{AgentKey: "mounted", AccessLevel: AccessLevelDefault, WorkspaceRoot: t.TempDir(),
		ConnectorDirs: map[string]string{"demo": t.TempDir()}, ConnectorInstallBinDirs: []string{bin}, ConnectorCLIEntries: entries}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, "demo-cli docs list", "/", vars); p.Decision != DecisionAllow || !p.ConnectorOnly {
		t.Fatalf("installed CLI not authorized: %+v", p)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, "demo-cli docs list | head -1", "/", vars); len(p.ConnectorIDs) != 1 || p.ConnectorIDs[0] != "demo" {
		t.Fatalf("composite command did not report its connector: %+v", p.ConnectorIDs)
	}
	// An entry is valid only while its connector stays mounted.
	session.ConnectorDirs = map[string]string{}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, "demo-cli docs list", "/", vars); p.ConnectorOnly {
		t.Fatal("unmounted connector kept its installed entry")
	}
	session.ConnectorDirs = map[string]string{"demo": t.TempDir()}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf changed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, "demo-cli docs list", "/", vars); p.ConnectorOnly {
		t.Fatal("changed installed program kept its authorization")
	}
}
