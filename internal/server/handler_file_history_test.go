package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestFileHistoryEndpointReturnsRecordedSnapshots(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-file-history"
	runID := "run-file-history"
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "notes.txt")
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		ChatID:        chatID,
		RunID:         runID,
		WorkspaceRoot: workspace,
	}}

	result, err := fixture.tools.Invoke(context.Background(), "file_write", map[string]any{
		"file_path": filePath,
		"content":   "hello\n",
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke file_write: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected file_write success, got %#v", result)
	}

	historyPath, _ := result.Structured["filePath"].(string)
	original := getFileHistory(t, fixture.server, chatID, runID, historyPath, "original")
	if original != "" {
		t.Fatalf("expected empty original, got %q", original)
	}
	current := getFileHistory(t, fixture.server, chatID, runID, historyPath, "current")
	if current != "hello\n" {
		t.Fatalf("expected current content, got %q", current)
	}

	_, _, _ = fixture.runs.Register(context.Background(), contracts.QuerySession{
		ChatID:   chatID,
		RunID:    runID,
		AgentKey: "mock-agent",
		RunOwner: contracts.AgentRunOwner("mock-agent", ""),
	})
	current = getFileHistory(t, fixture.server, "", runID, historyPath, "current")
	if current != "hello\n" {
		t.Fatalf("expected fallback current content, got %q", current)
	}
}

func TestFileHistoryEndpointFailures(t *testing.T) {
	fixture := newTestFixture(t)
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "notes.txt")

	tests := []struct {
		name string
		path string
		code int
	}{
		{
			name: "missing history",
			path: fileHistoryPath("chat-file-history-missing", "run-file-history-missing", filePath, "original"),
			code: http.StatusNotFound,
		},
		{
			name: "invalid chat id",
			path: fileHistoryPath("../bad", "run-file-history", filePath, "original"),
			code: http.StatusBadRequest,
		},
		{
			name: "invalid run id traversal",
			path: fileHistoryPath("chat-file-history", "../run-file-history", filePath, "original"),
			code: http.StatusBadRequest,
		},
		{
			name: "invalid file path traversal",
			path: fileHistoryPath("chat-file-history", "run-file-history", "../secrets.txt", "original"),
			code: http.StatusBadRequest,
		},
		{
			name: "invalid version",
			path: fileHistoryPath("chat-file-history", "run-file-history", filePath, "latest"),
			code: http.StatusBadRequest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("expected %d, got %d: %s", tc.code, rec.Code, rec.Body.String())
			}
		})
	}
}

func getFileHistory(t *testing.T, server *Server, chatID string, runID string, filePath string, version string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fileHistoryPath(chatID, runID, filePath, version), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.FileHistoryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Data.Content
}

func fileHistoryPath(chatID string, runID string, filePath string, version string) string {
	query := url.Values{}
	query.Set("chatId", chatID)
	query.Set("runId", runID)
	query.Set("filePath", filePath)
	query.Set("version", version)
	return "/api/file/history?" + query.Encode()
}
