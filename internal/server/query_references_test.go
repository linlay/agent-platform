package server

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

func TestPrepareSiteReferenceKeepsOnlyPointerMetadata(t *testing.T) {
	got, err := prepareSiteReference(api.Reference{
		ID:       "website:docs",
		Type:     "site",
		Name:     "Docs",
		Path:     "/untrusted/path",
		MimeType: "text/html",
		URL:      "https://example.com/docs",
		Meta: map[string]any{
			"kind":      "website",
			"updatedAt": float64(123),
			"content":   "must be removed",
		},
	})
	if err != nil {
		t.Fatalf("prepareSiteReference: %v", err)
	}
	if got.ID != "website:docs" || got.Name != "Docs" || got.URL != "https://example.com/docs" {
		t.Fatalf("unexpected site reference %#v", got)
	}
	if got.Path != "" || got.MimeType != "" || len(got.Meta) != 2 || got.Meta["content"] != nil {
		t.Fatalf("site reference retained untrusted content fields %#v", got)
	}
}

func TestPrepareSiteReferenceRejectsUnscopedEntryKey(t *testing.T) {
	_, err := prepareSiteReference(api.Reference{
		ID:   "docs",
		Type: "site",
		Name: "Docs",
		Meta: map[string]any{"kind": "website"},
	})
	statusErr, ok := err.(*statusError)
	if !ok || statusErr.code != "site_reference_unavailable" {
		t.Fatalf("expected site_reference_unavailable, got %#v", err)
	}
}

func TestBuildChatReferenceContextKeepsCompactSummaryAndRecentConversation(t *testing.T) {
	messages := []map[string]any{{
		"role":    "user",
		"content": "以下是此前对话的上下文压缩摘要。\n\ncompact facts",
	}}
	for index := 0; index < 15; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": "message-" + string(rune('a'+index)),
		})
	}
	messages = append(messages, map[string]any{
		"role":    "tool",
		"content": "private tool output",
	})

	got := buildChatReferenceContext(chat.Summary{
		ChatID:   "chat-source",
		ChatName: "Source chat",
	}, messages)
	for _, expected := range []string{
		`Referenced chat "Source chat" (chat-source).`,
		"[compact summary]",
		"compact facts",
		"message-o",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in context:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "message-a") || strings.Contains(got, "private tool output") {
		t.Fatalf("context did not enforce recent user/assistant window:\n%s", got)
	}
}

func TestPrepareChatReferenceRejectsSelfReference(t *testing.T) {
	server := &Server{}
	_, err := server.prepareChatReference(
		nil,
		"chat-current",
		api.Reference{Type: "chat", ID: "chat-current"},
	)
	statusErr, ok := err.(*statusError)
	if !ok || statusErr.code != "chat_reference_self" {
		t.Fatalf("expected chat_reference_self, got %#v", err)
	}
}

func TestPrepareChatReferenceReloadsTrustedHistory(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.EnsureChatWithSource(
		"chat-source",
		"coder",
		"",
		"original request",
		"query:alice",
	); err != nil {
		t.Fatalf("ensure source chat: %v", err)
	}
	const now = int64(1_700_000_000_000)
	if err := store.AppendQueryLine("chat-source", chat.QueryLine{
		Type:      "query",
		ChatID:    "chat-source",
		RunID:     "run-source",
		UpdatedAt: now,
		Query:     map[string]any{"role": "user", "message": "trusted reload"},
		Messages:  []map[string]any{{"role": "user", "content": "trusted reload", "ts": now}},
	}); err != nil {
		t.Fatalf("append source query: %v", err)
	}
	if err := store.AppendStepLine("chat-source", chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    "chat-source",
		RunID:     "run-source",
		UpdatedAt: now + 1,
		Messages: []chat.StoredMessage{{
			Role:    "assistant",
			Content: []chat.ContentPart{{Type: "text", Text: "trusted answer"}},
			Ts:      func() *int64 { value := now + 1; return &value }(),
		}},
	}); err != nil {
		t.Fatalf("append source answer: %v", err)
	}
	server := &Server{deps: Dependencies{Chats: store}}
	ctx := WithPrincipal(context.Background(), &Principal{Subject: "alice"})

	got, err := server.prepareChatReference(ctx, "chat-current", api.Reference{
		Type: "chat",
		ID:   "chat-source",
		Name: "spoofed name",
		Meta: map[string]any{"context": "spoofed context"},
	})
	if err != nil {
		t.Fatalf("prepare chat reference: %v", err)
	}
	contextText := strings.TrimSpace(got.Meta["context"].(string))
	if got.Name == "spoofed name" || !strings.Contains(contextText, "trusted reload") || !strings.Contains(contextText, "trusted answer") || strings.Contains(contextText, "spoofed context") {
		t.Fatalf("chat reference was not rebuilt from trusted storage: %#v", got)
	}
}

func TestPrepareChatReferenceRejectsAnotherQueryPrincipal(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.EnsureChatWithSource(
		"chat-source",
		"coder",
		"",
		"original request",
		"query:alice",
	); err != nil {
		t.Fatalf("ensure source chat: %v", err)
	}
	server := &Server{deps: Dependencies{Chats: store}}
	ctx := WithPrincipal(context.Background(), &Principal{Subject: "bob"})

	_, err = server.prepareChatReference(ctx, "chat-current", api.Reference{
		Type: "chat",
		ID:   "chat-source",
	})
	statusErr, ok := err.(*statusError)
	if !ok || statusErr.code != "chat_reference_forbidden" {
		t.Fatalf("expected chat_reference_forbidden, got %#v", err)
	}
}
