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
	"time"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

type httpBackedClientRequestInvoker struct {
	actionURL string
	cdpURL    string
}

type scriptedClientRequestInvoker struct {
	frames      []ClientResponseFrame
	err         error
	calls       int
	target      ClientTarget
	request     ClientRequest
	deadline    time.Time
	hadDeadline bool
}

func (i *scriptedClientRequestInvoker) InvokeClientRequest(ctx context.Context, target ClientTarget, request ClientRequest, onFrame func(ClientResponseFrame) error) error {
	i.calls++
	i.target = target
	i.request = request
	i.deadline, i.hadDeadline = ctx.Deadline()
	for _, frame := range i.frames {
		if err := onFrame(frame); err != nil {
			return err
		}
	}
	return i.err
}

func TestDesktopReverseRequestUsesCallerToolDeadline(t *testing.T) {
	code := 0
	data, _ := json.Marshal(map[string]any{"ok": true, "action": "desktop.theme.get", "result": map[string]any{}})
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{
		Frame: "response", Type: desktopActionRequestType, ID: "deadline-request", Code: &code, Data: data,
	}}}
	executor := &RuntimeToolExecutor{
		cfg:           config.Config{RuntimeMode: config.RuntimeModeDesktop},
		clientRequest: invoker,
		clientTargets: emptyRunClientTargetStore{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	result, err := executor.invokeDesktopAction(ctx, map[string]any{
		"requestId": "deadline-request", "action": "desktop.theme.get", "args": map[string]any{},
	}, &ExecutionContext{})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("invoke desktop action: result=%#v err=%v", result, err)
	}
	if !invoker.hadDeadline || !invoker.deadline.Equal(wantDeadline) {
		t.Fatalf("reverse request deadline = %v, want caller tool deadline %v", invoker.deadline, wantDeadline)
	}
}

func TestDesktopReverseRequestPreservesNonRetryableClientMetadata(t *testing.T) {
	code := 409
	data, _ := json.Marshal(map[string]any{
		"retryable": false,
		"details":   map[string]any{"recovery": "reattach_source_chat"},
	})
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{
		Frame: "error", Type: "source_chat_not_ready", ID: "workpanel-rejected", Code: &code,
		Msg: "source_chat_not_ready: canonical Chat synchronization failed", Data: data,
	}}}
	executor := &RuntimeToolExecutor{
		cfg:           config.Config{RuntimeMode: config.RuntimeModeStandalone},
		clientRequest: invoker,
		clientTargets: emptyRunClientTargetStore{},
	}
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId": "workpanel-rejected",
		"action":    "desktop.workpanel.openWeb",
		"args":      map[string]any{"url": "https://example.test/document"},
	}, &ExecutionContext{Session: QuerySession{RunID: "run-1", ChatID: "chat-1", AgentKey: "agent-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "desktop_action_client_rejected" || result.ExitCode != -1 {
		t.Fatalf("unexpected rejected result: %#v", result)
	}
	details, _ := result.Structured["details"].(map[string]any)
	if details["clientErrorType"] != "source_chat_not_ready" || details["clientCode"] != 409 {
		t.Fatalf("client rejection identity was not preserved: %#v", details)
	}
	if details["retryable"] != false || details["recovery"] != "reattach_source_chat" {
		t.Fatalf("client retry metadata was not preserved: %#v", details)
	}
}

func TestDesktopRuntimeModeRoutingMatrix(t *testing.T) {
	code := 0
	inline, _ := json.Marshal(map[string]any{"ok": true, "action": "desktop.workpanel.getState", "result": map[string]any{"ok": true}})
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{
		Frame: "response", Type: desktopActionRequestType, ID: "standalone-workpanel", Code: &code, Data: inline,
	}}}
	executor := &RuntimeToolExecutor{
		cfg:           config.Config{RuntimeMode: config.RuntimeModeStandalone},
		clientRequest: invoker,
		clientTargets: emptyRunClientTargetStore{},
	}
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId": "standalone-workpanel", "action": "desktop.workpanel.getState", "args": map[string]any{},
	}, &ExecutionContext{})
	if err != nil || result.ExitCode != 0 || invoker.calls != 1 || invoker.request.Type != desktopActionRequestType {
		t.Fatalf("standalone WorkPanel route failed: result=%#v calls=%d request=%#v err=%v", result, invoker.calls, invoker.request, err)
	}

	unsupported, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.theme.get", "args": map[string]any{},
	}, &ExecutionContext{})
	if err != nil || unsupported.Error != "desktop_action_unsupported_runtime" || invoker.calls != 1 {
		t.Fatalf("standalone Desktop action was not rejected before dispatch: result=%#v calls=%d err=%v", unsupported, invoker.calls, err)
	}
	cdp, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": "Runtime.evaluate", "params": map[string]any{"expression": "1"},
	}, &ExecutionContext{})
	if err != nil || cdp.Error != "desktop_cdp_unsupported_runtime" || invoker.calls != 1 {
		t.Fatalf("standalone CDP was not rejected before dispatch: result=%#v calls=%d err=%v", cdp, invoker.calls, err)
	}
}

func TestDesktopReverseRequestDoesNotUseStaleSessionTargetWhenRunTargetIsMissing(t *testing.T) {
	runs := NewInMemoryRunManager()
	runs.Register(context.Background(), QuerySession{
		RunID: "run-without-reverse-target", ChatID: "chat-1", AgentKey: "agent-1", RunOwner: AgentRunOwner("agent-1", ""),
	})
	invoker := &scriptedClientRequestInvoker{}
	executor := &RuntimeToolExecutor{
		cfg:           config.Config{RuntimeMode: config.RuntimeModeStandalone},
		clientRequest: invoker,
		clientTargets: runs,
	}
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.workpanel.getState", "args": map[string]any{},
	}, &ExecutionContext{Session: QuerySession{
		RunID: "run-without-reverse-target", WebClientTarget: ClientTarget{SessionID: "stale-session"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "desktop_action_target_unavailable" || invoker.calls != 0 {
		t.Fatalf("stale session target was used: result=%#v calls=%d", result, invoker.calls)
	}
}

func TestDesktopReverseRequestUsesLatestRunTarget(t *testing.T) {
	code := 0
	data, _ := json.Marshal(map[string]any{"ok": true, "action": "desktop.workpanel.getState", "result": map[string]any{}})
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{
		Frame: "response", Type: desktopActionRequestType, ID: "latest-target", Code: &code, Data: data,
	}}}
	runs := NewInMemoryRunManager()
	stale := ClientTarget{SessionID: "ws-stale"}
	latest := ClientTarget{SessionID: "ws-latest"}
	runs.Register(context.Background(), QuerySession{
		RunID: "run-latest-target", ChatID: "chat-latest-target", AgentKey: "agent-1",
		RunOwner: AgentRunOwner("agent-1", ""), WebClientTarget: stale,
	})
	if !runs.BindClientTarget("run-latest-target", latest) {
		t.Fatal("bind latest target")
	}
	executor := (&RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeStandalone}}).
		WithClientRequestInvoker(invoker).
		WithClientTargetStore(runs)
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId": "latest-target", "action": "desktop.workpanel.getState", "args": map[string]any{},
	}, &ExecutionContext{Session: QuerySession{
		RunID: "run-latest-target", SubTaskID: "sub-agent-1", WebClientTarget: stale,
	}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("invoke latest target: result=%#v err=%v", result, err)
	}
	if invoker.target != latest {
		t.Fatalf("invocation target = %#v, want %#v", invoker.target, latest)
	}
}

func TestDesktopReverseRequestDoesNotInheritTargetForIndependentRootRun(t *testing.T) {
	runs := NewInMemoryRunManager()
	rootTarget := ClientTarget{SessionID: "ws-root"}
	runs.Register(context.Background(), QuerySession{
		RunID: "run-root", ChatID: "chat-root", AgentKey: "agent-1",
		RunOwner: AgentRunOwner("agent-1", ""), WebClientTarget: rootTarget,
	})
	runs.Register(context.Background(), QuerySession{
		RunID: "run-independent", ChatID: "chat-independent", AgentKey: "agent-1",
		RunOwner: AgentRunOwner("agent-1", ""),
	})
	invoker := &scriptedClientRequestInvoker{}
	executor := (&RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeStandalone}}).
		WithClientRequestInvoker(invoker).
		WithClientTargetStore(runs)
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.workpanel.getState", "args": map[string]any{},
	}, &ExecutionContext{Session: QuerySession{
		RunID: "run-independent",
		// A stale copied context must not bypass the authoritative runtime store.
		WebClientTarget: rootTarget,
	}})
	if err != nil {
		t.Fatal(err)
	}
	details, _ := result.Structured["details"].(map[string]any)
	if result.Error != "desktop_action_target_unavailable" || details["reason"] != "run_target_missing" || invoker.calls != 0 {
		t.Fatalf("independent run inherited target: result=%#v calls=%d", result, invoker.calls)
	}
}

func (i *httpBackedClientRequestInvoker) InvokeClientRequest(ctx context.Context, _ ClientTarget, request ClientRequest, onFrame func(ClientResponseFrame) error) error {
	endpoint := i.actionURL
	if request.Type == desktopCDPRequestType {
		endpoint = i.cdpURL
	}
	body, err := json.Marshal(request.Payload)
	if err != nil {
		return err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var data json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
		return err
	}
	code := 0
	return onFrame(ClientResponseFrame{Frame: "response", Type: request.Type, ID: request.ID, Code: &code, Msg: "success", Data: data})
}

type emptyRunClientTargetStore struct{}

func (emptyRunClientTargetStore) BindClientTarget(string, ClientTarget) bool {
	return false
}
func (emptyRunClientTargetStore) ResolveClientTarget(string) (ClientTarget, bool) {
	return ClientTarget{SessionID: "ws-desktop-test"}, true
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
		RunID:           "run-1",
		ChatID:          "chat-1",
		AgentKey:        "desktopAssistant",
		WebClientTarget: WebClientTarget{SessionID: "ws-desktop-test"},
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

func TestInvokeDesktopActionReassemblesValidatedStream(t *testing.T) {
	payload := []byte(`{"ok":true,"action":"desktop.controlCenter.readServiceLog","result":{"content":"streamed"}}`)
	streamID := "stream-json-1"
	frames := make([]ClientResponseFrame, 0, 3)
	for index, chunk := range [][]byte{payload[:len(payload)/2], payload[len(payload)/2:]} {
		event, _ := json.Marshal(desktopStreamEvent{
			Seq:       int64(index + 1),
			Type:      desktopResponseDeltaEventType,
			Timestamp: 1771888000000 + int64(index),
			Encoding:  "base64",
			Chunk:     base64.StdEncoding.EncodeToString(chunk),
		})
		frames = append(frames, ClientResponseFrame{Frame: "stream", ID: "stream-request", StreamID: streamID, Event: event})
	}
	code := 0
	terminal, _ := json.Marshal(map[string]any{
		"streamed":    true,
		"streamId":    streamID,
		"encoding":    "base64",
		"contentType": "application/json",
		"chunkCount":  2,
		"totalBytes":  len(payload),
	})
	frames = append(frames, ClientResponseFrame{Frame: "response", Type: desktopActionRequestType, ID: "stream-request", Code: &code, Data: terminal})
	executor := &RuntimeToolExecutor{
		cfg:           config.Config{RuntimeMode: config.RuntimeModeDesktop},
		clientRequest: &scriptedClientRequestInvoker{frames: frames},
		clientTargets: emptyRunClientTargetStore{},
	}
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId": "stream-request",
		"action":    "desktop.controlCenter.readServiceLog",
	}, &ExecutionContext{})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("streamed desktop action failed: result=%#v err=%v", result, err)
	}
	response := cloneDesktopMapValue(result.Structured["response"])
	resultNode := cloneDesktopMapValue(response["result"])
	if resultNode["content"] != "streamed" {
		t.Fatalf("unexpected streamed result: %#v", result.Structured)
	}
}

func TestInvokeDesktopActionRejectsInvalidStreamManifest(t *testing.T) {
	event, _ := json.Marshal(desktopStreamEvent{
		Seq:       1,
		Type:      desktopResponseDeltaEventType,
		Timestamp: 1771888000000,
		Encoding:  "base64",
		Chunk:     base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`)),
	})
	code := 0
	terminal, _ := json.Marshal(map[string]any{
		"streamed": true, "streamId": "wrong-stream", "encoding": "base64", "chunkCount": 1, "totalBytes": 11,
	})
	executor := &RuntimeToolExecutor{
		cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop},
		clientRequest: &scriptedClientRequestInvoker{frames: []ClientResponseFrame{
			{Frame: "stream", ID: "invalid-stream", StreamID: "stream-1", Event: event},
			{Frame: "response", Type: desktopActionRequestType, ID: "invalid-stream", Code: &code, Data: terminal},
		}},
		clientTargets: emptyRunClientTargetStore{},
	}
	result, err := executor.invokeDesktopAction(context.Background(), map[string]any{
		"requestId": "invalid-stream", "action": "desktop.controlCenter.readServiceLog",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke invalid stream: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_action_invalid_client_response" {
		t.Fatalf("expected invalid stream response, got %#v", result)
	}
}

func TestInvokeDesktopCDPStreamsScreenshotToChatFileAndCleansInvalidPartial(t *testing.T) {
	chatRoot := t.TempDir()
	chatID := "chat-stream-screenshot"
	png := testDesktopScreenshotPNG(t)
	streamID := "stream-screenshot-1"
	frames := make([]ClientResponseFrame, 0, 3)
	for index, chunk := range [][]byte{png[:len(png)/2], png[len(png)/2:]} {
		event, _ := json.Marshal(desktopStreamEvent{
			Seq:       int64(index + 1),
			Type:      desktopScreenshotDeltaEventType,
			Timestamp: 1771888000000 + int64(index),
			Encoding:  "base64",
			Chunk:     base64.StdEncoding.EncodeToString(chunk),
		})
		frames = append(frames, ClientResponseFrame{Frame: "stream", ID: "screenshot-request", StreamID: streamID, Event: event})
	}
	code := 0
	terminal, _ := json.Marshal(map[string]any{
		"ok": true, "method": desktopCdpCaptureScreenshotMethod,
		"result": map[string]any{"data": map[string]any{
			"streamed": true, "streamId": streamID, "encoding": "base64", "chunkCount": 2, "totalBytes": len(png),
		}},
	})
	frames = append(frames, ClientResponseFrame{Frame: "response", Type: desktopCDPRequestType, ID: "screenshot-request", Code: &code, Data: terminal})
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			RuntimeMode: config.RuntimeModeDesktop,
			Paths:       config.PathsConfig{ChatsDir: chatRoot},
		},
		clientRequest: &scriptedClientRequestInvoker{frames: frames},
		clientTargets: emptyRunClientTargetStore{},
	}
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"requestId": "screenshot-request", "method": desktopCdpCaptureScreenshotMethod,
	}, &ExecutionContext{Session: QuerySession{ChatID: chatID}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("streamed screenshot failed: result=%#v err=%v", result, err)
	}
	response := cloneDesktopMapValue(result.Structured["response"])
	resultNode := cloneDesktopMapValue(response["result"])
	metadata := cloneDesktopMapValue(resultNode["data"])
	filePath, _ := metadata["filePath"].(string)
	written, readErr := os.ReadFile(filePath)
	if readErr != nil || !bytes.Equal(written, png) {
		t.Fatalf("unexpected streamed screenshot file: path=%q err=%v", filePath, readErr)
	}

	badEvent, _ := json.Marshal(desktopStreamEvent{
		Seq: 1, Type: desktopScreenshotDeltaEventType, Timestamp: 1771888000000,
		Encoding: "base64", Chunk: base64.StdEncoding.EncodeToString(png[:4]),
	})
	badExecutor := *executor
	badExecutor.clientRequest = &scriptedClientRequestInvoker{frames: []ClientResponseFrame{
		{Frame: "stream", ID: "bad-screenshot", StreamID: "bad-stream", Event: badEvent},
		{Frame: "stream", ID: "bad-screenshot", StreamID: "bad-stream", Event: json.RawMessage(`{"seq":3,"type":"desktop.cdp.screenshot.delta","timestamp":1771888000001,"encoding":"base64","chunk":"AA=="}`)},
	}}
	badResult, badErr := badExecutor.invokeDesktopCDP(context.Background(), map[string]any{
		"requestId": "bad-screenshot", "method": desktopCdpCaptureScreenshotMethod,
	}, &ExecutionContext{Session: QuerySession{ChatID: chatID}})
	if badErr != nil || badResult.ExitCode != -1 || badResult.Error != "desktop_cdp_invalid_client_response" {
		t.Fatalf("expected invalid screenshot stream failure, got result=%#v err=%v", badResult, badErr)
	}
	entries, err := os.ReadDir(filepath.Join(chatRoot, chatID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("partial screenshot was not cleaned: %s", entry.Name())
		}
	}
	oversized, err := newDesktopScreenshotSink(executor, &ExecutionContext{Session: QuerySession{ChatID: chatID}})
	if err != nil {
		t.Fatal(err)
	}
	oversized.size = desktopMaxDecodedResponseBytes
	if err := oversized.write([]byte{0}); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatalf("expected screenshot size limit, got %v", err)
	}
	oversized.abort()
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
		RunID:           "run-cdp",
		ChatID:          "chat-cdp",
		AgentKey:        "desktopAssistant",
		WebClientTarget: WebClientTarget{SessionID: "ws-desktop-test"},
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
		"desktop.workpanel.getState",
		"desktop.workpanel.openTab",
		"desktop.workpanel.openWeb",
		"desktop.workpanel.refreshWeb",
		"desktop.workpanel.activateTab",
		"desktop.workpanel.closeTab",
		"desktop.workpanel.closeWorkpanel",
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
		"desktop.workpanel.activateTab",
		"desktop.workpanel.closeTab",
		"desktop.workpanel.closeWorkpanel",
		"desktop.workpanel.getState",
		"desktop.workpanel.openTab",
		"desktop.workpanel.openWeb",
		"desktop.workpanel.refreshWeb",
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

func TestInvokeDesktopActionRequiresClientProvider(t *testing.T) {
	result, err := (&RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.controlCenter.listServices",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "desktop_action_provider_unavailable" {
		t.Fatalf("expected provider unavailable failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
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
	return &RuntimeToolExecutor{
		cfg:           config.Config{RuntimeMode: config.RuntimeModeDesktop},
		clientRequest: &httpBackedClientRequestInvoker{actionURL: actionURL, cdpURL: cdpURL},
		clientTargets: emptyRunClientTargetStore{},
	}
}
