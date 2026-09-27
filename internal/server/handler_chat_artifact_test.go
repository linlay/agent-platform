package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
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
