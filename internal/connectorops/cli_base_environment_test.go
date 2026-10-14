package connectorops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

type cliEnvironmentObservation struct {
	Network      map[string]string `json:"network"`
	Secrets      map[string]bool   `json:"secrets"`
	Home         string            `json:"home"`
	Temporary    string            `json:"temporary"`
	InstalledBin string            `json:"installedBin"`
	PublicConfig string            `json:"publicConfig"`
	OwnAccount   bool              `json:"ownAccount"`
}

func TestCLIBaseEnvironmentChildHelper(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "connectorops-base-environment-helper" {
			marker = i
			break
		}
	}
	if marker < 0 || marker+1 >= len(os.Args) {
		return
	}
	observation := cliEnvironmentObservation{
		Network: map[string]string{}, Secrets: map[string]bool{}, Home: os.Getenv("HOME"),
		Temporary: os.Getenv("TMPDIR"), InstalledBin: os.Getenv("CONNECTOR_BIN_DIR"),
		PublicConfig: os.Getenv("SERVICE_ENDPOINT"), OwnAccount: os.Getenv("ACCOUNT_TOKEN") == "connector-owned-token",
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "PATHEXT"} {
		observation.Network[key] = os.Getenv(key)
	}
	for _, key := range []string{"AP_ACCESS_TOKEN", "SSH_AUTH_SOCK", "OTHER_CONNECTOR_SECRET", "UNRELATED_SECRET"} {
		observation.Secrets[key] = os.Getenv(key) != ""
	}
	data, err := json.Marshal(observation)
	if err != nil {
		os.Exit(8)
	}
	if os.Args[marker+1] == "version" {
		// The lifecycle version command retains valid SemVer stdout, while
		// recording its actual child environment under this fixture's private HOME.
		if err := os.WriteFile(filepath.Join(observation.Home, "lifecycle-environment.json"), data, 0600); err != nil {
			os.Exit(9)
		}
		os.Stdout.WriteString("1.0.0\n")
	} else {
		os.Stdout.Write(data)
	}
	os.Exit(0)
}

func TestGenericCLIInvokeUsesLifecycleNetworkEnvironmentWithoutAmbientCredentials(t *testing.T) {
	wantNetwork := map[string]string{
		"HTTP_PROXY": "http://proxy.example.test:8081", "HTTPS_PROXY": "http://proxy.example.test:8082",
		"ALL_PROXY": "socks5://proxy.example.test:1080", "NO_PROXY": "jira.example.test,localhost,127.0.0.1",
		"SSL_CERT_FILE": filepath.Join(t.TempDir(), "ca.pem"), "SSL_CERT_DIR": t.TempDir(),
		"PATHEXT": ".EXE;.CMD;.COM;.BAT",
	}
	for key, value := range wantNetwork {
		t.Setenv(key, value)
	}
	for _, key := range []string{"AP_ACCESS_TOKEN", "SSH_AUTH_SOCK", "OTHER_CONNECTOR_SECRET", "UNRELATED_SECRET", "ACCOUNT_TOKEN"} {
		t.Setenv(key, "must-not-inherit")
	}
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "environment-fixture")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	entry := "environment-helper"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, entry), data, 0700); err != nil {
		t.Fatal(err)
	}
	versionCommand := entry + " -test.run=TestCLIBaseEnvironmentChildHelper -- connectorops-base-environment-helper version"
	files := map[string]any{
		"connector.json": map[string]any{
			"id": "environment-fixture", "name": "Environment fixture", "version": "1.0.0", "type": "cli", "auth_mode": "token",
			"token_schema": map[string]any{"fields": []map[string]any{{"key": "ACCOUNT_TOKEN", "label": "Account token", "type": "password", "required": true}}},
		},
		"cli.json": map[string]any{
			"versionCheck": map[string]any{"command": map[string]string{"darwin": versionCommand, "linux": versionCommand, "win32": versionCommand}, "minVersion": "1.0.0"},
			"staticEnv":    map[string]string{"SERVICE_ENDPOINT": "https://service.example.test"},
			"env":          map[string]string{"ACCOUNT_TOKEN": "${ACCOUNT_TOKEN}"},
		},
	}
	for name, value := range files {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := sources.Load("environment-fixture")
	if err != nil {
		t.Fatal(err)
	}
	manager := connectorauth.New(t.Context(), sources, nil)
	if state, err := manager.Prepare(t.Context(), pkg.ID); err != nil || state.Status != "ready" {
		t.Fatalf("fixture preparation failed: %+v %v", state, err)
	}
	private, err := pkg.ConnectorStateDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(private, "home", "lifecycle-environment.json"))
	var lifecycle cliEnvironmentObservation
	if err != nil || json.Unmarshal(data, &lifecycle) != nil {
		t.Fatalf("lifecycle child environment unavailable: %v", err)
	}
	if _, err := manager.SetToken(t.Context(), pkg.ID, map[string]string{"ACCOUNT_TOKEN": "connector-owned-token"}); err != nil {
		t.Fatal(err)
	}
	service := Service{Auth: manager, Sources: sources}
	scope := Scope{Subject: "fixture-subject", IdempotencyNamespace: "environment-regression", Execution: []Permission{{pkg.ID, "cli"}}, Check: func() error { return nil }}
	result, err := service.Invoke(context.Background(), scope, Request{
		ConnectorID: pkg.ID, Adapter: "cli",
		Args: []string{"-test.run=TestCLIBaseEnvironmentChildHelper", "--", "connectorops-base-environment-helper", "inspect"},
	})
	var business cliEnvironmentObservation
	if err != nil || result.ExitCode != 0 || json.Unmarshal([]byte(result.Stdout), &business) != nil {
		t.Fatalf("generic child environment unavailable: %+v %v", result, err)
	}
	for phase, observation := range map[string]cliEnvironmentObservation{"lifecycle": lifecycle, "business": business} {
		if !reflect.DeepEqual(observation.Network, wantNetwork) {
			t.Fatalf("%s lost proxy, CA or PATHEXT configuration: %+v", phase, observation.Network)
		}
		for key, present := range observation.Secrets {
			if present {
				t.Fatalf("%s inherited unrelated credential %s", phase, key)
			}
		}
		if observation.Home != filepath.Join(private, "home") || observation.Temporary != filepath.Join(private, "tmp") || observation.InstalledBin == "" || observation.PublicConfig != "https://service.example.test" {
			t.Fatalf("%s lost its connector-specific environment: %+v", phase, observation)
		}
	}
	if lifecycle.OwnAccount || !business.OwnAccount || lifecycle.InstalledBin != business.InstalledBin {
		t.Fatal("credential phase isolation or installation identity changed")
	}
}
