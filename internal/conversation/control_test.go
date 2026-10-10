package conversation

import (
	"agent-platform/internal/chat"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func controlChatFixture(t *testing.T) (*Service, *chat.FileStore, ControlCaller) {
	t.Helper()
	store, e := chat.NewFileStoreAtStartup(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	for _, id := range []string{"current", "target", "foreign"} {
		owner := "user"
		if id == "foreign" {
			owner = "other"
		}
		if _, _, e = store.EnsureChatWithSourceAndMode(id, "agent", id, "query:"+owner, "GENERAL"); e != nil {
			t.Fatal(e)
		}
	}
	service := NewService(store, nil, nil, nil)
	service.ControlStateDir = t.TempDir()
	return service, store, ControlCaller{Subject: "user", ChatID: "current", AgentKey: "agent", RunID: "caller-run", ToolID: "tool-1"}
}
func controlAppendRun(t *testing.T, store *chat.FileStore, id, run, answer string) {
	t.Helper()
	ts := time.Now().UnixMilli()
	if e := store.OnRunStarted(chat.RunStart{ChatID: id, RunID: run, AgentKey: "agent", StartedAtMillis: ts}); e != nil {
		t.Fatal(e)
	}
	if e := store.AppendQueryLine(id, chat.QueryLine{Type: "query", ChatID: id, RunID: run, UpdatedAt: ts, Query: map[string]any{"chatId": id, "runId": run, "role": "user", "message": "hello"}, Messages: []map[string]any{{"role": "user", "content": "hello", "ts": ts}}}); e != nil {
		t.Fatal(e)
	}
	if e := store.AppendStepLine(id, chat.StepLine{Type: chat.StepLineTypeReact, ChatID: id, RunID: run, UpdatedAt: ts + 1, Messages: []chat.StoredMessage{{Role: "assistant", Content: []chat.ContentPart{{Type: "text", Text: answer}}, ReasoningContent: []chat.ContentPart{{Type: "text", Text: "private-reasoning"}}, Ts: &ts}}}); e != nil {
		t.Fatal(e)
	}
	if e := store.OnRunCompleted(chat.RunCompletion{ChatID: id, RunID: run, AgentKey: "agent", InitialMessage: "hello", AssistantText: answer, FinishReason: "complete", StartedAtMillis: ts, UpdatedAtMillis: ts + 2}); e != nil {
		t.Fatal(e)
	}
}
func TestControlChatOwnerAndVisiblePagination(t *testing.T) {
	s, store, c := controlChatFixture(t)
	controlAppendRun(t, store, "target", "run-1", strings.Repeat("正文", 12000))
	if _, e := s.ResolveControlChat(c, "foreign", false); e == nil {
		t.Fatal("foreign owner accepted")
	}
	anonymous := c
	anonymous.Subject = ""
	if _, e := s.ResolveControlChat(anonymous, "target", false); e == nil {
		t.Fatal("anonymous cross-chat access")
	}
	args := map[string]any{"chatId": "target", "view": "messages", "limit": float64(2)}
	var content strings.Builder
	pages := 0
	for {
		page, e := s.ControlRead(c, args)
		if e != nil {
			t.Fatal(e)
		}
		for _, m := range page["messages"].([]ControlMessage) {
			content.WriteString(m.Content)
		}
		pages++
		next := page["nextCursor"].(string)
		if next == "" {
			break
		}
		args["cursor"] = next
		if pages > 5 {
			t.Fatal("unbounded pagination")
		}
	}
	if !strings.Contains(content.String(), strings.Repeat("正文", 12000)) || strings.Contains(content.String(), "private-reasoning") || pages < 2 {
		t.Fatalf("bad visible pagination: pages=%d runes=%d", pages, len([]rune(content.String())))
	}
}
func TestControlExportReceiptsAndDeleteRevision(t *testing.T) {
	s, store, c := controlChatFixture(t)
	controlAppendRun(t, store, "target", "run-1", "answer")
	args := map[string]any{"chatId": "target", "format": "snapshot"}
	first, e := s.ControlManage(c, "export", args, "")
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.ControlManage(c, "export", args, "")
	if e != nil || first["url"] != second["url"] {
		t.Fatalf("receipt %v %v", second, e)
	}
	b, e := os.ReadFile(filepath.Join(store.ChatDir("current"), first["url"].(string)))
	if e != nil || !json.Valid(b) || !strings.Contains(string(b), "private-reasoning") {
		t.Fatalf("export %s %v", b, e)
	}
	if _, e = s.ControlManage(c, "export", map[string]any{"chatId": "target", "format": "markdown"}, ""); e == nil {
		t.Fatal("changed arguments reused receipt")
	}
	rev, e := s.ControlDeleteRevision(c, "target", false)
	if e != nil {
		t.Fatal(e)
	}
	controlAppendRun(t, store, "target", "run-2", "later")
	if _, e = s.ControlManage(c, "delete", map[string]any{"chatId": "target"}, rev); e == nil {
		t.Fatal("stale approval deleted history")
	}
	for _, action := range []string{"archive", "fork", "delete"} {
		if _, e = s.ControlManage(c, action, map[string]any{"chatId": "current", "sourceChatId": "current"}, rev); e == nil {
			t.Fatalf("active caller %s accepted", action)
		}
	}
}

func TestControlBoundedListAndSearch(t *testing.T) {
	s, store, c := controlChatFixture(t)
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("page-%03d", i)
		if _, _, err := store.EnsureChatWithSourceAndMode(id, "agent", id, "query:user", "GENERAL"); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"limit": float64(7)}
	seen := map[string]bool{}
	for page := 0; ; page++ {
		if page > 30 {
			t.Fatal("pagination did not finish")
		}
		result, err := s.ControlList(c, args)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range result["chats"].([]chat.Summary) {
			if seen[v.ChatID] {
				t.Fatal("duplicate")
			}
			seen[v.ChatID] = true
		}
		next := result["nextCursor"].(string)
		if len(next) > 512 {
			t.Fatal("cursor grew with catalog")
		}
		if next == "" {
			break
		}
		args["cursor"] = next
	}
	if len(seen) != 122 || seen["foreign"] {
		t.Fatalf("visible count=%d", len(seen))
	}
	controlAppendRun(t, store, "target", "search-run", strings.Repeat("前文", 1000)+"needle"+strings.Repeat("尾", 200))
	result, err := s.ControlSearch(c, map[string]any{"chatId": "target", "query": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	hits := result["hits"].([]map[string]any)
	if len(hits) != 1 || !strings.Contains(hits[0]["snippet"].(string), "needle") || len([]rune(hits[0]["snippet"].(string))) > 500 {
		t.Fatalf("bad snippet: %+v", hits)
	}
	result, err = s.ControlSearch(c, map[string]any{"query": "absent"})
	if err != nil {
		t.Fatal(err)
	}
	if result["nextCursor"] == "" || result["incomplete"] != true {
		t.Fatal("scan budget falsely reported completion")
	}
}

func TestControlForkRejectsUnprovenExistingTarget(t *testing.T) {
	s, store, c := controlChatFixture(t)
	controlAppendRun(t, store, "target", "run-1", "answer")
	stem := "control-" + controlDigest([]string{c.RunID, c.ToolID})[:32]
	if _, _, err := store.EnsureChatWithSourceAndMode(stem, "agent", "unrelated", "query:user", "GENERAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlManage(c, "fork", map[string]any{"sourceChatId": "target"}, ""); err == nil || !strings.Contains(err.Error(), "idempotency_conflict") {
		t.Fatalf("adopted unrelated fork: %v", err)
	}
}

func TestConversationViewsShareMutationLock(t *testing.T) {
	s, _, _ := controlChatFixture(t)
	view := s.WithDependencies(nil, nil, nil, nil, nil)
	release := s.LockMutation()
	started := make(chan struct{})
	acquired := make(chan struct{})
	go func() { close(started); unlock := view.LockMutation(); close(acquired); unlock() }()
	<-started
	select {
	case <-acquired:
		t.Fatal("view acquired independent mutex")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("shared lock never released")
	}
}

func TestControlCursorHidesScannedIdentitiesAndRejectsTampering(t *testing.T) {
	c := ControlCaller{Subject: "owner", AgentKey: "agent"}
	args := map[string]any{"scope": "instance"}
	cur, err := controlPage(args, c)
	if err != nil {
		t.Fatal(err)
	}
	cur.After = "another-owner-private-chat"
	encoded, err := controlNext(cur)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), cur.After) || json.Valid(bytes) {
		t.Fatal("cursor exposes scanned identities")
	}
	args["cursor"] = encoded
	decoded, err := controlPage(args, c)
	if err != nil || decoded.After != cur.After {
		t.Fatal("cursor failed to resume", err)
	}
	bytes[len(bytes)-1] ^= 1
	args["cursor"] = base64.RawURLEncoding.EncodeToString(bytes)
	if _, err := controlPage(args, c); err == nil {
		t.Fatal("tampered cursor accepted")
	}
	args["cursor"] = encoded
	c.Subject = "other"
	if _, err := controlPage(args, c); err == nil {
		t.Fatal("cursor crossed owner boundary")
	}
}
