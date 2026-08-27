package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/chat"
)

func seedServerImageArtifact(t *testing.T, fixture testFixture, chatID string) (string, string) {
	t.Helper()
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "image"); err != nil {
		t.Fatal(err)
	}
	relativePath := "artifacts/run-1/source.png"
	targetPath := filepath.Join(fixture.chats.ChatDir(chatID), filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	resourceURL, err := chat.BuildChatScopeRef(relativePath)
	if err != nil {
		t.Fatal(err)
	}
	manifestWriter, ok := fixture.chats.(chat.ArtifactManifestWriter)
	if !ok {
		t.Fatal("chat store does not support artifact manifests")
	}
	if err := manifestWriter.AppendArtifactManifest(chatID, "run-1", time.Now().UnixMilli(), []map[string]any{{
		"artifactId": "artifact-1", "type": "file", "name": "source.png",
		"mimeType": "image/png", "sizeBytes": len(data), "url": resourceURL,
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	return relativePath, fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixMilli())
}

func postResourceImageCommit(t *testing.T, server *Server, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/resource/image/commit", bytes.NewReader(body)))
	return recorder
}

func TestResourceImageCommitEndpointCreatesArtifactAndValidatesOwner(t *testing.T) {
	fixture := newTestFixture(t)
	relativePath, revision := seedServerImageArtifact(t, fixture, "chat-image-commit")
	edited := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 2}
	base := map[string]any{
		"operation": "resource.image.commit", "profile": "artifact", "agentKey": "mock-agent",
		"chatId": "chat-image-commit", "resourceId": "artifact-1", "relativePath": relativePath,
		"mode": "new-artifact", "expectedRevision": revision, "mimeType": "image/png",
		"dataBase64": base64.StdEncoding.EncodeToString(edited),
	}
	recorder := postResourceImageCommit(t, fixture.server, base)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Code int                            `json:"code"`
		Data chat.ResourceImageCommitResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != 0 || response.Data.ArtifactID == "" || response.Data.RelativePath == relativePath {
		t.Fatalf("unexpected response %#v", response)
	}

	wrongOwner := make(map[string]any, len(base))
	for key, value := range base {
		wrongOwner[key] = value
	}
	wrongOwner["agentKey"] = "another-agent"
	if denied := postResourceImageCommit(t, fixture.server, wrongOwner); denied.Code != http.StatusForbidden {
		t.Fatalf("owner mismatch status=%d body=%s", denied.Code, denied.Body.String())
	}

	badSignature := make(map[string]any, len(base))
	for key, value := range base {
		badSignature[key] = value
	}
	badSignature["dataBase64"] = base64.StdEncoding.EncodeToString([]byte("not-an-image"))
	if invalid := postResourceImageCommit(t, fixture.server, badSignature); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid signature status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestResourceImageCommitEndpointRejectsRevisionConflictAndReferenceOverwrite(t *testing.T) {
	fixture := newTestFixture(t)
	relativePath, _ := seedServerImageArtifact(t, fixture, "chat-image-conflict")
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 3}
	conflict := postResourceImageCommit(t, fixture.server, map[string]any{
		"operation": "resource.image.commit", "profile": "artifact", "agentKey": "mock-agent",
		"chatId": "chat-image-conflict", "resourceId": "artifact-1", "relativePath": relativePath,
		"mode": "overwrite", "expectedRevision": "1:1", "mimeType": "image/png",
		"dataBase64": base64.StdEncoding.EncodeToString(png),
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	referencePath := filepath.Join(fixture.chats.ChatDir("chat-image-conflict"), "source.png")
	if err := os.MkdirAll(filepath.Dir(referencePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(referencePath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	denied := postResourceImageCommit(t, fixture.server, map[string]any{
		"operation": "resource.image.commit", "profile": "reference", "agentKey": "mock-agent",
		"chatId": "chat-image-conflict", "resourceId": "reference-1", "relativePath": "source.png",
		"mode": "overwrite", "expectedRevision": "1:1", "mimeType": "image/png",
		"dataBase64": base64.StdEncoding.EncodeToString(png),
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("reference overwrite status=%d body=%s", denied.Code, denied.Body.String())
	}
}
