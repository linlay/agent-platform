package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/stream"
)

type publishedReader struct {
	handlerKBaseService
	calls int
}

func (p *publishedReader) ReadBound(agent, id string, o knowledge.ReadOptions) (knowledge.ReadResult, error) {
	p.calls++
	if agent != "docs" || id != "research" || o.Path != "second/same.md" {
		panic("incorrect published scope")
	}
	return knowledge.ReadResult{Found: true, Path: o.Path, Content: "original"}, nil
}
func (*publishedReader) OpenBoundSource(string, string, string) (*os.File, error) {
	return nil, os.ErrNotExist
}
func TestKnowledgeSourceRequiresOwnedPublishedReference(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.EnsureChatWithSource("owned-chat", "docs", "hello", api.ChatSourceQueryPrefix+"alice")
	if err = store.OnRunStarted(chat.RunStart{ChatID: "owned-chat", RunID: "run-1", AgentKey: "docs", StartedAtMillis: testEpochMillis - 1}); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendEvent("owned-chat", stream.EventData{Type: "run.start", Timestamp: testEpochMillis - 1, Payload: map[string]any{"chatId": "owned-chat", "runId": "run-1"}}); err != nil {
		t.Fatal(err)
	}
	source := stream.Source{ID: "kbase:research/second/same.md", LibraryID: "research", AgentKey: "docs", Chunks: []stream.SourceChunk{{ChunkID: "evidence", Content: "snippet"}}}
	if err = store.AppendEvent("owned-chat", stream.EventData{Type: "source.publish", Timestamp: testEpochMillis, Payload: map[string]any{"runId": "run-1", "publishId": "publish-1", "kind": "kbase", "sources": []stream.Source{source}}}); err != nil {
		t.Fatal(err)
	}
	reader := &publishedReader{}
	s := &Server{deps: Dependencies{Chats: store, KBase: reader}, chatResources: chatresource.NewService(store)}
	for _, tc := range []struct {
		subject, id string
		status      int
	}{{"", source.ID, 401}, {"bob", source.ID, 403}, {"alice", "kbase:research/second/private.md", 404}, {"alice", source.ID, 200}} {
		body, _ := json.Marshal(map[string]any{"chatId": "owned-chat", "sourceId": tc.id})
		r := httptest.NewRequest("POST", "/api/chat/sources/read", strings.NewReader(string(body)))
		if tc.subject != "" {
			r = r.WithContext(WithPrincipal(context.Background(), &Principal{Subject: tc.subject}))
		}
		w := httptest.NewRecorder()
		s.handleKnowledgeSource(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	if reader.calls != 1 {
		t.Fatal("unauthorized CLI invocation", reader.calls)
	}
}
