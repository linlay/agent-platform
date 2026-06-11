package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

func TestExternalToolManagerInvokesStdioJSONRPC(t *testing.T) {
	manager := NewExternalToolManager()
	defer manager.Close()
	manager.Configure([]api.ToolDetailResponse{externalTestToolDefinition("qs_read", "ok")})

	result, err := manager.Invoke(context.Background(), api.ToolDetailResponse{
		Name: "qs_read",
		Meta: externalTestMeta("ok"),
	}, map[string]any{"file_path": "10.69"}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke external tool: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success result, got %#v", result)
	}
	if result.Structured["tool"] != "qs_read" {
		t.Fatalf("unexpected result %#v", result.Structured)
	}
	args, _ := result.Structured["arguments"].(map[string]any)
	if args["file_path"] != "10.69" {
		t.Fatalf("unexpected arguments %#v", args)
	}
}

func TestExternalToolManagerReturnsStructuredToolError(t *testing.T) {
	manager := NewExternalToolManager()
	defer manager.Close()
	manager.Configure([]api.ToolDetailResponse{externalTestToolDefinition("qs_read", "structured-error")})

	result, err := manager.Invoke(context.Background(), api.ToolDetailResponse{
		Name: "qs_read",
		Meta: externalTestMeta("structured-error"),
	}, nil, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke external tool: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "fake_error" {
		t.Fatalf("expected structured tool error, got %#v", result)
	}
}

func TestExternalToolManagerTimeout(t *testing.T) {
	manager := NewExternalToolManager()
	defer manager.Close()
	manager.Configure([]api.ToolDetailResponse{externalTestToolDefinition("qs_read", "slow")})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := manager.Invoke(ctx, api.ToolDetailResponse{
		Name: "qs_read",
		Meta: externalTestMeta("slow"),
	}, nil, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke external tool: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "external_tool_timeout" {
		t.Fatalf("expected timeout result, got %#v", result)
	}
}

func TestExternalToolManagerProcessExitUnavailable(t *testing.T) {
	manager := NewExternalToolManager()
	defer manager.Close()
	manager.Configure([]api.ToolDetailResponse{externalTestToolDefinition("qs_read", "exit")})

	result, err := manager.Invoke(context.Background(), api.ToolDetailResponse{
		Name: "qs_read",
		Meta: externalTestMeta("exit"),
	}, nil, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke external tool: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "external_tool_unavailable" {
		t.Fatalf("expected unavailable result, got %#v", result)
	}
}

func TestExternalToolManagerHelper(t *testing.T) {
	if os.Getenv("AGENT_PLATFORM_EXTERNAL_TOOL_HELPER") != "1" {
		return
	}
	mode := os.Getenv("AGENT_PLATFORM_EXTERNAL_TOOL_MODE")
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req externalRPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			_ = encoder.Encode(externalRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})
		case "shutdown":
			_ = encoder.Encode(externalRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})
			os.Exit(0)
		case "tools/call":
			if mode == "slow" {
				time.Sleep(time.Second)
			}
			if mode == "exit" {
				os.Exit(3)
			}
			params := AnyMapNode(req.Params)
			if mode == "structured-error" {
				_ = encoder.Encode(externalRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"error": "fake_error", "message": "boom"}})
				continue
			}
			_ = encoder.Encode(externalRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"tool":      AnyStringNode(params["name"]),
				"arguments": AnyMapNode(params["arguments"]),
			}})
		default:
			_ = encoder.Encode(externalRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &externalRPCError{Code: -32601, Message: "method not found"}})
		}
	}
	os.Exit(0)
}

func externalTestToolDefinition(name string, mode string) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: name,
		Meta: map[string]any{
			"kind": "external",
			"external": map[string]any{
				"transport":      "stdio-jsonrpc",
				"serviceKey":     "helper",
				"command":        os.Args[0],
				"args":           []string{"-test.run=TestExternalToolManagerHelper"},
				"startupTimeout": 1,
				"timeout":        30,
				"env": map[string]any{
					"AGENT_PLATFORM_EXTERNAL_TOOL_HELPER": "1",
					"AGENT_PLATFORM_EXTERNAL_TOOL_MODE":   mode,
				},
			},
		},
	}
}

func externalTestMeta(mode string) map[string]any {
	return externalTestToolDefinition("qs_read", mode).Meta
}
