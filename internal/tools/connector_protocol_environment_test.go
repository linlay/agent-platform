package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestProtocolConnectorCredentialsReachOnlyItsVerifiedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX child fixture; Windows launch is covered in connectorauth")
	}
	root := t.TempDir()
	state := filepath.Join(root, "state")
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.ConnectorDirs = map[string]string{}
	for _, id := range []string{"first", "second"} {
		dir := filepath.Join(root, "packages", id)
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(bin, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := map[string]any{"id": id, "name": id, "version": "1.0.0", "type": "cli", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "USER", "label": "User", "type": "text", "required": true}, {"key": "PASSWORD", "label": "Password", "type": "password", "required": true}}}}
		cli := map[string]any{"versionCheck": map[string]any{"command": map[string]string{"darwin": id + " --version", "linux": id + " --version", "win32": id + ".exe --version"}, "minVersion": "1.0.0"}, "env": map[string]string{"USER_NAME": "${USER}", "SERVICE_PASSWORD": "${PASSWORD}"}, "staticEnv": map[string]string{"SERVICE_URL": "https://" + id + ".invalid"}}
		for name, v := range map[string]any{"connector.json": manifest, "cli.json": cli} {
			b, _ := json.Marshal(v)
			if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		script := "#!/bin/sh\nset -eu\ntest \"$USER_NAME\" = '" + id + "-user'\ntest \"$SERVICE_PASSWORD\" = '" + id + "-password'\ntest \"$SERVICE_URL\" = 'https://" + id + ".invalid'\ntest \"$HOME\" = '" + filepath.Join(state, id, "home") + "'\ntest -n \"$CONNECTOR_BIN_DIR\"\nprintf 'verified-" + id + "'\n"
		entry := filepath.Join(bin, id)
		if err := os.WriteFile(entry, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		pkg, err := connector.LoadDirectory(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		pkg.StateRoot = state
		if _, err := pkg.SetConfigured(true); err != nil {
			t.Fatal(err)
		}
		creds, _ := json.Marshal(map[string]string{"USER": id + "-user", "PASSWORD": id + "-password"})
		if err := os.WriteFile(filepath.Join(state, id, "credentials.json"), creds, 0600); err != nil {
			t.Fatal(err)
		}
		binding, err := connectorauth.CLIEnvironment(pkg)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := connector.SnapshotCLIEntries(id, dir)
		if err != nil {
			t.Fatal(err)
		}
		ctx.Session.ConnectorDirs[id] = dir
		ctx.Session.ConnectorBinDirs = append(ctx.Session.ConnectorBinDirs, bin)
		ctx.Session.ConnectorCredentials = append(ctx.Session.ConnectorCredentials, binding)
		ctx.Session.ConnectorCLIEntries = append(ctx.Session.ConnectorCLIEntries, entries...)
	}
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{ShellFeaturesEnabled: true}}}
	for _, id := range []string{"first", "second"} {
		result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": strconv.Quote(filepath.Join(ctx.Session.ConnectorDirs[id], "bin", id)) + " query"}, ctx)
		if err != nil || result.Error != "" || result.ExitCode != 0 || !strings.Contains(result.Output, "verified-"+id) {
			t.Fatalf("child %s: %+v %v", id, result, err)
		}
	}
	base, err := executor.commandEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range base {
		if strings.HasPrefix(v, "SERVICE_PASSWORD=") || strings.HasPrefix(v, "SERVICE_URL=") {
			t.Fatal("connector configuration leaked to ordinary shell")
		}
	}
}
