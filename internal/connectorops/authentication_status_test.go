package connectorops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestAuthorizationStatusCLIHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "connector-status-helper" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "version":
		fmt.Println("1.0.0")
	case "failed":
		// A shell reports a program it cannot run as 127: not a sign-out.
		os.Exit(127)
	case "signed-out":
		fmt.Println(`{"authenticated":false}`)
	case "authorized":
		fmt.Println(`{"authenticated":true}`)
	default:
		os.Exit(99)
	}
	os.Exit(0)
}

func TestInvocationSeparatesCLIStatusFailureFromConfirmedSignOut(t *testing.T) {
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		mode   string
		status string
		code   string
		http   int
		// unprepared removes the installation after an authorized check.
		unprepared bool
	}{
		{"failed", "failed", "connector_unavailable", 503, false},
		{"signed-out", "unauthorized", "connector_auth_required", 401, false},
		{"authorized", "authorized", "connector_preparation_required", 409, true},
	} {
		name := test.mode
		if test.unprepared {
			name = "unprepared"
		}
		t.Run(name, func(t *testing.T) {
			sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
			dir := filepath.Join(sources.ExternalRoot, "demo")
			if err := os.MkdirAll(filepath.Join(dir, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			entry := "status-helper"
			if runtime.GOOS == "windows" {
				entry += ".exe"
			}
			if err := os.WriteFile(filepath.Join(dir, "bin", entry), binary, 0700); err != nil {
				t.Fatal(err)
			}
			command := func(mode string) map[string]any {
				line := fmt.Sprintf("%s -test.run=^TestAuthorizationStatusCLIHelper$ -- connector-status-helper %s", entry, mode)
				return map[string]any{"darwin": line, "linux": line, "win32": line}
			}
			files := map[string]any{
				"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "cli", "authMode": "self"},
				"cli.json": map[string]any{
					"versionCheck":    map[string]any{"command": command("version"), "minVersion": "1.0.0"},
					"auth":            command("authorized"),
					"status":          command(test.mode),
					"unAuth":          command("authorized"),
					"statusMatchJson": map[string]any{"authenticated": true},
					"authUrlDomain":   "example.test",
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
			m := connectorauth.New(t.Context(), sources, nil)
			if _, err := m.Prepare(t.Context(), "demo"); err != nil {
				t.Fatal(err)
			}
			pkg, err := sources.Load("demo")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pkg.SetConfigured(true); err != nil {
				t.Fatal(err)
			}
			checked, err := m.Check(t.Context(), "demo", "")
			if err != nil || checked.Status != test.status {
				t.Fatal("unexpected status probe outcome", checked, err)
			}
			if test.unprepared {
				if err := os.RemoveAll(filepath.Join(filepath.Dir(sources.ExternalRoot), "connector-installs", "demo")); err != nil {
					t.Fatal(err)
				}
			}
			service := Service{Auth: m, Sources: sources}
			scope := Scope{Subject: "owner", IdempotencyNamespace: "fixture", Execution: []Permission{{ConnectorID: "demo", Adapter: "cli"}}, Check: func() error { return nil }}
			_, err = service.Invoke(t.Context(), scope, Request{ConnectorID: "demo", Adapter: "cli", Args: []string{"business"}})
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != test.code || failure.Status != test.http {
				t.Fatal("transient failure was treated as sign-out", err)
			}
			state, err := pkg.ReadConnection()
			if err != nil || !state.Configured {
				t.Fatal("status failure changed binding preference", state, err)
			}
		})
	}
}
