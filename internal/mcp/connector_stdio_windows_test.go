//go:build windows

package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPWindowsConnectorNPMShimUsesNodeArgv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "package with spaces", "bin")
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
	server := ServerDefinition{ConnectorID: "demo", Transport: TransportStdio, Command: "office", Args: args, WorkingDir: bin, ConnectorBinDir: bin}
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
	if strings.EqualFold(filepath.Base(cmd.Path), "cmd.exe") || cmd.Dir != bin {
		t.Fatal("MCP npm shim used shell interpretation or lost its working directory")
	}
}

func TestMCPWindowsCommandsOutsideConnectorBinKeepTheirLauncher(t *testing.T) {
	tools := filepath.Join(t.TempDir(), "tools")
	if err := os.MkdirAll(tools, 0700); err != nil {
		t.Fatal(err)
	}
	// npm's own npx.cmd sets its script through variables; it is not a connector entry.
	npx := filepath.Join(tools, "npx.cmd")
	if err := os.WriteFile(npx, []byte("@ECHO OFF\r\nSET \"NPX_CLI_JS=%~dp0\\node_modules\\npm\\bin\\npx-cli.js\"\r\n\"%NODE_EXE%\" \"%NPX_CLI_JS%\" %*\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	server := ServerDefinition{Transport: TransportStdio, Command: "npx", Args: []string{"-y", "example-mcp"}, ConnectorBinDir: filepath.Join(t.TempDir(), "bin")}
	transport, err := (&Client{}).transportWithIdentity(server, nil)
	if err != nil {
		t.Fatal("ordinary stdio command was rejected", err)
	}
	if cmd := transport.(*sdkmcp.CommandTransport).Command; !strings.EqualFold(cmd.Path, npx) {
		t.Fatalf("ordinary stdio command was rewritten: %s", cmd.Path)
	}
}
