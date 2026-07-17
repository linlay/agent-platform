package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	channelpkg "agent-platform/internal/channel"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestAgentFileEndpointReadsCoderAndKBaseWorkspaceFiles(t *testing.T) {
	fixture, coderWorkspace, kbaseWorkspace := newAgentFileTestFixture(t)

	coderResp := getAgentFileJSON(t, fixture.server, "coder-file", "docs/hello.md")
	if coderResp.AgentKey != "coder-file" || coderResp.ContentKind != "text" || coderResp.Encoding != "utf-8" {
		t.Fatalf("unexpected coder response metadata: %#v", coderResp)
	}
	if coderResp.Path != "docs/hello.md" || coderResp.Content != "# Hello\n\ncoder workspace\n" {
		t.Fatalf("unexpected coder file content: %#v", coderResp)
	}
	if coderResp.WorkspaceRoot != canonicalPathForAgentFileTest(t, coderWorkspace) || coderResp.ContentURL == "" ||
		!strings.Contains(coderResp.ContentURL, "response=content") {
		t.Fatalf("unexpected coder workspace/content URL: %#v", coderResp)
	}

	kbaseResp := getAgentFileJSON(t, fixture.server, "kbase-file", "docs/kbase.md")
	if kbaseResp.AgentKey != "kbase-file" || kbaseResp.ContentKind != "text" {
		t.Fatalf("unexpected kbase response metadata: %#v", kbaseResp)
	}
	if kbaseResp.Path != "docs/kbase.md" || kbaseResp.Content != "# KBase\n\nknowledge workspace\n" {
		t.Fatalf("unexpected kbase file content: %#v", kbaseResp)
	}
	if kbaseResp.WorkspaceRoot != canonicalPathForAgentFileTest(t, kbaseWorkspace) {
		t.Fatalf("unexpected kbase workspace root: %#v", kbaseResp)
	}
}

func TestAgentFileEndpointSupportsAbsolutePathAndContentResponse(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	absPath := filepath.Join(coderWorkspace, "docs", "hello.md")

	resp := getAgentFileJSON(t, fixture.server, "coder-file", absPath)
	if resp.Path != "docs/hello.md" || resp.AbsolutePath != canonicalPathForAgentFileTest(t, absPath) {
		t.Fatalf("unexpected absolute path response: %#v", resp)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentFileURL("coder-file", "docs/hello.md", "content"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected content response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "# Hello\n\ncoder workspace\n" {
		t.Fatalf("unexpected content response body: %q", got)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/") {
		t.Fatalf("expected text content type, got %q", contentType)
	}

	pdfResp := getAgentFileJSON(t, fixture.server, "coder-file", "docs/manual.pdf")
	if pdfResp.ContentKind != "binary" || pdfResp.Content != "" || !strings.Contains(pdfResp.MimeType, "application/pdf") {
		t.Fatalf("expected pdf metadata-only response, got %#v", pdfResp)
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentFileURL("coder-file", "docs/manual.pdf", "content"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected pdf content response 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte("%PDF-1.4\nmock pdf\n")) {
		t.Fatalf("unexpected pdf body: %q", rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/pdf") {
		t.Fatalf("expected pdf content type, got %q", contentType)
	}
}

func TestAgentFileEndpointRejectsWorkspaceEscapes(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	outsidePath := filepath.Join(filepath.Dir(coderWorkspace), "outside.md")
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	tests := []struct {
		name string
		path string
		code int
	}{
		{name: "relative parent escape", path: "../outside.md", code: http.StatusForbidden},
		{name: "absolute outside workspace", path: outsidePath, code: http.StatusForbidden},
		{name: "directory", path: "docs", code: http.StatusBadRequest},
		{name: "missing stable workspace", path: "notes.md", code: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agentKey := "coder-file"
			if tc.name == "missing stable workspace" {
				agentKey = "mock-agent"
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentFileURL(agentKey, tc.path, ""), nil))
			if rec.Code != tc.code {
				t.Fatalf("expected %d, got %d: %s", tc.code, rec.Code, rec.Body.String())
			}
			if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
				t.Fatalf("expected JSON error response, got %q", contentType)
			}
		})
	}

	linkPath := filepath.Join(coderWorkspace, "docs", "outside-link.md")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentFileURL("coder-file", "docs/outside-link.md", ""), nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected symlink escape 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWebSocketAgentFileReturnsTextAndBinaryMetadata(t *testing.T) {
	fixture, _, _ := newAgentFileTestFixture(t)
	server := newLoopbackServer(t, fixture.server)
	conn := dialAgentFileWebSocket(t, server.URL, "/ws")
	defer conn.Close()

	writeAgentFileWSRequest(t, conn, "file_text", map[string]any{
		"agentKey": "coder-file",
		"path":     "docs/hello.md",
	})
	textFrame := readAgentFileWSResponse(t, conn, "file_text")
	if textFrame.Type != "/api/file" || textFrame.Code != 0 || textFrame.Data.ContentKind != "text" ||
		textFrame.Data.Content != "# Hello\n\ncoder workspace\n" {
		t.Fatalf("unexpected websocket text file response: %#v", textFrame)
	}

	writeAgentFileWSRequest(t, conn, "file_binary", map[string]any{
		"agentKey": "coder-file",
		"path":     "docs/manual.pdf",
		"response": "json",
	})
	binaryFrame := readAgentFileWSResponse(t, conn, "file_binary")
	if binaryFrame.Type != "/api/file" || binaryFrame.Code != 0 || binaryFrame.Data.ContentKind != "binary" ||
		binaryFrame.Data.Content != "" || binaryFrame.Data.ContentURL == "" {
		t.Fatalf("unexpected websocket binary file response: %#v", binaryFrame)
	}
}

func TestWebSocketAgentFileRejectsInvalidAndForbiddenRequests(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	outsidePath := filepath.Join(filepath.Dir(coderWorkspace), "outside.md")
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	server := newLoopbackServer(t, fixture.server)
	conn := dialAgentFileWebSocket(t, server.URL, "/ws")
	defer conn.Close()

	tests := []struct {
		name         string
		payload      any
		expectedType string
		expectedCode int
	}{
		{
			name:         "invalid payload",
			payload:      []any{},
			expectedType: "invalid_request",
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "content response is HTTP only",
			payload: map[string]any{
				"agentKey": "coder-file",
				"path":     "docs/hello.md",
				"response": "content",
			},
			expectedType: "invalid_request",
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "unknown agent",
			payload: map[string]any{
				"agentKey": "missing-agent",
				"path":     "docs/hello.md",
			},
			expectedType: "not_found",
			expectedCode: http.StatusNotFound,
		},
		{
			name: "workspace escape",
			payload: map[string]any{
				"agentKey": "coder-file",
				"path":     "../outside.md",
			},
			expectedType: "forbidden",
			expectedCode: http.StatusForbidden,
		},
		{
			name: "missing file",
			payload: map[string]any{
				"agentKey": "coder-file",
				"path":     "docs/missing.md",
			},
			expectedType: "not_found",
			expectedCode: http.StatusNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "file_" + strings.ReplaceAll(tc.name, " ", "_")
			writeAgentFileWSRequest(t, conn, requestID, tc.payload)
			frame := readAgentFileWSError(t, conn, requestID)
			if frame.Type != tc.expectedType || frame.Code != tc.expectedCode {
				t.Fatalf("unexpected websocket file error: %#v", frame)
			}
		})
	}
}

func TestChannelWebSocketAgentFileAllowsDirectAgentKey(t *testing.T) {
	fixture, _, _ := newAgentFileTestFixture(t)
	fixture.server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{{
		ID:   "workspace-read",
		Mode: config.ChannelModeServer,
	}})
	server := newLoopbackServer(t, fixture.server)
	conn := dialAgentFileWebSocket(t, server.URL, "/ws/channel?channelId=workspace-read")
	defer conn.Close()

	writeAgentFileWSRequest(t, conn, "channel_file", map[string]any{
		"agentKey": "coder-file",
		"path":     "docs/hello.md",
	})
	frame := readAgentFileWSResponse(t, conn, "channel_file")
	if frame.Type != "/api/file" || frame.Code != 0 || frame.Data.AgentKey != "coder-file" ||
		frame.Data.Content != "# Hello\n\ncoder workspace\n" {
		t.Fatalf("unexpected channel websocket file response: %#v", frame)
	}
}

type agentFileWSResponseFrame struct {
	Frame string                `json:"frame"`
	Type  string                `json:"type"`
	ID    string                `json:"id"`
	Code  int                   `json:"code"`
	Data  api.AgentFileResponse `json:"data"`
}

func dialAgentFileWebSocket(t *testing.T, baseURL string, path string) *gws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + path
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	waitForPushFrameType(t, conn, "connected")
	return conn
}

func writeAgentFileWSRequest(t *testing.T, conn *gws.Conn, requestID string, payload any) {
	t.Helper()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/file",
		ID:      requestID,
		Payload: ws.MarshalPayload(payload),
	}); err != nil {
		t.Fatalf("write websocket file request: %v", err)
	}
}

func readAgentFileWSResponse(t *testing.T, conn *gws.Conn, requestID string) agentFileWSResponseFrame {
	t.Helper()
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		return json.Unmarshal(data, &frame) == nil && frame.Frame == ws.FrameResponse && frame.ID == requestID
	})
	var frame agentFileWSResponseFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket file response: %v", err)
	}
	return frame
}

func readAgentFileWSError(t *testing.T, conn *gws.Conn, requestID string) ws.ErrorFrame {
	t.Helper()
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		return json.Unmarshal(data, &frame) == nil && frame.Frame == ws.FrameError && frame.ID == requestID
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket file error: %v", err)
	}
	return frame
}

func newAgentFileTestFixture(t *testing.T) (testFixture, string, string) {
	t.Helper()
	var coderWorkspace string
	var kbaseWorkspace string
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(root string, cfg *config.Config) {
			coderWorkspace = filepath.Join(root, "coder-workspace")
			kbaseWorkspace = filepath.Join(root, "kbase-workspace")
			for _, dir := range []string{
				filepath.Join(coderWorkspace, "docs"),
				filepath.Join(kbaseWorkspace, "docs"),
				filepath.Join(cfg.Paths.AgentsDir, "coder-file"),
				filepath.Join(cfg.Paths.AgentsDir, "kbase-file"),
			} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", dir, err)
				}
			}
			if err := os.WriteFile(filepath.Join(coderWorkspace, "docs", "hello.md"), []byte("# Hello\n\ncoder workspace\n"), 0o644); err != nil {
				t.Fatalf("write coder doc: %v", err)
			}
			if err := os.WriteFile(filepath.Join(coderWorkspace, "docs", "manual.pdf"), []byte("%PDF-1.4\nmock pdf\n"), 0o644); err != nil {
				t.Fatalf("write coder pdf: %v", err)
			}
			if err := os.WriteFile(filepath.Join(kbaseWorkspace, "docs", "kbase.md"), []byte("# KBase\n\nknowledge workspace\n"), 0o644); err != nil {
				t.Fatalf("write kbase doc: %v", err)
			}
			writeAgentFileTestAgent(t, filepath.Join(cfg.Paths.AgentsDir, "coder-file", "agent.yml"), "coder-file", "CODER", coderWorkspace)
			writeAgentFileTestAgent(t, filepath.Join(cfg.Paths.AgentsDir, "kbase-file", "agent.yml"), "kbase-file", "KBASE", kbaseWorkspace)
		},
	})
	return fixture, coderWorkspace, kbaseWorkspace
}

func writeAgentFileTestAgent(t *testing.T, path string, key string, mode string, workspace string) {
	t.Helper()
	content := strings.Join([]string{
		"key: " + key,
		"name: " + key,
		"mode: " + mode,
		"modelConfig:",
		"  modelKey: mock-model",
		"runtimeConfig:",
		"  workspaceRoot: " + filepath.ToSlash(workspace),
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent %s: %v", key, err)
	}
}

func getAgentFileJSON(t *testing.T, server *Server, agentKey string, path string) api.AgentFileResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentFileURL(agentKey, path, ""), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentFileResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Data
}

func agentFileURL(agentKey string, path string, responseMode string) string {
	query := url.Values{}
	query.Set("agentKey", agentKey)
	query.Set("path", path)
	if responseMode != "" {
		query.Set("response", responseMode)
	}
	return "/api/file?" + query.Encode()
}

func canonicalPathForAgentFileTest(t *testing.T, path string) string {
	t.Helper()
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return filepath.Clean(realPath)
}
