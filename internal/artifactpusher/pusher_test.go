package artifactpusher

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type recordingNotifications struct {
	eventType string
	data      map[string]any
}

func (r *recordingNotifications) Broadcast(eventType string, data map[string]any) {
	r.eventType = eventType
	r.data = data
}

type staticResolver struct {
	baseURL string
}

func (r staticResolver) Resolve(string) (string, string, bool) {
	return r.baseURL, "", true
}

func TestPushOneSendsResourcePushedAfterUploadSuccess(t *testing.T) {
	uploadCalled := false
	var uploadMethod, uploadPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadCalled = true
		uploadMethod = r.Method
		uploadPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	writeArtifactFile(t, chatsDir, "chat-1/report.txt", []byte("hello"))

	notifications := &recordingNotifications{}
	pusher := &Pusher{
		resolver:      staticResolver{baseURL: server.URL},
		uploadPath:    "/upload",
		chatsDir:      chatsDir,
		http:          server.Client(),
		notifications: notifications,
	}

	pusher.pushOne("chat-1", map[string]any{
		"artifactId": "artifact-1",
		"name":       "report.txt",
		"mimeType":   "text/plain",
		"type":       "file",
		"url":        "/api/resource?file=chat-1/report.txt",
	})

	if !uploadCalled {
		t.Fatalf("expected upload request")
	}
	if uploadMethod != http.MethodPost || uploadPath != "/upload" {
		t.Fatalf("unexpected upload request %s %s", uploadMethod, uploadPath)
	}
	if notifications.eventType != "resource.pushed" {
		t.Fatalf("expected resource.pushed notification, got %q", notifications.eventType)
	}
	if notifications.data["chatId"] != "chat-1" || notifications.data["artifactId"] != "artifact-1" || notifications.data["name"] != "report.txt" || notifications.data["mimeType"] != "text/plain" {
		t.Fatalf("unexpected notification data: %#v", notifications.data)
	}
	if notifications.data["sha256"] == "" || notifications.data["sizeBytes"] != 5 {
		t.Fatalf("expected sha and size in notification data: %#v", notifications.data)
	}
	if pushedAt, ok := notifications.data["pushedAt"].(int64); !ok || pushedAt <= 0 {
		t.Fatalf("expected pushedAt in notification data: %#v", notifications.data)
	}
	if _, exists := notifications.data["timestamp"]; exists {
		t.Fatalf("resource.pushed must not include timestamp: %#v", notifications.data)
	}
}

func TestPushOneDoesNotSendResourcePushedAfterUploadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("nope"))
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	writeArtifactFile(t, chatsDir, "chat-1/report.txt", []byte("hello"))

	notifications := &recordingNotifications{}
	pusher := &Pusher{
		resolver:      staticResolver{baseURL: server.URL},
		uploadPath:    "/upload",
		chatsDir:      chatsDir,
		http:          server.Client(),
		notifications: notifications,
	}

	pusher.pushOne("chat-1", map[string]any{
		"artifactId": "artifact-1",
		"name":       "report.txt",
		"mimeType":   "text/plain",
		"type":       "file",
		"url":        "/api/resource?file=chat-1/report.txt",
	})

	if notifications.eventType != "" || notifications.data != nil {
		t.Fatalf("did not expect notification after failed upload, got type=%q data=%#v", notifications.eventType, notifications.data)
	}
}

func writeArtifactFile(t *testing.T, root string, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write artifact file: %v", err)
	}
}
