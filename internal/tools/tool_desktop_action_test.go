package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

type recordingWebClientRequestInvoker struct {
	target   WebClientTarget
	request  WebClientActionRequest
	response WebClientActionResponse
	err      error
	calls    int
}

func webClientResponseCode(code int) *int {
	return &code
}

func (r *recordingWebClientRequestInvoker) InvokeWebClientAction(
	_ context.Context,
	target WebClientTarget,
	request WebClientActionRequest,
) (WebClientActionResponse, error) {
	r.calls++
	r.target = target
	r.request = request
	return r.response, r.err
}

func TestInvokeDesktopActionCallsBridge(t *testing.T) {
	var got desktopActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"action":"desktop.theme.set","result":{"themeMode":"dark","resolvedTheme":"dark"}}`))
	}))
	defer server.Close()

	result, err := newDesktopTestExecutor(server.URL, "").invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.theme.set",
		"args": map[string]any{
			"themeMode": "dark",
		},
	}, &ExecutionContext{Session: QuerySession{
		RunID:    "run-1",
		ChatID:   "chat-1",
		AgentKey: "desktopAssistant",
	}})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.Action != "desktop.theme.set" {
		t.Fatalf("unexpected action: %s", got.Action)
	}
	if got.Args["themeMode"] != "dark" {
		t.Fatalf("unexpected args: %#v", got.Args)
	}
	if got.Source.RunID != "run-1" || got.Source.ChatID != "chat-1" || got.Source.AgentKey != "desktopAssistant" {
		t.Fatalf("unexpected source: %#v", got.Source)
	}
}

func TestInvokeDesktopActionForwardsWebClientActionAsFlatRequest(t *testing.T) {
	invoker := &recordingWebClientRequestInvoker{
		response: WebClientActionResponse{
			Frame: "response",
			Type:  webClientSidebarSetState,
			ID:    "web-request-1",
			Code:  webClientResponseCode(0),
			Msg:   "success",
			Data:  json.RawMessage(`{"applied":true,"sidebar":"right","open":true,"tab":"debug"}`),
		},
	}
	executor := newDesktopTestExecutor("http://desktop-bridge.invalid", "").WithWebClientRequestInvoker(invoker)
	target := WebClientTarget{
		BoundaryKey: "device:web-1",
		SurfaceID:   "surface-1",
	}
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId":           "web-request-1",
		"action":              webClientSidebarSetState,
		"confirmationSummary": "must not be forwarded",
		"args": map[string]any{
			"sidebar": "right",
			"open":    true,
			"tab":     "debug",
		},
	}, &ExecutionContext{Session: QuerySession{WebClientTarget: target}})
	if err != nil {
		t.Fatalf("invoke webclient action: %v", err)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("expected successful webclient result, got %#v", result)
	}
	if invoker.calls != 1 || !reflect.DeepEqual(invoker.target, target) {
		t.Fatalf("unexpected invocation target: calls=%d target=%#v", invoker.calls, invoker.target)
	}
	if invoker.request.ID != "web-request-1" || invoker.request.Type != webClientSidebarSetState {
		t.Fatalf("unexpected flat webclient request: %#v", invoker.request)
	}
	if invoker.request.Payload["sidebar"] != "right" || invoker.request.Payload["open"] != true || invoker.request.Payload["tab"] != "debug" {
		t.Fatalf("unexpected webclient payload: %#v", invoker.request.Payload)
	}
	if _, exists := invoker.request.Payload["confirmationSummary"]; exists {
		t.Fatalf("confirmationSummary must not be forwarded: %#v", invoker.request.Payload)
	}
	if result.Structured["applied"] != true || result.Structured["tab"] != "debug" {
		t.Fatalf("unexpected structured result: %#v", result.Structured)
	}
}

func TestInvokeDesktopActionValidatesWebClientSidebarArgsBeforeSending(t *testing.T) {
	invoker := &recordingWebClientRequestInvoker{}
	executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"action": webClientSidebarSetState,
		"args": map[string]any{
			"sidebar": "left",
			"open":    true,
			"tab":     "debug",
		},
	}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
	if err != nil {
		t.Fatalf("invoke webclient action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "invalid_args" {
		t.Fatalf("expected invalid_args, got %#v", result)
	}
	if invoker.calls != 0 {
		t.Fatalf("invalid action must not reach webclient invoker")
	}
}

func TestInvokeDesktopActionForwardsWebClientOpenURL(t *testing.T) {
	invoker := &recordingWebClientRequestInvoker{
		response: WebClientActionResponse{
			Frame: "response",
			Type:  webClientSidebarOpenURL,
			ID:    "web-url-1",
			Code:  webClientResponseCode(0),
			Msg:   "success",
			Data:  json.RawMessage(`{"applied":true,"sidebar":"right","open":true,"tab":"web","url":"https://www.sina.com.cn/","title":"新浪"}`),
		},
	}
	executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId": "web-url-1",
		"action":    webClientSidebarOpenURL,
		"args": map[string]any{
			"url":   "www.sina.com.cn",
			"title": "新浪",
		},
	}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
	if err != nil {
		t.Fatalf("invoke webclient action: %v", err)
	}
	if result.ExitCode != 0 || result.Structured["url"] != "https://www.sina.com.cn/" {
		t.Fatalf("unexpected webclient openUrl result: %#v", result)
	}
	if invoker.request.Type != webClientSidebarOpenURL ||
		invoker.request.Payload["url"] != "www.sina.com.cn" ||
		invoker.request.Payload["title"] != "新浪" {
		t.Fatalf("unexpected flat openUrl request: %#v", invoker.request)
	}
}

func TestInvokeDesktopActionForwardsWebClientRefreshURL(t *testing.T) {
	invoker := &recordingWebClientRequestInvoker{
		response: WebClientActionResponse{
			Frame: "response",
			Type:  webClientSidebarRefreshURL,
			ID:    "web-refresh-1",
			Code:  webClientResponseCode(0),
			Msg:   "success",
			Data:  json.RawMessage(`{"applied":true,"sidebar":"right","open":false,"tab":null,"url":"https://example.com/"}`),
		},
	}
	executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId": "web-refresh-1",
		"action":    webClientSidebarRefreshURL,
		"args": map[string]any{
			"url": "example.com",
		},
	}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
	if err != nil {
		t.Fatalf("invoke webclient action: %v", err)
	}
	if result.ExitCode != 0 || result.Structured["applied"] != true || result.Structured["url"] != "https://example.com/" {
		t.Fatalf("unexpected webclient refreshUrl result: %#v", result)
	}
	if invoker.request.Type != webClientSidebarRefreshURL || invoker.request.Payload["url"] != "example.com" {
		t.Fatalf("unexpected flat refreshUrl request: %#v", invoker.request)
	}
}

func TestInvokeDesktopActionRejectsInvalidWebClientOpenURLArgs(t *testing.T) {
	tests := []map[string]any{
		{},
		{"url": "javascript:alert(1)"},
		{"url": "//example.com"},
		{"url": "https://user:secret@example.com"},
		{"url": "https://example.com", "title": true},
		{"url": "https://example.com", "unexpected": true},
	}
	for _, actionArgs := range tests {
		invoker := &recordingWebClientRequestInvoker{}
		executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
		result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
			"action": webClientSidebarOpenURL,
			"args":   actionArgs,
		}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
		if err != nil {
			t.Fatalf("invoke webclient action: %v", err)
		}
		if result.ExitCode != -1 || result.Error != "invalid_args" {
			t.Fatalf("expected invalid_args for %#v, got %#v", actionArgs, result)
		}
		if invoker.calls != 0 {
			t.Fatalf("invalid openUrl args reached invoker: %#v", actionArgs)
		}
	}
}

func TestInvokeDesktopActionRejectsInvalidWebClientRefreshURLArgs(t *testing.T) {
	tests := []map[string]any{
		{},
		{"url": "javascript:alert(1)"},
		{"url": "//example.com"},
		{"url": "https://user:secret@example.com"},
		{"url": "https://example.com", "title": "not supported"},
	}
	for _, actionArgs := range tests {
		invoker := &recordingWebClientRequestInvoker{}
		executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
		result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
			"action": webClientSidebarRefreshURL,
			"args":   actionArgs,
		}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
		if err != nil {
			t.Fatalf("invoke webclient action: %v", err)
		}
		if result.ExitCode != -1 || result.Error != "invalid_args" {
			t.Fatalf("expected invalid_args for %#v, got %#v", actionArgs, result)
		}
		if invoker.calls != 0 {
			t.Fatalf("invalid refreshUrl args reached invoker: %#v", actionArgs)
		}
	}
}

func TestInvokeDesktopActionRejectsNonObjectWebClientArgs(t *testing.T) {
	invoker := &recordingWebClientRequestInvoker{}
	executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"action": webClientSidebarGetState,
		"args":   "not-an-object",
	}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
	if err != nil {
		t.Fatalf("invoke webclient action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "invalid_args" {
		t.Fatalf("expected invalid_args, got %#v", result)
	}
	if invoker.calls != 0 {
		t.Fatalf("invalid action must not reach webclient invoker")
	}
}

func TestInvokeDesktopActionRejectsInvalidWebClientResponseFrames(t *testing.T) {
	tests := []struct {
		name     string
		response WebClientActionResponse
	}{
		{
			name: "mismatched response type",
			response: WebClientActionResponse{
				Frame: "response",
				Type:  webClientSidebarGetState,
				ID:    "web-request-1",
				Code:  webClientResponseCode(0),
				Data:  json.RawMessage(`{}`),
			},
		},
		{
			name: "error without positive code",
			response: WebClientActionResponse{
				Frame: "error",
				Type:  "invalid_request",
				ID:    "web-request-1",
				Code:  webClientResponseCode(0),
			},
		},
		{
			name: "response without code",
			response: WebClientActionResponse{
				Frame: "response",
				Type:  webClientSidebarSetState,
				ID:    "web-request-1",
				Data:  json.RawMessage(`{}`),
			},
		},
		{
			name: "non object response data",
			response: WebClientActionResponse{
				Frame: "response",
				Type:  webClientSidebarSetState,
				ID:    "web-request-1",
				Code:  webClientResponseCode(0),
				Data:  json.RawMessage(`[]`),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invoker := &recordingWebClientRequestInvoker{response: test.response}
			executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
			result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
				"requestId": "web-request-1",
				"action":    webClientSidebarSetState,
				"args": map[string]any{
					"sidebar": "right",
					"open":    true,
				},
			}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
			if err != nil {
				t.Fatalf("invoke webclient action: %v", err)
			}
			if result.ExitCode != -1 || result.Error != "desktop_action_invalid_client_response" {
				t.Fatalf("expected invalid client response, got %#v", result)
			}
		})
	}
}

func TestInvokeDesktopActionMapsWebClientProviderFailures(t *testing.T) {
	tests := []struct {
		name     string
		response WebClientActionResponse
		err      error
		wantCode string
	}{
		{
			name:     "timeout",
			err:      context.DeadlineExceeded,
			wantCode: "desktop_action_client_timeout",
		},
		{
			name:     "disconnect",
			err:      ErrWebClientDisconnected,
			wantCode: "desktop_action_client_disconnected",
		},
		{
			name:     "target unavailable",
			err:      ErrWebClientTargetUnavailable,
			wantCode: "desktop_action_target_unavailable",
		},
		{
			name: "client rejection",
			response: WebClientActionResponse{
				Frame: "error",
				Type:  "unknown_request_type",
				ID:    "web-request-1",
				Code:  webClientResponseCode(404),
				Msg:   "unknown request type",
			},
			wantCode: "desktop_action_client_rejected",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invoker := &recordingWebClientRequestInvoker{
				response: test.response,
				err:      test.err,
			}
			executor := (&RuntimeToolExecutor{}).WithWebClientRequestInvoker(invoker)
			result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
				"requestId": "web-request-1",
				"action":    webClientSidebarGetState,
				"args":      map[string]any{},
			}, &ExecutionContext{Session: QuerySession{WebClientTarget: WebClientTarget{SessionID: "ws-1"}}})
			if err != nil {
				t.Fatalf("invoke webclient action: %v", err)
			}
			if result.ExitCode != -1 || result.Error != test.wantCode {
				t.Fatalf("expected %s, got %#v", test.wantCode, result)
			}
		})
	}
}

func TestInvokeDesktopCDPCallsBridge(t *testing.T) {
	var got desktopCDPRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"method":"Runtime.evaluate","result":{"value":42}}`))
	}))
	defer server.Close()

	result, err := newDesktopTestExecutor("", server.URL).invokeDesktopCDP(context.Background(), map[string]any{
		"requestId": "req-cdp",
		"method":    "Runtime.evaluate",
		"targetId":  "target-1",
		"sessionId": "session-1",
		"surfaceId": "surface-1",
		"params": map[string]any{
			"expression": "6 * 7",
		},
	}, &ExecutionContext{Session: QuerySession{
		RunID:    "run-cdp",
		ChatID:   "chat-cdp",
		AgentKey: "desktopAssistant",
	}})
	if err != nil {
		t.Fatalf("invoke desktop cdp: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.RequestID != "req-cdp" || got.Method != "Runtime.evaluate" {
		t.Fatalf("unexpected cdp request: %#v", got)
	}
	if got.TargetID != "target-1" || got.SessionID != "session-1" || got.SurfaceID != "surface-1" {
		t.Fatalf("unexpected target routing: %#v", got)
	}
	if got.Params["expression"] != "6 * 7" {
		t.Fatalf("unexpected params: %#v", got.Params)
	}
	if got.Source.RunID != "run-cdp" || got.Source.ChatID != "chat-cdp" || got.Source.AgentKey != "desktopAssistant" {
		t.Fatalf("unexpected source: %#v", got.Source)
	}
	response := result.Structured["response"].(map[string]any)
	resultNode := response["result"].(map[string]any)
	if resultNode["value"] != float64(42) {
		t.Fatalf("unexpected structured cdp result: %#v", result.Structured)
	}
}

func TestInvokeDesktopCDPNormalizesStringBooleanParams(t *testing.T) {
	var got desktopCDPRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"method":"Runtime.evaluate","result":{"value":42}}`))
	}))
	defer server.Close()

	result, err := newDesktopTestExecutor("", server.URL).invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Runtime.evaluate",
		"params": map[string]any{
			"expression":    "document.title",
			"returnByValue": "true",
			"awaitPromise":  "false",
		},
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop cdp: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.Params["expression"] != "document.title" {
		t.Fatalf("expression should remain a string, got %#v", got.Params["expression"])
	}
	if got.Params["returnByValue"] != true {
		t.Fatalf("returnByValue should be boolean true, got %#v", got.Params["returnByValue"])
	}
	if got.Params["awaitPromise"] != false {
		t.Fatalf("awaitPromise should be boolean false, got %#v", got.Params["awaitPromise"])
	}
}

func TestInvokeDesktopCDPCaptureScreenshotSavesImageAndOmitsBase64(t *testing.T) {
	png := testDesktopScreenshotPNG(t)
	encoded := base64.StdEncoding.EncodeToString(png)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"method": "Page.captureScreenshot",
			"result": map[string]any{"data": encoded},
		})
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = chatsDir
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
		"params": map[string]any{"format": "png"},
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful screenshot save, got exit=%d output=%s", result.ExitCode, result.Output)
	}
	if strings.Contains(result.Output, encoded) {
		t.Fatalf("screenshot base64 leaked into output: %s", result.Output)
	}

	response := result.Structured["response"].(map[string]any)
	resultNode := response["result"].(map[string]any)
	data := resultNode["data"].(map[string]any)
	referenceName := data["referenceName"].(string)
	if !strings.HasPrefix(referenceName, "desktop-cdp-screenshot-") || !strings.HasSuffix(referenceName, ".png") {
		t.Fatalf("unexpected reference name: %q", referenceName)
	}
	filePath := data["filePath"].(string)
	if filePath != filepath.Join(chatsDir, "chat-cdp", referenceName) {
		t.Fatalf("unexpected file path: %q", filePath)
	}
	written, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read written screenshot: %v", err)
	}
	if !bytes.Equal(written, png) {
		t.Fatalf("written screenshot bytes differ")
	}
	sha := sha256.Sum256(png)
	if data["sha256"] != hex.EncodeToString(sha[:]) || data["mimeType"] != "image/png" || data["sizeBytes"] != len(png) {
		t.Fatalf("unexpected screenshot metadata: %#v", data)
	}
	if data["saved"] != true || data["dataOmitted"] != true {
		t.Fatalf("expected saved/dataOmitted flags: %#v", data)
	}
	visionImage := data["visionRecognizeImage"].(map[string]any)
	if visionImage["reference_name"] != referenceName {
		t.Fatalf("unexpected vision image payload: %#v", visionImage)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotRequiresChatContext(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(testDesktopScreenshotPNG(t))
	server := desktopScreenshotServer(t, encoded, true)
	defer server.Close()

	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = t.TempDir()
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_cdp_screenshot_context_unavailable" {
		t.Fatalf("expected context error, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
	if strings.Contains(result.Output, encoded) {
		t.Fatalf("screenshot base64 leaked into context error output: %s", result.Output)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotRejectsInvalidBase64(t *testing.T) {
	encoded := "not-valid-base64-data-that-must-not-leak"
	server := desktopScreenshotServer(t, encoded, true)
	defer server.Close()

	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = t.TempDir()
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_cdp_screenshot_decode_failed" {
		t.Fatalf("expected decode error, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
	if strings.Contains(result.Output, encoded) {
		t.Fatalf("invalid screenshot base64 leaked into output: %s", result.Output)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotRequiresResultData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"method": "Page.captureScreenshot",
			"result": map[string]any{},
		})
	}))
	defer server.Close()

	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = t.TempDir()
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_cdp_screenshot_data_missing" {
		t.Fatalf("expected data missing error, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
}

func TestInvokeDesktopCDPCaptureScreenshotDoesNotSaveBridgeFailure(t *testing.T) {
	server := desktopScreenshotServer(t, "", false)
	defer server.Close()

	chatsDir := t.TempDir()
	executor := newDesktopTestExecutor("", server.URL)
	executor.cfg.Paths.ChatsDir = chatsDir
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Page.captureScreenshot",
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-cdp"}})
	if err != nil {
		t.Fatalf("invoke desktop cdp screenshot: %v", err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("expected bridge failure exit code, got %d output=%s", result.ExitCode, result.Output)
	}
	if _, err := os.Stat(filepath.Join(chatsDir, "chat-cdp")); !os.IsNotExist(err) {
		t.Fatalf("expected no screenshot directory on bridge failure, stat err=%v", err)
	}
}

func TestInvokeDesktopActionRejectsUnknownAction(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.unlisted.anything",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "unknown_action" {
		t.Fatalf("expected unknown_action failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
}

func TestInvokeDesktopActionAllowsCurrentDesktopActions(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got desktopActionRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requested = append(requested, got.Action)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"action": got.Action,
			"result": map[string]any{"ok": true},
		})
	}))
	defer server.Close()

	actions := []string{
		"desktop.general.deviceName",
		"desktop.theme.get",
		"desktop.theme.set",
		"desktop.locale.get",
		"desktop.locale.set",
		"desktop.copilot.getPagePreferences",
		"desktop.copilot.setPagePreference",
		"desktop.chatWorkPanel.getState",
		"desktop.chatWorkPanel.openTab",
		"desktop.web.listSurfaces",
		"desktop.webapp.getStatus",
		"desktop.website.list",
		"desktop.pet.show",
		"desktop.pet.state",
	}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			result, err := newDesktopTestExecutor(server.URL, "").invokeDesktopAction(context.Background(), map[string]any{
				"action": action,
			}, &ExecutionContext{})
			if err != nil {
				t.Fatalf("invoke desktop action: %v", err)
			}
			if result.ExitCode != 0 {
				t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
			}
		})
	}
	if len(requested) != len(actions) {
		t.Fatalf("expected bridge to receive %d actions, got %d: %#v", len(actions), len(requested), requested)
	}
}

func TestInvokeDesktopActionRejectsLegacyAndUnsupportedActions(t *testing.T) {
	for _, action := range []string{
		"desktop.setting.getState",
		"desktop.setting.validatePatch",
		"desktop.setting.previewPatch",
		"desktop.setting.applyPatch",
		"desktop.settings.getState",
		"desktop.agents.listAgents",
		"desktop.automations.listAutomations",
		"desktop.help.searchTopics",
		"desktop.web.list",
		"desktop.web.website.list",
		"desktop.web.webapp.getStatus",
		"desktop.webapp.installAndOpen",
		"desktop.webapp.checkPrerequisites",
		"desktop.webapp.getPublishInfo",
		"desktop.webapp.selectDirectory",
	} {
		t.Run(action, func(t *testing.T) {
			result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
				"action": action,
			}, &ExecutionContext{})
			if err != nil {
				t.Fatalf("invoke desktop action: %v", err)
			}
			if result.ExitCode != -1 || result.Error != "unknown_action" {
				t.Fatalf("expected unknown_action failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
			}
		})
	}
}

func TestInvokeDesktopActionRejectsPageActions(t *testing.T) {
	for _, action := range []string{
		"desktop.page.readCurrent",
		"desktop.embeddedWeb.readPageData",
	} {
		t.Run(action, func(t *testing.T) {
			result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
				"action": action,
			}, &ExecutionContext{})
			if err != nil {
				t.Fatalf("invoke desktop action: %v", err)
			}
			if result.ExitCode != -1 || result.Error != "unknown_action" {
				t.Fatalf("expected unknown_action failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
			}
		})
	}
}

func TestDesktopActionAllowlistMatchesToolSchema(t *testing.T) {
	want := []string{
		"desktop.controlCenter.getServiceDetail",
		"desktop.controlCenter.getServiceLogsMeta",
		"desktop.controlCenter.getServiceStatus",
		"desktop.controlCenter.initializeService",
		"desktop.controlCenter.installService",
		"desktop.controlCenter.listServices",
		"desktop.controlCenter.openLogViewer",
		"desktop.controlCenter.readServiceLog",
		"desktop.controlCenter.restartService",
		"desktop.controlCenter.startService",
		"desktop.controlCenter.stopService",
		"desktop.chatWorkPanel.activateTab",
		"desktop.chatWorkPanel.close",
		"desktop.chatWorkPanel.closeTab",
		"desktop.chatWorkPanel.getState",
		"desktop.chatWorkPanel.open",
		"desktop.chatWorkPanel.openTab",
		"desktop.copilot.getPagePreferences",
		"desktop.copilot.setPagePreference",
		"desktop.general.deviceName",
		"desktop.help.openTopic",
		"desktop.kanban.createIssue",
		"desktop.kanban.deleteIssue",
		"desktop.kanban.getIssue",
		"desktop.kanban.listIssues",
		"desktop.kanban.moveIssue",
		"desktop.kanban.updateIssue",
		"desktop.locale.get",
		"desktop.locale.set",
		"desktop.market.applySettingsPatch",
		"desktop.market.deleteSandboxImage",
		"desktop.market.exportSandboxImage",
		"desktop.market.getItemDetail",
		"desktop.market.getSettings",
		"desktop.market.importSandboxImage",
		"desktop.market.importSkill",
		"desktop.market.installItem",
		"desktop.market.listItems",
		"desktop.market.previewSettingsPatch",
		"desktop.market.refresh",
		"desktop.market.uninstallItem",
		"desktop.market.updateItem",
		"desktop.market.validateSettings",
		"desktop.navigate.toRoute",
		"desktop.pet.hide",
		"desktop.pet.list",
		"desktop.pet.set",
		"desktop.pet.show",
		"desktop.pet.state",
		"desktop.site.list",
		"desktop.theme.get",
		"desktop.theme.set",
		"desktop.web.activateSurface",
		"desktop.web.closeTab",
		"desktop.web.getSurfaceState",
		"desktop.web.goBack",
		"desktop.web.listSurfaces",
		"desktop.web.navigate",
		"desktop.web.openTab",
		"desktop.web.refreshSurface",
		"desktop.web.reload",
		"desktop.web.switchTab",
		"desktop.webapp.checkRuntime",
		"desktop.webapp.getPublishStatus",
		"desktop.webapp.getStatus",
		"desktop.webapp.install",
		"desktop.webapp.open",
		"desktop.webapp.publish",
		"desktop.webapp.restart",
		"desktop.webapp.start",
		"desktop.webapp.stop",
		"desktop.webapp.uninstall",
		"desktop.webapp.unpublish",
		"desktop.webapp.updatePreferences",
		"desktop.website.add",
		"desktop.website.list",
		"desktop.website.open",
		"desktop.website.remove",
		"desktop.website.update",
		"webclient.sidebar.getState",
		"webclient.sidebar.openUrl",
		"webclient.sidebar.refreshUrl",
		"webclient.sidebar.setState",
	}
	sort.Strings(want)

	gotAllowlist := sortedDesktopActionAllowlist(t)
	if !reflect.DeepEqual(gotAllowlist, want) {
		t.Fatalf("desktop action allowlist mismatch\nwant: %#v\n got: %#v", want, gotAllowlist)
	}

	gotSchema := sortedToolPropertyEnum(t, "desktop_action", "action")
	if !reflect.DeepEqual(gotSchema, want) {
		t.Fatalf("desktop action schema enum mismatch\nwant: %#v\n got: %#v", want, gotSchema)
	}
}

func TestDesktopCDPMethodSchemaUsesRecommendedEnum(t *testing.T) {
	want := []string{
		"DOM.getBoxModel",
		"DOM.getDocument",
		"DOM.getOuterHTML",
		"DOM.querySelector",
		"DOM.querySelectorAll",
		"Input.dispatchKeyEvent",
		"Input.dispatchMouseEvent",
		"Input.insertText",
		"Network.disable",
		"Network.enable",
		"Page.bringToFront",
		"Page.captureScreenshot",
		"Page.enable",
		"Page.navigate",
		"Page.reload",
		"Runtime.evaluate",
		"Target.getCurrentTarget",
		"Target.getTargets",
		"Target.closeTarget",
	}
	sort.Strings(want)

	got := sortedToolPropertyEnum(t, "desktop_cdp", "method")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop_cdp method enum mismatch\nwant: %#v\n got: %#v", want, got)
	}
	for _, endpoint := range []string{"/json/version", "/json", "/json/list"} {
		if enumContainsString(got, endpoint) {
			t.Fatalf("desktop_cdp method enum must not include HTTP endpoint %q", endpoint)
		}
	}
}

func TestInvokeDesktopActionRequiresConfiguredBridge(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.controlCenter.listServices",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_action_bridge_not_configured" {
		t.Fatalf("expected bridge not configured failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
}

func sortedDesktopActionAllowlist(t *testing.T) []string {
	t.Helper()
	allowlist, err := getDesktopActionAllowlist()
	if err != nil {
		t.Fatalf("load desktop action allowlist: %v", err)
	}
	values := make([]string, 0, len(allowlist))
	for action := range allowlist {
		values = append(values, action)
	}
	sort.Strings(values)
	return values
}

func sortedToolPropertyEnum(t *testing.T, toolName string, propertyName string) []string {
	t.Helper()
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tools: %v", err)
	}
	for _, def := range defs {
		if def.Name != toolName {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s parameters missing properties: %#v", toolName, def.Parameters)
		}
		property, ok := properties[propertyName].(map[string]any)
		if !ok {
			t.Fatalf("%s property %s missing: %#v", toolName, propertyName, properties[propertyName])
		}
		enum, ok := property["enum"].([]any)
		if !ok {
			t.Fatalf("%s property %s missing enum: %#v", toolName, propertyName, property)
		}
		values := make([]string, 0, len(enum))
		for _, item := range enum {
			value, ok := item.(string)
			if !ok {
				t.Fatalf("%s property %s enum contains non-string: %#v", toolName, propertyName, item)
			}
			values = append(values, value)
		}
		sort.Strings(values)
		return values
	}
	t.Fatalf("tool %s not found", toolName)
	return nil
}

func enumContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testDesktopScreenshotPNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode test png: %v", err)
	}
	return data
}

func desktopScreenshotServer(t *testing.T, data string, ok bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{
			"ok":     ok,
			"method": "Page.captureScreenshot",
		}
		if ok {
			payload["result"] = map[string]any{"data": data}
		} else {
			w.WriteHeader(http.StatusBadRequest)
			payload["error"] = map[string]any{"code": "cdp_failed", "message": "capture failed"}
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func newDesktopTestExecutor(actionURL string, cdpURL string) *RuntimeToolExecutor {
	return &RuntimeToolExecutor{cfg: config.Config{Desktop: config.DesktopConfig{
		Action: config.DesktopBridgeConfig{
			BridgeURL:      actionURL,
			RequestTimeout: 20,
		},
		CDP: config.DesktopBridgeConfig{
			BridgeURL:      cdpURL,
			RequestTimeout: 20,
		},
	}}}
}
