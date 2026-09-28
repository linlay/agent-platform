package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/config"
)

func TestChatDetailArtifactPublishedAt(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-artifact-time"
	if _, _, err := fixture.chats.EnsureChat(chatID, "agent-a", "", "Artifacts"); err != nil {
		t.Fatal(err)
	}
	writer := fixture.chats.(chat.ArtifactManifestWriter)
	for i, id := range []string{"first", "second"} {
		if err := writer.AppendArtifactManifest(chatID, "run-1", testEpochMillis+int64(i), []map[string]any{{
			"artifactId": id, "type": "file", "name": "result.html", "url": "artifacts/run-1/result.html",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[struct {
		Artifact *chat.ArtifactState `json:"artifact"`
	}]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Artifact == nil || len(response.Data.Artifact.Items) != 2 {
		t.Fatalf("unexpected artifacts: %s", rec.Body.String())
	}
	for i, item := range response.Data.Artifact.Items {
		if item.PublishedAt != testEpochMillis+int64(i) {
			t.Fatalf("item %d publishedAt=%d", i, item.PublishedAt)
		}
	}
}

func TestChatArtifactChecksPrincipalWithoutConnectorGrant(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err = store.EnsureChatWithSource("owned-chat", "agent", "", "hello", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Dependencies{Chats: store}, chatResources: chatresource.NewService(store)}
	for _, tc := range []struct {
		subject string
		status  int
	}{{"", 401}, {"bob", 403}, {"alice", 200}} {
		r := httptest.NewRequest("POST", "/api/chat/artifacts/list", strings.NewReader(`{"chatId":"owned-chat"}`))
		if tc.subject != "" {
			r = r.WithContext(WithPrincipal(context.Background(), &Principal{Subject: tc.subject}))
		}
		w := httptest.NewRecorder()
		s.handleChatArtifact(w, r)
		if w.Code != tc.status {
			t.Fatalf("subject=%q: %d %s", tc.subject, w.Code, w.Body.String())
		}
	}
}

func TestChatArtifactSourceRefReadHasNarrowDesktopAuthority(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chatID = "owned-chat-source-ref"
	const sourceRef = "artifacts/run-1/report.pdf"
	if _, _, err = store.EnsureChatWithSource(chatID, "agent", "", "hello", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	data := []byte("%PDF-1.7\nreport")
	path := filepath.Join(store.ChatDir(chatID), filepath.FromSlash(sourceRef))
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if err = store.AppendArtifactManifest(chatID, "run-1", 1, []map[string]any{{
		"artifactId": "artifact-1", "type": "file", "url": sourceRef, "name": "report.pdf",
		"mimeType": "application/pdf", "sizeBytes": len(data), "sha256": hex.EncodeToString(digest[:]),
	}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Dependencies{Chats: store}, chatResources: chatresource.NewService(store)}
	request := func(route, body string, principal *Principal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
		if principal != nil {
			r = r.WithContext(WithPrincipal(r.Context(), principal))
		}
		w := httptest.NewRecorder()
		s.handleChatArtifact(w, r)
		return w
	}
	owner := &Principal{Subject: "alice", Claims: map[string]any{"scope": "user"}}
	app := &Principal{Subject: "desktop", Claims: map[string]any{"scope": "app", "device_id": "device-1"}}
	other := &Principal{Subject: "bob", Claims: map[string]any{"scope": "user", "device_id": "device-1"}}

	if rec := request("/api/chat/artifacts/read", `{"chatId":"`+chatID+`","artifactId":"artifact-1"}`, owner); rec.Code != http.StatusOK || rec.Body.String() != string(data) {
		t.Fatalf("owner artifactId read status=%d body=%q", rec.Code, rec.Body.String())
	}
	s.deps.Config.RuntimeMode = config.RuntimeModeDesktop
	if rec := request("/api/chat/artifacts/read", `{"chatId":"`+chatID+`","sourceRef":"`+sourceRef+`"}`, app); rec.Code != http.StatusOK || rec.Body.String() != string(data) {
		t.Fatalf("desktop sourceRef read status=%d body=%q", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		name, route, body string
		principal         *Principal
	}{
		{name: "list", route: "/api/chat/artifacts/list", body: `{"chatId":"` + chatID + `"}`, principal: app},
		{name: "get", route: "/api/chat/artifacts/get", body: `{"chatId":"` + chatID + `","artifactId":"artifact-1"}`, principal: app},
		{name: "artifact id read", route: "/api/chat/artifacts/read", body: `{"chatId":"` + chatID + `","artifactId":"artifact-1"}`, principal: app},
		{name: "ordinary principal", route: "/api/chat/artifacts/read", body: `{"chatId":"` + chatID + `","sourceRef":"` + sourceRef + `"}`, principal: other},
		{name: "app without device", route: "/api/chat/artifacts/read", body: `{"chatId":"` + chatID + `","sourceRef":"` + sourceRef + `"}`, principal: &Principal{Subject: "desktop", Claims: map[string]any{"scope": "app"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := request(tc.route, tc.body, tc.principal); rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	s.deps.Config.RuntimeMode = config.RuntimeModeStandalone
	if rec := request("/api/chat/artifacts/read", `{"chatId":"`+chatID+`","sourceRef":"`+sourceRef+`"}`, app); rec.Code != http.StatusForbidden {
		t.Fatalf("standalone sourceRef read status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatArtifactReadRequiresExactlyOneLocator(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err = store.EnsureChat("chat-artifact-locator", "agent", "", "hello"); err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Dependencies{Chats: store}, chatResources: chatresource.NewService(store)}
	principal := &Principal{Subject: "owner"}
	for _, body := range []string{
		`{"chatId":"chat-artifact-locator"}`,
		`{"chatId":"chat-artifact-locator","artifactId":"a","sourceRef":"artifacts/run-1/a.txt"}`,
		`{"chatId":"chat-artifact-locator","sourceRef":"artifacts/run-1/a.txt","runId":"run-1"}`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/chat/artifacts/read", strings.NewReader(body))
		r = r.WithContext(WithPrincipal(context.Background(), principal))
		w := httptest.NewRecorder()
		s.handleChatArtifact(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, w.Code, w.Body.String())
		}
	}
}
