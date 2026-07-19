package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestClientStdioSessionConcurrencyReloadAndClose(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "stdio.yml")
	writeStdioHelperRegistry(t, registryPath, "first_tool")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	client := NewClient(registry, nil)

	tools, err := client.ListTools(t.Context(), "stdio-test")
	if err != nil {
		t.Fatalf("ListTools(first): %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "first_tool" {
		t.Fatalf("unexpected first tools: %#v", tools)
	}

	var firstPID int
	var pidMu sync.Mutex
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			result, callErr := client.CallTool(context.Background(), "stdio-test", "first_tool", map[string]any{"value": value}, nil)
			if callErr != nil {
				errors <- callErr
				return
			}
			pid, parseErr := mcpResultPID(result)
			if parseErr != nil {
				errors <- parseErr
				return
			}
			pidMu.Lock()
			defer pidMu.Unlock()
			if firstPID == 0 {
				firstPID = pid
			} else if firstPID != pid {
				errors <- fmt.Errorf("concurrent calls used different processes: %d and %d", firstPID, pid)
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for callErr := range errors {
		t.Error(callErr)
	}
	if firstPID == 0 {
		t.Fatal("stdio helper PID was not returned")
	}

	writeStdioHelperRegistry(t, registryPath, "second_tool")
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	client.Reconcile()
	waitForProcessExit(t, firstPID)
	tools, err = client.ListTools(t.Context(), "stdio-test")
	if err != nil {
		t.Fatalf("ListTools(second): %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "second_tool" {
		t.Fatalf("unexpected reloaded tools: %#v", tools)
	}
	result, err := client.CallTool(t.Context(), "stdio-test", "second_tool", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("CallTool(second): %v", err)
	}
	secondPID, err := mcpResultPID(result)
	if err != nil {
		t.Fatal(err)
	}
	if secondPID == firstPID {
		t.Fatalf("registry reload reused old process %d", firstPID)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForProcessExit(t, secondPID)
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("AP_MCP_STDIO_HELPER") != "1" {
		return
	}
	toolName := os.Getenv("AP_MCP_STDIO_TOOL")
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "stdio-test-helper", Version: "1.0.0"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        toolName,
		Description: "stdio test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		structured := map[string]any{"pid": os.Getpid(), "tool": toolName}
		data, _ := json.Marshal(structured)
		return &sdkmcp.CallToolResult{
			Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
			StructuredContent: structured,
		}, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func writeStdioHelperRegistry(t *testing.T, path string, toolName string) {
	t.Helper()
	content := "serverKey: stdio-test\n" +
		"transport: stdio\n" +
		"command: " + strconv.Quote(os.Args[0]) + "\n" +
		"args: [\"-test.run=^TestMCPStdioHelperProcess$\"]\n" +
		"env:\n" +
		"  AP_MCP_STDIO_HELPER: \"1\"\n" +
		"  AP_MCP_STDIO_TOOL: " + strconv.Quote(toolName) + "\n" +
		"startup-timeout: 5\n" +
		"read-timeout: 5\n" +
		"retry: 0\n"
	writeMCPRegistryFile(t, path, content)
}

func mcpResultPID(result any) (int, error) {
	mapped, _ := result.(map[string]any)
	structured, _ := mapped["structuredContent"].(map[string]any)
	value, ok := structured["pid"].(json.Number)
	if !ok {
		return 0, fmt.Errorf("MCP result has no PID: %#v", result)
	}
	pid, err := strconv.Atoi(value.String())
	if err != nil {
		return 0, fmt.Errorf("invalid MCP PID %q: %w", value, err)
	}
	return pid, nil
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", pid)
}
