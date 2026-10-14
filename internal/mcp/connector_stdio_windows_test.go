//go:build windows

package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/hostenv"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPWindowsPreparedNPMShimUsesNodeArgv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "installation with spaces", "bin")
	script := filepath.Join(bin, "node_modules", "example-cli", "server.cjs")
	if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(bin, "node.exe"), script} {
		if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "office.cmd"), []byte(`@"%_prog%" "%~dp0\node_modules\example-cli\server.cjs" %*`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"mcp", `{"text":"%PATH% & | $(touch nope) 中文"}`}
	server := ServerDefinition{ConnectorID: "demo", Transport: TransportStdio, Command: "office", Args: args, WorkingDir: bin, ConnectorInstallationBinDir: bin}
	transport, err := (&Client{}).transportWithIdentity(server, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := transport.(*sdkmcp.CommandTransport).Command
	canonicalScript, _ := filepath.EvalSymlinks(script)
	wantNode := filepath.Join(bin, "node.exe")
	if cmd.Path != wantNode || !reflect.DeepEqual(cmd.Args, append([]string{wantNode, canonicalScript}, args...)) {
		t.Fatal("MCP npm shim did not preserve Node argv")
	}
	if strings.EqualFold(filepath.Base(cmd.Path), "cmd.exe") || hostenv.Value(cmd.Env, "CONNECTOR_BIN_DIR") != bin || cmd.Dir != bin {
		t.Fatal("MCP npm shim used shell interpretation or lost its installation")
	}
}
