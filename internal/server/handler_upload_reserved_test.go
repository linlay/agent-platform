package server

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reservedUploadRequest(t *testing.T, server *Server, internal bool, chatID, fileName string) (int, []byte) {
	t.Helper()
	if internal {
		status, body, err := server.ExecuteInternalUpload(context.Background(), chatID, "reserved-upload", fileName, "text/plain", []byte("replacement"))
		if err != nil {
			t.Fatal(err)
		}
		return status, body
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chatId", chatID); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.handleUpload(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestUploadReservedNameRejectedBeforeChatCreation(t *testing.T) {
	for _, internal := range []bool{false, true} {
		for _, chatID := range []string{"", "new-chat"} {
			for _, name := range []string{".uploads.jsonl", ".UPLOADS.JSONL", "folder/.uploads.jsonl", " .uploads.jsonl "} {
				t.Run(strings.Join([]string{map[bool]string{false: "http", true: "internal"}[internal], chatID, name}, "/"), func(t *testing.T) {
					// No store or notification sink: rejection must precede all Chat mutations.
					status, body := reservedUploadRequest(t, &Server{}, internal, chatID, name)
					if status != http.StatusBadRequest || !strings.Contains(string(body), "reserved") {
						t.Fatalf("expected reserved-name error, got %d: %s", status, body)
					}
				})
			}
		}
	}
}

func TestUploadReservedNamePreservesManifestAndSameNameOverwrite(t *testing.T) {
	fixture := newTestFixture(t)
	first := postTestUpload(t, fixture.server, "", "first", "notes.txt", "first content")
	assertUUIDLike(t, first.ChatID)
	manifest := filepath.Join(fixture.chats.ChatDir(first.ChatID), uploadManifestName)
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, internal := range []bool{false, true} {
		status, body := reservedUploadRequest(t, fixture.server, internal, first.ChatID, uploadManifestName)
		if status != http.StatusBadRequest {
			t.Fatalf("expected rejection, got %d: %s", status, body)
		}
		after, err := os.ReadFile(manifest)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("reserved upload changed manifest: %v", err)
		}
	}
	second := postTestUpload(t, fixture.server, first.ChatID, "second", "notes.txt", "second content")
	if second.ChatID != first.ChatID || second.Upload.ID != "r02" {
		t.Fatalf("rejected upload changed Chat or consumed an upload ID: %#v", second)
	}
	content, err := os.ReadFile(filepath.Join(fixture.chats.ChatDir(first.ChatID), "notes.txt"))
	if err != nil || string(content) != "second content" {
		t.Fatalf("ordinary same-name upload did not overwrite: content=%q err=%v", content, err)
	}
}
