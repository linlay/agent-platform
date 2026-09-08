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

func TestConnectorManageTokenFileDoesNotEchoCredentials(t *testing.T) {
	runtimeRoot := t.TempDir()
	t.Setenv("AP_RUNTIME_STATE_DIR", "")
	dir := filepath.Join(runtimeRoot, "connectors-center", "demo")
	os.MkdirAll(dir, 0755)
	files := map[string]string{
		filepath.Join(dir, "connector.json"):     `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"token","token_schema":{"fields":[{"key":"API_KEY","required":true}]}}`,
		filepath.Join(dir, "mcp.json"):           `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp","headers":{"X-API-Key":"${API_KEY}"}}}}`,
		filepath.Join(t.TempDir(), "input.json"): `{"API_KEY":"private-token"}`,
	}
	input := ""
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(path) == "input.json" {
			input = path
		}
	}
	args := []string{"set-token", "--runtime-dir", runtimeRoot, "--id", "demo", "--credentials-file", input}
	var out bytes.Buffer
	if err := runConnectorManagement(args, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("private-token")) || !bytes.Contains(out.Bytes(), []byte(`"status":"authorized"`)) {
		t.Fatal("unsafe token response", out.String())
	}
	out.Reset()
	if err := runConnectorManagement([]string{"logout", "--runtime-dir", runtimeRoot, "--id", "demo"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, ".state", "connectors", "demo", "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("logout retained credential")
	}
}

func TestConnectorManageOneIDUsesIdentityFileAndNeverStoresToken(t *testing.T) {
	runtimeRoot := t.TempDir()
	t.Setenv("AP_RUNTIME_STATE_DIR", "")
	t.Setenv("AP_ACCESS_TOKEN", "untrusted-process-token")
	dir := filepath.Join(runtimeRoot, "connectors-center", "demo")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "connector.json"), []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"oneid-token"}`), 0644)
	os.WriteFile(filepath.Join(dir, "cli.json"), []byte(`{}`), 0644)
	explicit := filepath.Join(t.TempDir(), "sso-access-token.txt")
	os.WriteFile(explicit, []byte("test-private-sso"), 0600)
	for _, tc := range []struct{ file, status string }{{"", "unauthorized"}, {explicit, "authorized"}} {
		args := []string{"status", "--runtime-dir", runtimeRoot, "--id", "demo"}
		if tc.file != "" {
			args = append(args, "--identity-file", tc.file)
		}
		var out bytes.Buffer
		if err := runConnectorManagement(args, &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out.Bytes(), []byte(`"status":"`+tc.status+`"`)) || bytes.Contains(out.Bytes(), []byte("test-private-sso")) {
			t.Fatal("wrong identity status", out.String())
		}
	}
	var out bytes.Buffer
	if err := runConnectorManagement([]string{"status", "--runtime-dir", runtimeRoot, "--id", "demo", "--identity-file", "relative.txt"}, &out); err == nil {
		t.Fatal("relative identity file accepted")
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, ".state", "connectors", "demo")); !os.IsNotExist(err) {
		t.Fatal("SSO created a connector credential directory")
	}
}
