package connectorauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/connector"
)

func TestTokenCandidateReusesPreparedPrivateCLIWithIsolatedHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installed CLI fixture; Windows lifecycle adapter is tested separately")
	}
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "candidate")
	installer := filepath.Join(dir, "installer", "candidate-cli")
	if err := os.MkdirAll(filepath.Dir(installer), 0700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '1.2.0\\n'; exit 0; fi\ntest \"$API_KEY\" = \"candidate-secret\" || exit 72\nprintf checked > \"$HOME/checked\"\nprintf '{\"authenticated\":true}\\n'\n"
	if err := os.WriteFile(installer, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]any{
		"connector.json": map[string]any{
			"id": "candidate", "name": "Candidate", "version": "1.0.0", "type": "cli", "auth_mode": "token",
			"token_schema": map[string]any{"fields": []any{map[string]any{"key": "API_KEY", "label": "API Key", "type": "password", "required": true}}},
		},
		"cli.json": map[string]any{
			"init":            osCommands("cp ./installer/candidate-cli \"$CONNECTOR_BIN_DIR/candidate-cli\" && chmod 755 \"$CONNECTOR_BIN_DIR/candidate-cli\""),
			"versionCheck":    map[string]any{"command": osCommands("candidate-cli --version"), "minVersion": "1.2.0"},
			"env":             map[string]any{"API_KEY": "${API_KEY}"},
			"status":          osCommands("candidate-cli status"),
			"statusMatchJson": map[string]any{"authenticated": true},
		},
	}
	for name, value := range files {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(t.Context(), sources, nil)
	if _, err := m.Prepare(t.Context(), "candidate"); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("candidate")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := CLIInstallationBinDir(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bin, "candidate-cli")); err != nil {
		t.Fatal("private CLI was not prepared", err)
	}
	result, err := m.SetToken(t.Context(), "candidate", map[string]string{"API_KEY": "candidate-secret"})
	if err != nil || result.Status != "authorized" {
		t.Fatal("candidate could not use prepared installation", result, err)
	}
	private, err := CLIPrivateEnvironmentDirs(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(private["HOME"], "checked")); !os.IsNotExist(err) {
		t.Fatal("candidate validator touched active connector HOME", err)
	}
	if _, err := os.Stat(filepath.Join(bin, "candidate-cli")); err != nil {
		t.Fatal("temporary credential cleanup removed shared installation", err)
	}
}
