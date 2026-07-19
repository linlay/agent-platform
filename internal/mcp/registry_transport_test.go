package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func writeMCPRegistryFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
}
