package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

func postDocumentCommit(t *testing.T, server *Server, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/document/commit", bytes.NewReader(body)))
	return recorder
}

func TestDocumentCommitEndpointOverwritesWorkspaceWithRevision(t *testing.T) {
	fixture, _, _ := newAgentFileTestFixture(t)
	file := getAgentFileJSON(t, fixture.server, "coder-file", "docs/hello.md")
	payload := map[string]any{
		"operation":        "document.commit",
		"source":           map[string]any{"kind": "workspace-file", "agentKey": "coder-file", "path": "docs/hello.md"},
		"mode":             "overwrite",
		"expectedRevision": file.Revision,
		"payload": map[string]any{
			"kind": "document-markdown", "mimeType": "text/markdown", "encoding": "utf-8", "text": "# Edited\n",
		},
	}
	recorder := postDocumentCommit(t, fixture.server, payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response api.ApiResponse[api.DocumentCommitResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.SourceKind != "workspace-file" || response.Data.Revision == "" || response.Data.Revision == file.Revision {
		t.Fatalf("unexpected commit response %#v", response.Data)
	}
	if got := getAgentFileJSON(t, fixture.server, "coder-file", "docs/hello.md"); got.Content != "# Edited\n" {
		t.Fatalf("workspace content=%q", got.Content)
	}
	if stale := postDocumentCommit(t, fixture.server, payload); stale.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func seedServerMarkdownArtifact(t *testing.T, fixture testFixture, chatID string) (string, string) {
	t.Helper()
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "document"); err != nil {
		t.Fatal(err)
	}
	relativePath := "artifacts/run-doc/notes.md"
	targetPath := filepath.Join(fixture.chats.ChatDir(chatID), filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("# Original\n")
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	resourceURL, err := chat.BuildChatScopeRef(relativePath)
	if err != nil {
		t.Fatal(err)
	}
	writer, ok := fixture.chats.(chat.ArtifactManifestWriter)
	if !ok {
		t.Fatal("chat store does not support artifact manifests")
	}
	if err := writer.AppendArtifactManifest(chatID, "run-doc", time.Now().UnixMilli(), []map[string]any{{
		"artifactId": "artifact-doc", "type": "file", "name": "notes.md",
		"mimeType": "text/markdown", "sizeBytes": len(data), "url": resourceURL,
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	return relativePath, fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixMilli())
}

func TestDocumentCommitEndpointCreatesArtifactAndRejectsReferenceOverwrite(t *testing.T) {
	fixture := newTestFixture(t)
	relativePath, revision := seedServerMarkdownArtifact(t, fixture, "chat-document-commit")
	base := map[string]any{
		"operation": "document.commit",
		"source": map[string]any{
			"kind": "artifact", "agentKey": "mock-agent", "chatId": "chat-document-commit",
			"resourceId": "artifact-doc", "relativePath": relativePath,
		},
		"mode": "new-artifact", "expectedRevision": revision,
		"payload": map[string]any{
			"kind": "document-markdown", "mimeType": "text/markdown", "encoding": "utf-8", "text": "# Edited\n",
		},
	}
	recorder := postDocumentCommit(t, fixture.server, base)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response api.ApiResponse[api.DocumentCommitResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ArtifactID == "" || response.Data.RelativePath == relativePath || response.Data.SourceKind != "artifact" {
		t.Fatalf("unexpected new Artifact response %#v", response.Data)
	}

	referencePath := filepath.Join(fixture.chats.ChatDir("chat-document-commit"), "source.txt")
	if err := os.WriteFile(referencePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	denied := postDocumentCommit(t, fixture.server, map[string]any{
		"operation": "document.commit",
		"source": map[string]any{
			"kind": "reference", "agentKey": "mock-agent", "chatId": "chat-document-commit",
			"resourceId": "reference-doc", "relativePath": "source.txt",
		},
		"mode": "overwrite", "expectedRevision": fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixMilli()),
		"payload": map[string]any{
			"kind": "document-text", "mimeType": "text/plain", "encoding": "utf-8", "text": "edited",
		},
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("reference overwrite status=%d body=%s", denied.Code, denied.Body.String())
	}
}
