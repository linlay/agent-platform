package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/windows"
)

func TestWindowsStdioMCPDoesNotAllocateConsole(t *testing.T) {
	root := t.TempDir()
	content := "serverKey: console-test\ntransport: stdio\ncommand: " + strconv.Quote(os.Args[0]) + "\n" +
		"args: [\"-test.run=^TestWindowsMCPConsoleHelper$\"]\nenv:\n  AP_MCP_CONSOLE_HELPER: \"1\"\nstartup-timeout: 5\nread-timeout: 5\nretry: 0\n"
	writeMCPRegistryFile(t, filepath.Join(root, "stdio.yml"), content)
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClientWithGate(registry, nil, nil)
	t.Cleanup(func() { _ = client.Close() })
	tools, err := client.ListTools(t.Context(), "console-test")
	if err != nil || len(tools) != 1 {
		t.Fatalf("initialize/list tools: %#v, %v", tools, err)
	}
	result, err := client.CallTool(t.Context(), "console-test", "console_test", map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := result.(map[string]any)
	structured, _ := mapped["structuredContent"].(map[string]any)
	if console, ok := structured["hasConsole"].(bool); !ok || console {
		t.Fatalf("stdio process allocated a console: %#v", result)
	}
}

func TestWindowsMCPConsoleHelper(t *testing.T) {
	if os.Getenv("AP_MCP_CONSOLE_HELPER") != "1" {
		return
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "console-test", Version: "1.0.0"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "console_test", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
			return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"hasConsole": console != 0}}, nil
		})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}
