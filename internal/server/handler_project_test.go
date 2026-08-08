package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func TestProjectTreeReadsCoderAndKBaseWorkspaces(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	if err := os.MkdirAll(filepath.Join(coderWorkspace, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coderWorkspace, "zeta.txt"), []byte("zeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	coder := getProjectTree(t, fixture.server, "coder-file", "", "", 1)
	if coder.AgentKey != "coder-file" || coder.Mode != "CODER" || coder.WorkspaceName != filepath.Base(coderWorkspace) {
		t.Fatalf("unexpected coder tree metadata: %#v", coder)
	}
	if len(coder.Entries) != 1 || coder.Entries[0].Kind != "directory" || coder.NextCursor == "" {
		t.Fatalf("expected paged directory-first response: %#v", coder)
	}
	next := getProjectTree(t, fixture.server, "coder-file", "", coder.NextCursor, 10)
	if len(next.Entries) == 0 {
		t.Fatalf("expected second tree page: %#v", next)
	}

	kbase := getProjectTree(t, fixture.server, "kbase-file", "docs", "", 200)
	if kbase.AgentKey != "kbase-file" || kbase.Mode != "KBASE" || len(kbase.Entries) != 1 || kbase.Entries[0].Path != "docs/kbase.md" {
		t.Fatalf("unexpected kbase tree: %#v", kbase)
	}
}

func TestProjectEmptyCollectionsAreJSONArrays(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	emptyDir := filepath.Join(coderWorkspace, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	treeRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(treeRec, httptest.NewRequest(http.MethodGet, projectTreeURL("coder-file", "empty", "", 200), nil))
	if treeRec.Code != http.StatusOK || !strings.Contains(treeRec.Body.String(), `"entries":[]`) {
		t.Fatalf("empty tree must return entries as an array, status=%d body=%s", treeRec.Code, treeRec.Body.String())
	}

	chatID := "chat-project-empty-history"
	if _, _, err := fixture.chats.EnsureChatWithSourceAndMode(chatID, "coder-file", "", "project", "web", "CODER"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	changesRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(changesRec, httptest.NewRequest(http.MethodGet, projectChangesURL("coder-file", chatID, ""), nil))
	if changesRec.Code != http.StatusOK || !strings.Contains(changesRec.Body.String(), `"runs":[]`) || !strings.Contains(changesRec.Body.String(), `"items":[]`) {
		t.Fatalf("empty changes must return runs/items as arrays, status=%d body=%s", changesRec.Code, changesRec.Body.String())
	}
}

func TestProjectTreeRejectsUnsupportedPathsAndStaleCursor(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	if err := os.WriteFile(filepath.Join(coderWorkspace, "another.txt"), []byte("another\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := getProjectTree(t, fixture.server, "coder-file", "", "", 1)
	if first.NextCursor == "" {
		t.Fatal("expected cursor")
	}
	if err := os.WriteFile(filepath.Join(coderWorkspace, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		url   string
		code  int
		match string
	}{
		{name: "unsupported mode", url: projectTreeURL("root-workspace", "", "", 200), code: http.StatusBadRequest, match: "project browsing only supports"},
		{name: "parent escape", url: projectTreeURL("coder-file", "../", "", 200), code: http.StatusForbidden, match: "outside agent workspace"},
		{name: "absolute path", url: projectTreeURL("coder-file", coderWorkspace, "", 200), code: http.StatusBadRequest, match: "workspace-relative"},
		{name: "stale cursor", url: projectTreeURL("coder-file", "", first.NextCursor, 1), code: http.StatusConflict, match: "changed while paging"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.match) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestProjectTreeDoesNotExpandDirectorySymlinks(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	link := filepath.Join(coderWorkspace, "docs-link")
	if err := os.Symlink(filepath.Join(coderWorkspace, "docs"), link); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	tree := getProjectTree(t, fixture.server, "coder-file", "", "", 200)
	found := false
	for _, entry := range tree.Entries {
		if entry.Name == "docs-link" {
			found = true
			if entry.Kind != "symlink" || entry.TargetKind != "directory" || entry.Accessible {
				t.Fatalf("unexpected directory symlink entry: %#v", entry)
			}
		}
	}
	if !found {
		t.Fatal("expected directory symlink in tree")
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectTreeURL("coder-file", "docs-link", "", 200), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected symlink expansion rejection, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProjectTreeSymlinkAndFilesystemRootBoundaries(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	insideLink := filepath.Join(coderWorkspace, "hello-link.md")
	if err := os.Symlink(filepath.Join(coderWorkspace, "docs", "hello.md"), insideLink); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	outside := filepath.Join(filepath.Dir(coderWorkspace), "outside-project.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideLink := filepath.Join(coderWorkspace, "outside-link.md")
	if err := os.Symlink(outside, outsideLink); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	tree := getProjectTree(t, fixture.server, "coder-file", "", "", 200)
	entries := map[string]api.ProjectTreeEntry{}
	for _, entry := range tree.Entries {
		entries[entry.Name] = entry
	}
	if entry := entries["hello-link.md"]; entry.Kind != "symlink" || entry.TargetKind != "file" || !entry.Accessible {
		t.Fatalf("inside file symlink should be previewable: %#v", entry)
	}
	if entry := entries["outside-link.md"]; entry.Kind != "symlink" || entry.Accessible {
		t.Fatalf("escaping symlink should remain visible but inaccessible: %#v", entry)
	}

	chatsRel, err := filepath.Rel(string(filepath.Separator), fixture.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectTreeURL("project-root-workspace", filepath.ToSlash(chatsRel), "", 200), nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "chat") {
		t.Fatalf("filesystem-root project must reject ChatsRoot, got %d: %s", rec.Code, rec.Body.String())
	}

	if filepath.Separator == '/' {
		dev := getProjectTree(t, fixture.server, "project-root-workspace", "dev", "", 1000)
		for _, entry := range dev.Entries {
			switch entry.Name {
			case "null", "full", "random", "urandom", "zero":
				t.Fatalf("blocked device file leaked from project tree: %#v", entry)
			}
		}
	}
}

func TestProjectTreeRejectsUnknownAgent(t *testing.T) {
	fixture, _, _ := newAgentFileTestFixture(t)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectTreeURL("missing-agent", "", "", 200), nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "agent not found") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectChangesAndDiffUseRunFileHistory(t *testing.T) {
	fixture, coderWorkspace, _ := newAgentFileTestFixture(t)
	chatID := "chat-project-history"
	runID := "run-project-history"
	if _, _, err := fixture.chats.EnsureChatWithSourceAndMode(chatID, "coder-file", "", "project", "web", "CODER"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := fixture.chats.AppendQueryLine(chatID, chat.QueryLine{
		Type: "query", ChatID: chatID, RunID: runID, UpdatedAt: time.Now().UnixMilli(),
		Query: map[string]any{"role": "user", "content": "write project file"},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	filePath := filepath.Join(coderWorkspace, "generated.txt")
	result, err := fixture.tools.Invoke(context.Background(), "file_write", map[string]any{
		"file_path": filePath,
		"content":   "hello project\n",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{
		AgentKey: "coder-file", Mode: "CODER", ChatID: chatID, RunID: runID, WorkspaceRoot: coderWorkspace,
	}})
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("write project history fixture: result=%#v err=%v", result, err)
	}

	changes := getProjectChanges(t, fixture.server, "coder-file", chatID, "")
	if len(changes.Runs) != 1 || changes.Runs[0].RunID != runID || len(changes.Items) != 1 {
		t.Fatalf("unexpected project changes: %#v", changes)
	}
	item := changes.Items[0]
	if item.Path != "generated.txt" || item.ChangeType != "added" || item.Original.Exists || !item.Current.Exists {
		t.Fatalf("unexpected change item: %#v", item)
	}

	diff := getProjectDiff(t, fixture.server, "coder-file", chatID, runID, "generated.txt")
	if diff.ChangeType != "added" || diff.Original.Exists || diff.Original.Content != "" || diff.Current.Content != "hello project\n" || diff.Current.Encoding != "utf-8" {
		t.Fatalf("unexpected project diff: %#v", diff)
	}
	result, err = fixture.tools.Invoke(context.Background(), "file_write", map[string]any{
		"file_path": filePath,
		"content":   "hello modified project\n",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{
		AgentKey: "coder-file", Mode: "CODER", ChatID: chatID, RunID: runID, WorkspaceRoot: coderWorkspace,
	}})
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("modify project history fixture: result=%#v err=%v", result, err)
	}
	updatedAdded := getProjectDiff(t, fixture.server, "coder-file", chatID, runID, "generated.txt")
	if updatedAdded.ChangeType != "added" || updatedAdded.Original.Content != "" || updatedAdded.Current.Content != "hello modified project\n" {
		t.Fatalf("unexpected updated added-file diff: %#v", updatedAdded)
	}

	existingPath := filepath.Join(coderWorkspace, "docs", "hello.md")
	result, err = fixture.tools.Invoke(context.Background(), "file_write", map[string]any{
		"file_path": existingPath,
		"content":   "# Hello\n\nmodified workspace\n",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{
		AgentKey: "coder-file", Mode: "CODER", ChatID: chatID, RunID: runID, WorkspaceRoot: coderWorkspace,
	}})
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("modify existing project history fixture: result=%#v err=%v", result, err)
	}
	modified := getProjectDiff(t, fixture.server, "coder-file", chatID, runID, "docs/hello.md")
	if modified.ChangeType != "modified" || modified.Original.Content != "# Hello\n\ncoder workspace\n" || modified.Current.Content != "# Hello\n\nmodified workspace\n" {
		t.Fatalf("unexpected modified project diff: %#v", modified)
	}

	binaryPath := filepath.Join(coderWorkspace, "binary.txt")
	result, err = fixture.tools.Invoke(context.Background(), "file_write", map[string]any{
		"file_path": binaryPath,
		"content":   "\x00binary history",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{
		AgentKey: "coder-file", Mode: "CODER", ChatID: chatID, RunID: runID, WorkspaceRoot: coderWorkspace,
	}})
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("write binary history fixture: result=%#v err=%v", result, err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectDiffURL("coder-file", chatID, runID, "binary.txt"), nil))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected binary diff rejection, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectDiffURL("coder-file", chatID, "missing-run", "generated.txt"), nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "run not found") {
		t.Fatalf("expected wrong run rejection, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectDiffURL("coder-file", chatID, runID, "missing.txt"), nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "diff not found") {
		t.Fatalf("expected missing history rejection, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectChangesURL("kbase-file", chatID, ""), nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected cross-agent chat rejection, got %d: %s", rec.Code, rec.Body.String())
	}

	fixture.server.deps.Config.FileTools.MaxReadBytes = 4
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectDiffURL("coder-file", chatID, runID, "generated.txt"), nil))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "diff exceeds") {
		t.Fatalf("expected diff size rejection, got %d: %s", rec.Code, rec.Body.String())
	}
}

func getProjectTree(t *testing.T, server *Server, agentKey string, path string, cursor string, limit int) api.ProjectTreeResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectTreeURL(agentKey, path, cursor, limit), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected tree 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.ProjectTreeResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode tree response: %v", err)
	}
	return response.Data
}

func getProjectChanges(t *testing.T, server *Server, agentKey string, chatID string, runID string) api.ProjectChangesResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectChangesURL(agentKey, chatID, runID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected changes 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.ProjectChangesResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode changes response: %v", err)
	}
	return response.Data
}

func getProjectDiff(t *testing.T, server *Server, agentKey string, chatID string, runID string, path string) api.ProjectDiffResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projectDiffURL(agentKey, chatID, runID, path), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected diff 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.ProjectDiffResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode diff response: %v", err)
	}
	return response.Data
}

func projectTreeURL(agentKey string, path string, cursor string, limit int) string {
	query := url.Values{"agentKey": []string{agentKey}, "path": []string{path}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprint(limit))
	}
	return "/api/project/tree?" + query.Encode()
}

func projectChangesURL(agentKey string, chatID string, runID string) string {
	query := url.Values{"agentKey": []string{agentKey}, "chatId": []string{chatID}}
	if runID != "" {
		query.Set("runId", runID)
	}
	return "/api/project/changes?" + query.Encode()
}

func projectDiffURL(agentKey string, chatID string, runID string, path string) string {
	query := url.Values{"agentKey": []string{agentKey}, "chatId": []string{chatID}, "runId": []string{runID}, "path": []string{path}}
	return "/api/project/diff?" + query.Encode()
}
