package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	. "agent-platform/internal/contracts"
)

func TestDesktopAwcpParamsFileMatchesInline(t *testing.T) {
	root := t.TempDir()
	chatDir, tempDir := t.TempDir(), t.TempDir()
	data := "{\n\"revision\":\"手册版本\",\"action\":\"forum.sections.list\",\"args\":{\"text\":\"中文\\n多行\\\"引号\",\"object\":{},\"array\":[1,2.5,true,false,null,{}],\"number\":23,\"boolean\":false}}"
	var params map[string]any
	if err := json.Unmarshal([]byte(data), &params); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"params.json", "@workspace/params.json", "@chat/params.json", "@temp/params.json", filepath.Join(root, "params.json"), "/workspace/params.json"} {
		t.Run(path, func(t *testing.T) {
			executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
			execCtx.Session.RuntimeContext.LocalPaths.ChatDir = chatDir
			execCtx.Session.TempRoot = tempDir
			if path == "/workspace/params.json" {
				execCtx.Session.AgentHasRuntimeSandbox = true
				execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
			}
			for _, dir := range []string{root, chatDir, tempDir} {
				mustWriteFile(t, filepath.Join(dir, "params.json"), data)
			}
			for _, source := range []map[string]any{{"params": params}, {"paramsFile": path}} {
				source["method"] = desktopAwcpInvokeMethod
				source["surfaceId"] = "page:test"
				result, err := executor.Invoke(context.Background(), "desktop_cdp", source, execCtx)
				if err != nil || result.ExitCode != 0 {
					t.Fatalf("%#v %v", result, err)
				}
			}
			_, requests := invoker.snapshots()
			if len(requests) != 2 || requests[0].Type != desktopAwcpInvokeAction || !reflect.DeepEqual(requests[0].Payload, requests[1].Payload) || !reflect.DeepEqual(requests[0].Source, requests[1].Source) {
				t.Fatalf("payload mismatch: %#v", requests)
			}
			if requests[1].Payload["surfaceId"] != "page:test" || requests[1].Payload["paramsFile"] != nil || !reflect.DeepEqual(requests[1].Payload["args"], params["args"]) {
				t.Fatal(requests[1])
			}
		})
	}
}

func TestDesktopAwcpParamsFileErrorsNeverSend(t *testing.T) {
	root := t.TempDir()
	cases := []struct{ name, data, code, actual string }{
		{"empty", "", "desktop_cdp_params_file_invalid_json", "invalid JSON"},
		{"syntax", "{\n \"revision\": \"SECRET\",\n \"action\": }", "desktop_cdp_params_file_invalid_json", "invalid JSON"},
		{"trailing", "{} {}", "desktop_cdp_params_file_invalid_json", "invalid JSON"},
		{"utf8", "{\"args\":\"\xff\"}", "desktop_cdp_params_file_invalid_json", "invalid UTF-8"},
		{"array", "[]", "desktop_cdp_params_file_invalid_json", "array"},
		{"null", "null", "desktop_cdp_params_file_invalid_json", "null"},
		{"string", "\"\"", "desktop_cdp_params_file_invalid_json", "string"},
		{"number", "1", "desktop_cdp_params_file_invalid_json", "number"},
		{"boolean", "true", "desktop_cdp_params_file_invalid_json", "boolean"},
		{"wrapped", `{"method":"AWCP.invoke","params":{"revision":"r","action":"a","args":{}}}`, "invalid_args", "object"},
		{"missing revision", `{"action":"a","args":{}}`, "invalid_args", "missing"},
		{"missing action", `{"revision":"r","args":{}}`, "invalid_args", "missing"},
		{"missing args", `{"revision":"r","action":"a"}`, "invalid_args", "missing"},
		{"string args", `{"revision":"r","action":"a","args":""}`, "invalid_args", "string"},
		{"encoded args", `{"revision":"r","action":"a","args":"{}"}`, "invalid_args", "string"},
		{"null args", `{"revision":"r","action":"a","args":null}`, "invalid_args", "null"},
		{"number args", `{"revision":"r","action":"a","args":1}`, "invalid_args", "number"},
		{"boolean args", `{"revision":"r","action":"a","args":false}`, "invalid_args", "boolean"},
		{"array args", `{"revision":"r","action":"a","args":[]}`, "invalid_args", "array"},
		{"extra", `{"revision":"r","action":"a","args":{},"secret":"SECRET"}`, "invalid_args", "object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
			mustWriteFile(t, filepath.Join(root, "params.json"), tc.data)
			result, err := executor.Invoke(context.Background(), "desktop_cdp", map[string]any{"method": desktopAwcpInvokeMethod, "paramsFile": "params.json"}, execCtx)
			_, requests := invoker.snapshots()
			if err != nil || result.Error != tc.code || len(requests) != 0 {
				t.Fatalf("%#v %v requests=%#v", result, err, requests)
			}
			details := result.Structured["details"].(map[string]any)
			if details["executionStarted"] != false || details["stage"] != "platform_parse" || details["parameterSource"] != "paramsFile" || details["actualType"] != tc.actual || details["path"] == nil {
				t.Fatal(details)
			}
			if strings.Contains(result.Output, "SECRET") || !strings.Contains(result.Output, "修正") {
				t.Fatal(result.Output)
			}
			if tc.name == "syntax" && (details["line"] != 3 || details["column"] != 12) {
				t.Fatal(details)
			}
		})
	}
}

func TestDesktopAwcpParameterSources(t *testing.T) {
	for _, args := range []map[string]any{
		{}, {"params": nil, "paramsFile": "missing"}, {"params": map[string]any{}, "paramsFile": "missing"},
		{"paramsFile": nil}, {"paramsFile": 23}, {"paramsFile": ""}, {"params": ""}, {"params": "{}"},
		{"paramsFile": "missing", "requestId": "forged"},
	} {
		executor, execCtx, invoker := desktopCDPParamsTestRuntime(t.TempDir())
		args["method"] = desktopAwcpInvokeMethod
		result, err := executor.Invoke(context.Background(), "desktop_cdp", args, execCtx)
		_, requests := invoker.snapshots()
		if err != nil || result.Error != "invalid_args" || len(requests) != 0 {
			t.Fatalf("%#v %v %#v", result, err, requests)
		}
	}
}

func TestDesktopAwcpParamsFileReadFailures(t *testing.T) {
	for _, mode := range []string{"missing", "unreadable", "directory", "size", "alias escape", "manual"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
			path := filepath.Join(root, "params.json")
			mustWriteFile(t, path, `{"revision":"r","action":"a","args":{}}`)
			args := map[string]any{"method": desktopAwcpInvokeMethod, "paramsFile": path}
			code := "desktop_cdp_params_file_read_failed"
			switch mode {
			case "missing":
				args["paramsFile"] = filepath.Join(root, "missing.json")
			case "unreadable":
				if err := os.Chmod(path, 0); err != nil {
					t.Skip(err)
				}
				defer os.Chmod(path, 0600)
				if f, err := os.Open(path); err == nil {
					f.Close()
					t.Skip("host can read mode 000")
				}
			case "directory":
				args["paramsFile"] = root
				code = "desktop_cdp_params_file_invalid_file"
			case "size":
				executor.cfg.FileTools.MaxReadBytes = 5
				code = "desktop_cdp_params_file_too_large"
			case "alias escape":
				args["paramsFile"] = "@workspace/../params.json"
				code = "desktop_cdp_params_file_invalid_path"
			case "manual":
				args["method"] = desktopAwcpGetManualMethod
				code = "invalid_args"
			}
			result, err := executor.Invoke(context.Background(), "desktop_cdp", args, execCtx)
			_, requests := invoker.snapshots()
			if err != nil || result.Error != code || len(requests) != 0 {
				t.Fatalf("%#v %v %#v", result, err, requests)
			}
		})
	}
}

func TestDesktopAwcpParamsFileTransportFailureDoesNotReplay(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, context.Canceled, ErrClientDisconnected} {
		executor, execCtx, _ := desktopCDPParamsTestRuntime(t.TempDir())
		mustWriteFile(t, filepath.Join(execCtx.Session.WorkspaceRoot, "params.json"), `{"revision":"r","action":"a","args":{}}`)
		invoker := &scriptedClientRequestInvoker{err: failure}
		executor.clientRequest = invoker
		result, err := executor.Invoke(context.Background(), "desktop_cdp", map[string]any{"method": desktopAwcpInvokeMethod, "paramsFile": "params.json"}, execCtx)
		if err != nil || result.ExitCode != -1 || invoker.calls != 1 {
			t.Fatalf("%#v %v calls=%d", result, err, invoker.calls)
		}
		if strings.Contains(result.Output, "platform_parse") || strings.Contains(result.Output, "修正") || strings.Contains(result.Output, `"executionStarted":false`) {
			t.Fatal(result.Output)
		}
	}
}

func TestDesktopAwcpParamsFileExactLimitAndWorkspace(t *testing.T) {
	root := t.TempDir()
	executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
	data := `{"revision":"r","action":"a","args":{}}`
	mustWriteFile(t, filepath.Join(root, "params.json"), data)
	args := map[string]any{"method": desktopAwcpInvokeMethod, "paramsFile": "params.json"}
	executor.cfg.FileTools.MaxReadBytes = len(data)
	result, err := executor.Invoke(context.Background(), "desktop_cdp", args, execCtx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("%#v %v", result, err)
	}
	executor.cfg.FileTools.MaxReadBytes = len(data) - 1
	result, err = executor.Invoke(context.Background(), "desktop_cdp", args, execCtx)
	if err != nil || result.Error != "desktop_cdp_params_file_too_large" {
		t.Fatalf("%#v %v", result, err)
	}
	execCtx.Session.WorkspaceRoot = ""
	result, err = executor.Invoke(context.Background(), "desktop_cdp", args, execCtx)
	if err != nil || result.Error != "workspace_unavailable" {
		t.Fatalf("%#v %v", result, err)
	}
	_, requests := invoker.snapshots()
	if len(requests) != 1 {
		t.Fatal(requests)
	}
}
