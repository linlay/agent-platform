package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryOnlySettledChats(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().Add(-time.Hour).UnixMilli()
	for _, id := range []string{"completed", "active", "awaiting", "automatic"} {
		source := "query"
		if id == "automatic" {
			source = "automation:daily"
		}
		if _, _, err = s.EnsureChatWithSource(id, "agent", "", "hello", source); err != nil {
			t.Fatal(err)
		}
		if err = s.OnRunStarted(RunStart{ChatID: id, RunID: id, AgentKey: "agent", StartedAtMillis: at}); err != nil {
			t.Fatal(err)
		}
		if id != "active" {
			if err = s.OnRunCompleted(RunCompletion{ChatID: id, RunID: id, AgentKey: "agent", FinishReason: "stop", StartedAtMillis: at, UpdatedAtMillis: at + 1000}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.db.Exec("UPDATE CHATS SET AWAITING_ID_='pending' WHERE CHAT_ID_='awaiting'"); err != nil {
		t.Fatal(err)
	}
	items, err := s.MemoryChats(context.Background(), at, 0, "", 200)
	if err != nil || len(items) != 1 || items[0].ChatID != "completed" {
		t.Fatal(items, err)
	}
	if err = s.OnRunStarted(RunStart{ChatID: "completed", RunID: "reopened", AgentKey: "agent", StartedAtMillis: at + 2000}); err != nil {
		t.Fatal(err)
	}
	items, err = s.MemoryChats(context.Background(), at, 0, "", 200)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}
func TestMemoryEvidenceOriginalTextWithoutSyntheticContext(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw := `{"_type":"query","runId":"r","updatedAt":1791158400000,"query":{"role":"user","message":"请用中文"},"_compact":{"level":"L1","id":"compact-1"}}
{"_type":"query","runId":"r","updatedAt":1791158400001,"query":{"kind":"system-init","message":"secret prompt"}}
{"_type":"query","runId":"r","taskId":"child","updatedAt":1791158400002,"query":{"message":"delegation"}}
{"_type":"steer","chatId":"chat","runId":"r","updatedAt":1791158400003,"steer":{"chatId":"chat","runId":"r","steerId":"s1","role":"user","message":"改用英文"}}
{"_type":"steer","chatId":"chat","runId":"r","updatedAt":1791158400004,"steer":{"chatId":"chat","runId":"r","steerId":"s2","role":"user","message":""}}
{"_type":"react","runId":"r","updatedAt":1791158400005,"messages":[{"role":"assistant","ts":1791158400005,"content":[{"type":"text","text":"已完成"}],"reasoning_content":[{"text":"secret reasoning"}]}]}
{"_type":"compact.checkpoint","runId":"r","updatedAt":1791158400006,"summary":"synthetic"}
`
	if err = os.WriteFile(filepath.Join(s.root, "chat.jsonl"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := s.MemoryMessages("chat", "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("%+v", out)
	}
	if out[0].Content != "请用中文" || out[1].Content != "改用英文" || strings.Contains(out[2].Content, "secret") {
		t.Fatal(out)
	}
}
