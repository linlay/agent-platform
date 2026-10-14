package accesspolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
)

func privateConnectorFixture(t *testing.T) (QuerySession, map[string]string, string, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, ".state")
	installation := filepath.Join(state, "connectors", "wecom", "installations", "1.3.3", "digest")
	bin := filepath.Join(installation, "bin")
	packageRoot := filepath.Join(root, "ru-connectors", "wecom", "digest")
	for _, dir := range []string{bin, packageRoot} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	entry := filepath.Join(bin, "wecom-cli.cmd")
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(entry, []byte("@echo off\r\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "wecom-cli"), []byte("#!/bin/sh\n"), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		// npm places a relative symlink in bin; its actual JS entry is in lib.
		script := filepath.Join(installation, "lib", "node_modules", "@wecom", "cli", "bin", "wecom.js")
		if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("#!/usr/bin/env node\nconsole.log('fixture');\n"), 0700); err != nil {
			t.Fatal(err)
		}
		entry = filepath.Join(bin, "wecom-cli")
		rel, _ := filepath.Rel(bin, script)
		if err := os.Symlink(rel, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := connector.SnapshotCLIEntriesFromBin("wecom", installation, bin)
	if err != nil || len(entries) != 1 {
		t.Fatalf("freeze private installation: %v %+v", err, entries)
	}
	session := QuerySession{
		AgentKey: "mounted", WorkspaceRoot: t.TempDir(), ProtectedPaths: []string{state},
		ConnectorDirs: map[string]string{"wecom": packageRoot}, ConnectorBinDirs: []string{bin}, ConnectorCLIEntries: entries,
	}
	vars := map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	return session, vars, entry, state
}

func TestMountedPrivateConnectorExecutesWithoutOpeningState(t *testing.T) {
	session, vars, entry, state := privateConnectorFixture(t)
	command := "wecom-cli --version"
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		session.AccessLevel = level
		p := ReviewBashCommand(config.AccessPolicyConfig{}, session, command, session.WorkspaceRoot, vars)
		if p.Decision != DecisionAllow || !p.ConnectorOnly || DirectConnectorInvocation(session, command, session.WorkspaceRoot, vars) == nil {
			t.Fatalf("private entry rejected at %s: %+v", level, p)
		}
		for _, path := range []string{entry, filepath.Dir(entry), filepath.Join(state, "connectors", "wecom", "credentials.json")} {
			for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
				p, err := BuildPathPlan(config.AccessPolicyConfig{}, session, mode, path)
				if err != nil || !p.Blocked() {
					t.Fatalf("private state access granted at %s: %s %s %+v %v", level, mode, path, p, err)
				}
			}
		}
		for _, surrounding := range []string{
			"cat '" + entry + "'",
			command + " && ls '" + filepath.Dir(entry) + "'",
			command + " > '" + filepath.Join(state, "output") + "'",
		} {
			if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, surrounding, session.WorkspaceRoot, vars); !p.Blocked() {
				t.Fatalf("surrounding shell received entry grant at %s: %s %+v", level, surrounding, p)
			}
		}
	}
}

func TestMountedPrivateConnectorRejectsChangedOrUnmountedEntry(t *testing.T) {
	session, vars, _, _ := privateConnectorFixture(t)
	command := "wecom-cli --version"
	unmounted := session
	unmounted.ConnectorDirs = nil
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, unmounted, command, session.WorkspaceRoot, vars); p.HasConnector || !p.Blocked() {
		t.Fatalf("unmounted entry trusted: %+v", p)
	}
	entry := session.ConnectorCLIEntries[0]
	if err := os.WriteFile(entry.Path, []byte("changed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, command, session.WorkspaceRoot, vars); p.HasConnector || !p.Blocked() {
		t.Fatalf("changed private entry trusted: %+v", p)
	}
}

func TestPrivateHostInstallationDoesNotImpersonateContainerPackage(t *testing.T) {
	session, vars, _, _ := privateConnectorFixture(t)
	session.AgentHasRuntimeSandbox = true
	entry := session.ConnectorCLIEntries[0]
	guest := "/connectors/wecom/" + entry.RelativePath
	env := &BashEnvironment{
		Directory: func(string) (string, error) { return "/workspace", nil },
		Resolve:   func(string, string, map[string]string) (string, error) { return guest, nil },
		Canonical: func(string, string) (string, error) { return guest, nil },
		Inspect:   func(string) (string, string, error) { return "#!/bin/sh\n", entry.SHA256, nil },
	}
	if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, session, "wecom-cli --version", session.WorkspaceRoot, vars, env, nil); p.HasConnector {
		t.Fatalf("host installation granted a different container mount: %+v", p)
	}
}
