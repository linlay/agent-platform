package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestRegistryLoadsStdioAndResolvesRelativePaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "qiuerscript.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMCPRegistryFile(t, path, `
serverKey: qiuerscript
transport: stdio
command: bin/qiuerscript-tool
args: [serve, --datasource, dev]
env:
  QS_PROFILE: test
workingDirectory: work
startup-timeout: 7
read-timeout: 30
`)
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	server, ok := registry.Server("qiuerscript")
	if !ok {
		t.Fatal("stdio server was not loaded")
	}
	wantCommand := filepath.Join(filepath.Dir(path), "bin", "qiuerscript-tool")
	wantWorkDir := filepath.Join(filepath.Dir(path), "work")
	if server.Transport != TransportStdio || server.Command != wantCommand || server.WorkingDir != wantWorkDir {
		t.Fatalf("unexpected stdio server: %#v", server)
	}
	if strings.Join(server.Args, " ") != "serve --datasource dev" || server.Env["QS_PROFILE"] != "test" {
		t.Fatalf("unexpected stdio arguments/env: %#v", server)
	}
	if server.StartupTimeout != 7 || server.ReadTimeout != 30 {
		t.Fatalf("unexpected timeouts: %#v", server)
	}
}

func TestRegistryDefaultsToStreamableHTTP(t *testing.T) {
	root := t.TempDir()
	writeMCPRegistryFile(t, filepath.Join(root, "http.yml"), "serverKey: demo\nbaseUrl: http://127.0.0.1:8080\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	server, ok := registry.Server("demo")
	if !ok || server.Transport != TransportStreamableHTTP || server.ResolvedURL() != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("unexpected default HTTP server: %#v", server)
	}
}

func TestRegistryLoadsDesktopIdentityAuthSource(t *testing.T) {
	root := t.TempDir()
	writeMCPRegistryFile(t, filepath.Join(root, "http.yml"), "serverKey: demo\nbaseUrl: https://mcp.example.test\nauthSource: desktop-identity\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	server, ok := registry.Server("demo")
	if !ok || server.AuthSource != AuthSourceDesktopIdentity || server.AuthToken != "" {
		t.Fatalf("unexpected desktop identity auth source: %#v", server)
	}
}

func TestRegistryLoadsAgentBindingsAndToolSyncResolvesBoundTools(t *testing.T) {
	root := t.TempDir()
	writeMCPRegistryFile(t, filepath.Join(root, "http.yml"), `
serverKey: demo
baseUrl: https://mcp.example.test
bindings:
  agents: [cutej, "中文 Agent@dev", Other, CUTEJ, ""]
`)
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	server, ok := registry.Server("demo")
	if !ok || !reflect.DeepEqual(server.BoundAgentKeys, []string{"cutej", "中文 Agent@dev", "Other"}) {
		t.Fatalf("unexpected Agent bindings: %#v", server.BoundAgentKeys)
	}
	sync := NewToolSync(registry, nil)
	sync.snapshots["demo"] = serverToolSnapshot{toolsByName: map[string]api.ToolDetailResponse{
		"second": {Name: "demo_second", Meta: map[string]any{"serverKey": "demo"}},
		"first":  {Name: "demo_first", Meta: map[string]any{"serverKey": "demo"}},
	}}
	sync.toolsByName, sync.aliasToCanonical = mergeSnapshots(registry.Servers(), sync.snapshots)
	sync.registryVersion = registry.Version()
	sync.generation = 1
	if got := sync.BoundToolNames("CUTEJ"); !reflect.DeepEqual(got, []string{"demo_first", "demo_second"}) {
		t.Fatalf("BoundToolNames(cutej) = %#v", got)
	}
	if got := sync.BoundToolNames("missing"); len(got) != 0 {
		t.Fatalf("unbound Agent tools = %#v", got)
	}
	writeMCPRegistryFile(t, filepath.Join(root, "http.yml"), "serverKey: demo\nbaseUrl: https://mcp.example.test\nbindings:\n  agents: [other]\n")
	if err := registry.Reload(); err != nil {
		t.Fatalf("reload changed binding: %v", err)
	}
	if got := sync.BoundToolNames("other"); len(got) != 0 {
		t.Fatalf("new Registry binding mixed with old tool snapshot: %#v", got)
	}
}

func TestBoundToolNamesUsesFinalMergedMCPServerOwnership(t *testing.T) {
	root := t.TempDir()
	writeMCPRegistryFile(t, filepath.Join(root, "alpha.yml"), "serverKey: alpha\nbaseUrl: https://alpha.example.test\nbindings:\n  agents: [cutej]\n")
	writeMCPRegistryFile(t, filepath.Join(root, "beta.yml"), "serverKey: beta\nbaseUrl: https://beta.example.test\nbindings:\n  agents: [other]\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	sync := NewToolSync(registry, nil)
	sync.snapshots = map[string]serverToolSnapshot{
		"alpha": {toolsByName: map[string]api.ToolDetailResponse{
			"alpha_only": {Name: "alpha_only", Meta: map[string]any{"serverKey": "alpha"}},
			"shared":     {Name: "shared", Meta: map[string]any{"serverKey": "alpha"}},
		}},
		"beta": {toolsByName: map[string]api.ToolDetailResponse{
			"beta_only": {Name: "beta_only", Meta: map[string]any{"serverKey": "beta"}},
			"shared":    {Name: "shared", Meta: map[string]any{"serverKey": "beta"}},
		}},
	}
	sync.toolsByName, sync.aliasToCanonical = mergeSnapshots(registry.Servers(), sync.snapshots)
	sync.registryVersion = registry.Version()

	if got := sync.BoundToolNames("cutej"); !reflect.DeepEqual(got, []string{"alpha_only"}) {
		t.Fatalf("cutej received conflicted or foreign MCP tools: %#v", got)
	}
	if got := sync.BoundToolNames("other"); !reflect.DeepEqual(got, []string{"beta_only"}) {
		t.Fatalf("other received conflicted or foreign MCP tools: %#v", got)
	}
}

func TestRegistryRejectsInvalidTransportFieldCombinations(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "http missing base", content: "serverKey: demo\ntransport: streamable-http\n", want: "requires baseUrl"},
		{name: "http with command", content: "serverKey: demo\nbaseUrl: http://127.0.0.1\ncommand: tool\n", want: "cannot declare stdio fields"},
		{name: "http with empty args", content: "serverKey: demo\nbaseUrl: http://127.0.0.1\nargs: []\n", want: "cannot declare stdio fields"},
		{name: "stdio missing command", content: "serverKey: demo\ntransport: stdio\n", want: "requires command"},
		{name: "stdio with base", content: "serverKey: demo\ntransport: stdio\ncommand: tool\nbaseUrl: http://127.0.0.1\n", want: "cannot declare HTTP fields"},
		{name: "stdio with auth source", content: "serverKey: demo\ntransport: stdio\ncommand: tool\nauthSource: desktop-identity\n", want: "cannot declare HTTP fields"},
		{name: "http with static and sourced auth", content: "serverKey: demo\nbaseUrl: https://mcp.example.test\nauthToken: fixed\nauthSource: desktop-identity\n", want: "cannot declare both authToken and authSource"},
		{name: "http with unknown auth source", content: "serverKey: demo\nbaseUrl: https://mcp.example.test\nauthSource: unknown\n", want: "unsupported MCP authSource"},
		{name: "desktop identity requires https", content: "serverKey: demo\nbaseUrl: http://mcp.example.test\nauthSource: desktop-identity\n", want: "requires a valid HTTPS baseUrl"},
		{name: "bindings agents must be a list", content: "serverKey: demo\nbaseUrl: https://mcp.example.test\nbindings:\n  agents: cutej\n", want: "bindings.agents"},
		{name: "bindings must be a map", content: "serverKey: demo\nbaseUrl: https://mcp.example.test\nbindings: cutej\n", want: "bindings must be a map"},
		{name: "bindings require agents", content: "serverKey: demo\nbaseUrl: https://mcp.example.test\nbindings: {}\n", want: "bindings.agents is required"},
		{name: "bindings agents must not be empty", content: "serverKey: demo\nbaseUrl: https://mcp.example.test\nbindings:\n  agents: []\n", want: "at least one Agent key"},
		{name: "unknown transport", content: "serverKey: demo\ntransport: websocket\nbaseUrl: http://127.0.0.1\n", want: "unsupported MCP transport"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeMCPRegistryFile(t, filepath.Join(root, "server.yml"), test.content)
			_, err := NewRegistry(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewRegistry error = %v, want %q", err, test.want)
			}
		})
	}

}

func TestRegistryRejectsRemovedToolClassificationFields(t *testing.T) {
	for _, field := range []string{"type", "kind", "toolAction", "submitResultFormat"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			writeMCPRegistryFile(t, filepath.Join(root, "server.yml"), "serverKey: demo\nbaseUrl: http://127.0.0.1:8080\ntools:\n  - name: lookup\n    "+field+": legacy\n")
			_, err := NewRegistry(root)
			if err == nil || !strings.Contains(err.Error(), field+" is no longer supported") {
				t.Fatalf("NewRegistry error = %v, want removed field %q rejection", err, field)
			}
		})
	}
}

func writeMCPRegistryFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
}
