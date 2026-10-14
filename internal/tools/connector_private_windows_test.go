//go:build windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestMountedPrivateWindowsNPMConnectorUsesFrozenNodeArgv(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".state")
	installation := filepath.Join(state, "connectors", "wecom", "installations", "1.3.3", "digest")
	bin := filepath.Join(installation, "bin")
	packageRoot := filepath.Join(root, "ru-connectors", "wecom", "digest")
	script := filepath.Join(bin, "node_modules", "@wecom", "cli", "bin", "wecom.js")
	for _, dir := range []string{bin, packageRoot, filepath.Dir(script)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(bin, "node.exe"), script} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "wecom-cli"), []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(bin, "wecom-cli.cmd")
	if err := os.WriteFile(shim, []byte(`@"%_prog%" "%~dp0\node_modules\@wecom\cli\bin\wecom.js" %*`), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntriesFromBin("wecom", installation, bin)
	if err != nil {
		t.Fatal(err)
	}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.ConnectorDirs = map[string]string{"wecom": packageRoot}
	ctx.Session.ConnectorBinDirs = []string{bin}
	ctx.Session.ConnectorCLIEntries = entries
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{StateDir: state}}}
	env, err := executor.commandEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vars := bashEnvironmentVariables(env)
	command := `wecom-cli --json '{"text":"literal %PATH% & | $(touch nope) 中文"}'`
	p := executor.ReviewBashAccess(context.Background(), map[string]any{"command": command}, ctx, config.AccessPolicyConfig{})
	if !p.ConnectorOnly || !p.Allowed() {
		t.Fatalf("private Windows npm entry did not match: %+v", p)
	}
	direct := accesspolicy.DirectConnectorInvocation(executor.policySession(ctx), command, ctx.Session.WorkspaceRoot, vars)
	if direct == nil || !stringsEqualPath(direct.Program, shim) {
		t.Fatalf("POSIX sibling selected instead of npm cmd shim: %+v", direct)
	}
	cmd, err := connectorauth.ExecutionProgramCommand(context.Background(), direct.Program, direct.Args, ctx.Session.WorkspaceRoot, env)
	canonicalScript, _ := filepath.EvalSymlinks(script)
	want := append([]string{filepath.Join(bin, "node.exe"), canonicalScript}, direct.Args...)
	if err != nil || !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Windows connector argv was interpreted by a shell: %v %v", cmd, err)
	}
	if err := os.WriteFile(script, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if p := executor.ReviewBashAccess(context.Background(), map[string]any{"command": command}, ctx, config.AccessPolicyConfig{}); p.HasConnector || !p.Blocked() {
		t.Fatalf("unchanged cmd shim hid a changed script: %+v", p)
	}
}

func stringsEqualPath(a, b string) bool {
	a, ea := filepath.EvalSymlinks(a)
	b, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && filepath.Clean(a) == filepath.Clean(b)
}
