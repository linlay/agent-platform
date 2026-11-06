package server

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/memory"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownMemoryHTTPConflictAndRetiredRoutes(t *testing.T) {
	root := t.TempDir()
	s := &Server{deps: Dependencies{Memory: memory.NewStore(filepath.Join(root, "memory"), filepath.Join(root, "owner"), nil)}}
	request := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/memory/file?kind=owner", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		s.handleMemoryFile(w, r)
		return w
	}
	w := request("PUT", `{"kind":"owner","content":"# Owner\n中文偏好","revision":"missing"}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "")
	var result struct {
		Data memory.Document `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Data.Exists || result.Data.Content != "# Owner\n中文偏好" {
		t.Fatalf("read: %+v", result.Data)
	}
	if w = request("PUT", `{"kind":"owner","content":"stale","revision":"missing"}`); w.Code != 409 {
		t.Fatalf("conflict: %d", w.Code)
	}
	if w = request("PUT", `{"kind":"daily","date":"../OWNER","content":"bad","revision":"missing"}`); w.Code != 400 {
		t.Fatalf("escape: %d", w.Code)
	}
	if w = request("PUT", `{"kind":"owner","scopeType":"global","revision":"missing"}`); w.Code != 400 {
		t.Fatalf("old schema: %d", w.Code)
	}
	if w = request("PUT", `{"kind":"owner","revision":"`+result.Data.Revision+`"}`); w.Code != 400 {
		t.Fatalf("missing content: %d", w.Code)
	}
}

func TestQueryLoadsFileMemoryAndFreezesOwnerUntilNextRun(t *testing.T) {
	root := t.TempDir()
	store := memory.NewStore(filepath.Join(root, "memory"), filepath.Join(root, "owner"), nil)
	if _, err := store.Save("memory", "", "shared long-term fact", "missing"); err != nil {
		t.Fatal(err)
	}
	owner, err := store.Save("owner", "", "original owner", "missing")
	if err != nil {
		t.Fatal(err)
	}
	chats, err := chat.NewFileStoreAtStartup(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Dependencies{Config: config.Config{Paths: config.PathsConfig{MemoryDir: store.MemoryDir, OwnerDir: store.OwnerDir}, Memory: config.MemoryConfig{Enabled: true, ContextMaxChars: 12000}}, Chats: chats, Memory: store, Registry: queryMemoryRegistry{def: catalog.AgentDefinition{Key: "agent-a", Name: "Agent A", Mode: "GENERAL", ModelKey: "mock-model", MemoryEnabled: false, ContextTags: []string{"owner", "memory-global"}}}}}
	prepare := func() preparedQuery {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"hello"}`))
		p, err := prepareQueryForTest(s, req)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := prepare()
	if !strings.Contains(first.Session.GlobalMemoryContext, "shared long-term fact") || first.Session.OwnerPrompt != "original owner" {
		t.Fatalf("missing file context: %+v", first.Session)
	}
	if _, err := store.Save("owner", "", "updated owner", owner.Revision); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Read("summary", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("summary", "", "updated shared fact", doc.Revision); err != nil {
		t.Fatal(err)
	}
	second := prepare()
	if !strings.Contains(first.Session.GlobalMemoryContext, "shared long-term fact") || !strings.Contains(second.Session.GlobalMemoryContext, "updated shared fact") {
		t.Fatal("memory snapshot not frozen per run")
	}
	if first.Session.OwnerPrompt != "original owner" || second.Session.OwnerPrompt != "updated owner" {
		t.Fatal("owner snapshot not frozen per run")
	}
	s.deps.Config.Memory.Enabled = false
	s = &Server{deps: s.deps}
	if disabled := prepare(); disabled.Session.GlobalMemoryContext != "" {
		t.Fatal("disabled memory injected")
	}
}

func TestQueryMemoryTagsIndependentOfCollection(t *testing.T) {
	root := t.TempDir()
	store := memory.NewStore(filepath.Join(root, "memory"), filepath.Join(root, "owner"), nil)
	if _, err := store.Save("summary", "", "global-fact", "missing"); err != nil {
		t.Fatal(err)
	}
	for key, body := range map[string]string{"agent-a": "agent-a-fact", "agent-b": "agent-b-secret"} {
		dir := filepath.Join(store.MemoryDir, "agents", key)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, enabled := range []bool{false, true} {
		for _, tags := range [][]string{nil, {"memory-global"}, {"memory-agent"}, {"memory-agent", "memory-global"}} {
			t.Run(fmt.Sprintf("enabled=%v/tags=%v", enabled, tags), func(t *testing.T) {
				chats, err := chat.NewFileStoreAtStartup(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				s := &Server{deps: Dependencies{Config: config.Config{Paths: config.PathsConfig{MemoryDir: store.MemoryDir, OwnerDir: store.OwnerDir}, Memory: config.MemoryConfig{Enabled: true}}, Chats: chats, Memory: store, Registry: queryMemoryRegistry{def: catalog.AgentDefinition{Key: "agent-a", Name: "A", Mode: "GENERAL", ModelKey: "mock-model", MemoryEnabled: enabled, ContextTags: tags}}}}
				req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"hello"}`))
				p, err := prepareQueryForTest(s, req)
				if err != nil {
					t.Fatal(err)
				}
				global, agent := false, false
				for _, tag := range tags {
					global = global || tag == "memory-global"
					agent = agent || tag == "memory-agent"
				}
				if strings.Contains(p.Session.GlobalMemoryContext, "global-fact") != global || strings.Contains(p.Session.AgentMemoryContext, "agent-a-fact") != agent {
					t.Fatalf("wrong context: global=%q agent=%q", p.Session.GlobalMemoryContext, p.Session.AgentMemoryContext)
				}
				if strings.Contains(p.Session.AgentMemoryContext, "agent-b-secret") {
					t.Fatal("another agent memory leaked")
				}
			})
		}
	}
}
