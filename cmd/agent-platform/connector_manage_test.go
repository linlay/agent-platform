package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestConnectorManagementDerivesStateRootAndMigratesLegacyCredentials(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "runtime-default", true: "custom-state"}[custom], func(t *testing.T) {
			runtimeRoot := t.TempDir()
			t.Setenv("AP_RUNTIME_STATE_DIR", "")
			center := filepath.Join(runtimeRoot, "connectors-center")
			state := filepath.Join(runtimeRoot, ".state")
			old := filepath.Join(runtimeRoot, "connector-state")
			args := []string{"status", "--runtime-dir", runtimeRoot, "--id", "demo"}
			if custom {
				state = filepath.Join(t.TempDir(), "state")
				t.Setenv("AP_RUNTIME_STATE_DIR", state)
			}
			for path, data := range map[string]string{
				filepath.Join(center, "demo", "connector.json"): `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`,
				filepath.Join(center, "demo", "cli.json"):       `{}`,
				filepath.Join(old, ".credentials", "demo.json"): `{"TOKEN":"test-credential"}`,
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := runConnectorManagement(args, &out); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out.Bytes(), []byte("test-credential")) {
				t.Fatal("status exposed credential")
			}
			path := filepath.Join(state, "connectors", "demo", "credentials.json")
			if data, err := os.ReadFile(path); err != nil || string(data) != `{"TOKEN":"test-credential"}` {
				t.Fatal("state did not use the selected Platform root", err)
			}
			if _, err := os.Lstat(old); !os.IsNotExist(err) {
				t.Fatal("old state retained")
			}

		})
	}
}

func TestConnectorManagementRejectsDirectoryOverrideFlags(t *testing.T) {
	for _, flag := range []string{"--state-dir", "--connectors-center-dir", "--connectors-dir", "--connector-state-dir"} {
		var out bytes.Buffer
		if err := runConnectorManagement([]string{"status", "--runtime-dir", t.TempDir(), flag, t.TempDir(), "--id", "demo"}, &out); err == nil {
			t.Fatalf("removed directory override accepted: %s", flag)
		}
	}
}
