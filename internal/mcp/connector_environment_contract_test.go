package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/hostenv"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func oauthComponentFixture(t *testing.T, component map[string]any) connector.Package {
	t.Helper()
	dir := t.TempDir()
	oauth, _ := json.Marshal(map[string]any{"client_id": "test-client", "authorization_endpoint": "https://auth.example.test/authorize", "token_endpoint": "https://auth.example.test/token", "resource": "https://resource.example.test"})
	return connector.Package{Manifest: connector.Manifest{ID: "demo", Name: "Demo", Version: "1.0.0", Type: "mcp", AuthMode: connector.AuthOAuth, OAuth: oauth}, StateRoot: t.TempDir(), Dir: dir, MCP: map[string]map[string]any{"main": component}}
}

func saveOAuthComponentToken(t *testing.T, pkg connector.Package, destination, value string) {
	t.Helper()
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"resource": "https://resource.example.test", "destination": destination, "config": oauth2.Config{}, "token": &oauth2.Token{AccessToken: value, TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}})
	if err := os.WriteFile(filepath.Join(dir, "oauth.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
}

func TestMCPPublicOAuthHeaderAndEnvironmentUseStoredCredential(t *testing.T) {
	t.Setenv("OAUTH_ACCESS_TOKEN", "ambient-spoof")
	endpoint := "https://service.example.test/mcp"
	pkg := oauthComponentFixture(t, map[string]any{"type": "streamableHttp", "url": endpoint, "headers": map[string]any{"X-Access-Token": "${OAUTH_ACCESS_TOKEN}"}, "staticHeaders": map[string]any{"X-Client": "public"}})
	before, _ := json.Marshal(pkg.MCP)
	saveOAuthComponentToken(t, pkg, endpoint, "fake-oauth-first")
	server, err := connectorServer(pkg, "main")
	if err != nil || server.SetupError != "" || server.Headers["X-Access-Token"] != "${ACCESS_TOKEN}" {
		t.Fatal("public OAuth header was not normalized", err)
	}
	after, _ := json.Marshal(pkg.MCP)
	if string(before) != string(after) {
		t.Fatal("OAuth normalization rewrote the package")
	}
	want := "fake-oauth-first"
	calls := 0
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "Bearer "+want || req.Header.Get("X-Access-Token") != want || req.Header.Get("X-Client") != "public" {
			t.Error("OAuth headers did not use the current stored token")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	client := (&Client{httpClient: &http.Client{Transport: base}}).httpClientForServer(server)
	for _, value := range []string{"fake-oauth-first", "fake-oauth-second"} {
		want = value
		saveOAuthComponentToken(t, pkg, endpoint, value)
		req, _ := http.NewRequest(http.MethodPost, endpoint, nil)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if req.Header.Get("Authorization") != "" || req.Header.Get("X-Access-Token") != "" {
			t.Fatal("credential modified the caller request")
		}
	}
	if calls != 2 {
		t.Fatal("unexpected OAuth requests")
	}
	pkg.MCP["main"] = map[string]any{"type": "stdio", "command": os.Args[0], "env": map[string]any{"SERVICE_TOKEN": "${OAUTH_ACCESS_TOKEN}"}}
	saveOAuthComponentToken(t, pkg, "https://resource.example.test", "fake-stdio-token")
	server, err = connectorServer(pkg, "main")
	if err != nil || server.ConnectorCredentialEnv["SERVICE_TOKEN"] != "${ACCESS_TOKEN}" {
		t.Fatal("stdio OAuth public template missing", err)
	}
	resolved, err := connectorauth.ResolveEnvironment(t.Context(), connector.CredentialEnvironment{Root: server.ConnectorAuthRoot, ID: server.ConnectorID, Mode: server.ConnectorAuthMode, Resource: server.ConnectorOAuthResource, Destination: server.ConnectorOAuthResource, Env: server.ConnectorCredentialEnv}, "")
	if err != nil || resolved["SERVICE_TOKEN"] != "fake-stdio-token" {
		t.Fatal("stdio OAuth read ambient credentials", err)
	}
	transport, err := (&Client{}).transportWithIdentity(server, resolved)
	if err != nil {
		t.Fatal(err)
	}
	child := transport.(*sdkmcp.CommandTransport).Command
	if hostenv.Value(child.Env, "SERVICE_TOKEN") != "fake-stdio-token" || hostenv.Value(child.Env, "OAUTH_ACCESS_TOKEN") != "" {
		t.Fatal("stdio OAuth environment was not isolated")
	}
}

func TestMCPRejectsStaticTemplatesUnknownReferencesAndConflicts(t *testing.T) {
	for _, mode := range []connector.AuthMode{connector.AuthToken, connector.AuthOneID, connector.AuthOAuth, connector.AuthDelegated} {
		for _, component := range []map[string]any{
			{"type": "streamableHttp", "url": "https://service.example.test/mcp", "headers": map[string]any{"X-Key": "${UNKNOWN}"}},
			{"type": "streamableHttp", "url": "https://service.example.test/mcp", "staticHeaders": map[string]any{"X-Key": "${ONEID_TOKEN}"}},
			{"type": "streamableHttp", "url": "https://service.example.test/mcp", "headers": map[string]any{"X-Key": "fixed"}, "staticHeaders": map[string]any{"x-key": "fixed"}},
			{"type": "stdio", "command": os.Args[0], "staticEnv": map[string]any{"KEY": "${OAUTH_ACCESS_TOKEN}"}},
			{"type": "stdio", "command": os.Args[0], "env": map[string]any{"KEY": "${UNKNOWN}"}},
			{"type": "stdio", "command": os.Args[0], "env": map[string]any{"KEY": "fixed"}, "staticEnv": map[string]any{"KEY": "fixed"}},
			{"type": "stdio", "command": os.Args[0], "staticEnv": map[string]any{"CONNECTOR_BIN_DIR": "/spoof"}},
		} {
			pkg := oauthComponentFixture(t, component)
			pkg.AuthMode = mode
			pkg.TokenSchema = json.RawMessage(`{"fields":[{"key":"TOKEN","required":true}]}`)
			if _, err := connectorServer(pkg, "main"); err == nil {
				t.Fatal("invalid MCP authentication declaration accepted", mode)
			}
		}
	}
	for _, mode := range []connector.AuthMode{connector.AuthOneID, connector.AuthOAuth} {
		wrong := "${ONEID_TOKEN}"
		if mode == connector.AuthOneID {
			wrong = "${OAUTH_ACCESS_TOKEN}"
		}
		pkg := oauthComponentFixture(t, map[string]any{"type": "stdio", "command": os.Args[0], "env": map[string]any{"TOKEN": wrong}})
		pkg.AuthMode = mode
		if _, err := connectorServer(pkg, "main"); err == nil {
			t.Fatal("cross-mode token template accepted", mode)
		}
	}
}

func TestMCPStdioSharesCLIPrivateDirectoriesAndInstallationPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	packageBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(packageBin, 0700); err != nil {
		t.Fatal(err)
	}
	command := "serve-tool"
	if runtime.GOOS == "windows" {
		command += ".exe"
	}
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo", Name: "Demo", Version: "1.0.0", Type: "mcp", AuthMode: connector.AuthDelegated}, Dir: dir, BinDir: packageBin, StateRoot: filepath.Join(root, "state"), InstallationDigest: strings.Repeat("a", 64), CLI: map[string]any{
		"versionCheck": map[string]any{"command": map[string]any{"darwin": "serve-tool --version", "linux": "serve-tool --version", "win32": "serve-tool.exe --version"}, "minVersion": "1.0.0"},
		"platform":     map[string]any{"configEnv": "TOOL_CONFIG_DIR"},
	}}
	bin, err := connectorauth.CLIInstallationBinDir(pkg)
	if err != nil {
		t.Fatal(err)
	}
	private, err := connectorauth.CLIPrivateEnvironmentDirs(pkg)
	if err != nil {
		t.Fatal(err)
	}
	ambient := t.TempDir()
	for _, destination := range []string{filepath.Join(bin, command), filepath.Join(packageBin, command), filepath.Join(ambient, command)} {
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(os.Args[0], destination); err != nil {
			source, err := os.Open(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE, 0755)
			if err != nil {
				source.Close()
				t.Fatal(err)
			}
			_, err = io.Copy(target, source)
			source.Close()
			target.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", ambient+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCP_AMBIENT_SECRET", "fake-parent-secret")
	pkg.MCP = map[string]map[string]any{"main": {"type": "stdio", "command": command, "args": []string{"-test.run=^TestMCPPrivateCLIHelperProcess$"}, "staticEnv": map[string]any{"MCP_PRIVATE_TEST_HELPER": "1", "MCP_EXPECTED_HOME": private["HOME"], "MCP_EXPECTED_BIN": filepath.Join(bin, command)}}}
	server, err := connectorServer(pkg, "main")
	if err != nil {
		t.Fatal(err)
	}
	if server.Command != command || server.ConnectorInstallationBinDir != bin || server.ConnectorPrivateEnv["HOME"] != private["HOME"] || server.ConnectorPrivateEnv["TOOL_CONFIG_DIR"] != private["XDG_CONFIG_HOME"] {
		t.Fatal("stdio did not preserve CLI installation/private layout")
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
	client := NewClientWithGate(&Registry{servers: map[string]ServerDefinition{server.Key: server}}, nil, nil)
	defer client.Close()
	result, err := client.CallTool(t.Context(), server.Key, "read_environment", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := result.(map[string]any)
	structured, _ := mapped["structuredContent"].(map[string]any)
	if structured["privateBin"] != true || structured["privateHome"] != true || structured["privateConfig"] != true || structured["ambientSecret"] != false {
		t.Fatal("stdio child used an inconsistent or ambient environment")
	}
}

func TestMCPPrivateCLIHelperProcess(t *testing.T) {
	if os.Getenv("MCP_PRIVATE_TEST_HELPER") != "1" {
		return
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "private-cli-test", Version: "1.0.0"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "read_environment", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		executable, _ := os.Executable()
		executable, _ = filepath.EvalSymlinks(executable)
		expected, _ := filepath.EvalSymlinks(os.Getenv("MCP_EXPECTED_BIN"))
		values := map[string]any{"privateBin": executable == expected, "privateHome": os.Getenv("HOME") == os.Getenv("MCP_EXPECTED_HOME"), "privateConfig": os.Getenv("TOOL_CONFIG_DIR") == os.Getenv("XDG_CONFIG_HOME"), "ambientSecret": os.Getenv("MCP_AMBIENT_SECRET") != ""}
		return &sdkmcp.CallToolResult{StructuredContent: values}, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}
