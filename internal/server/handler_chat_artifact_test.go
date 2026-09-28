package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
)

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
