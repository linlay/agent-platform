package conversation

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenedSummariesIntersectionAndLimit(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"a", "b", "c"} {
		agent := "worker"
		if id == "c" {
			agent = "other"
		}
		if _, _, err := store.EnsureChatWithSourceAndMode(id, agent, id, "query:user", "GENERAL"); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(store, nil, nil, nil)
	s.MailboxOpened = func(agent, subject string) ([]map[string]any, error) {
		if agent != "lead" || subject != "user" {
			return nil, nil
		}
		return []map[string]any{{"chatId": "a"}, {"chatId": "c"}}, nil
	}
	rows, err := s.ListSummariesWithOptions(chat.ListOptions{OpenedBy: "lead", Subject: "user", AgentKey: "worker", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ChatID != "a" {
		t.Fatal(rows, err)
	}
	rows, err = s.ListSummariesWithOptions(chat.ListOptions{OpenedBy: "lead", Subject: "other"})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

type openedNoJSONLStore struct {
	*chat.FileStore
	reads int
}

func (s *openedNoJSONLStore) LoadJSONLContent(id string) (string, error) {
	s.reads++
	return s.FileStore.LoadJSONLContent(id)
}
func TestOpenedSummaryListNeverReadsJSONL(t *testing.T) {
	files, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if _, _, err := files.EnsureChatWithSourceAndMode("work", "worker", "task", "query:user", "GENERAL"); err != nil {
		t.Fatal(err)
	}
	tracked := &openedNoJSONLStore{FileStore: files}
	s := NewService(tracked, nil, nil, nil)
	s.MailboxOpened = func(string, string) ([]map[string]any, error) { return []map[string]any{{"chatId": "work"}}, nil }
	rows, err := s.ListSummariesWithOptions(chat.ListOptions{OpenedBy: "lead", Subject: "user"})
	if err != nil || len(rows) != 1 || tracked.reads != 0 {
		t.Fatal(rows, tracked.reads, err)
	}
}

type openedBatchRuns struct {
	contracts.RunManager
	calls int
}

func (s *openedBatchRuns) ActiveRunsForChats(ids []string) (map[string]contracts.RunStatusInfo, map[string]*contracts.ActiveRunConflictError) {
	s.calls++
	return map[string]contracts.RunStatusInfo{"work": {RunID: "current", ChatID: "work", State: contracts.RunLoopStateModelStreaming}}, nil
}
func TestControlOpenedIsCompactAndKeepsControlAddress(t *testing.T) {
	files, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if _, _, err := files.EnsureChatWithSourceAndMode("work", "worker", "task", "query:user", "GENERAL"); err != nil {
		t.Fatal(err)
	}
	tracked := &openedNoJSONLStore{FileStore: files}
	runs := &openedBatchRuns{}
	s := NewService(tracked, nil, nil, runs)
	s.MailboxOpened = func(agent, subject string) ([]map[string]any, error) {
		if agent != "lead" || subject != "user" {
			t.Fatal(agent, subject)
		}
		return []map[string]any{{"chatId": "work", "mailId": "letter", "runId": "owned"}}, nil
	}
	rows, err := s.ControlOpened(ControlCaller{AgentKey: "lead", Subject: "user"})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	row := rows[0]
	if row.MailID != "letter" || row.RunID != "owned" || row.Status != "running" || row.ActiveRun.RunID != "current" || runs.calls != 1 || tracked.reads != 0 {
		t.Fatal(rows, runs.calls, tracked.reads)
	}
	data, _ := json.Marshal(row)
	if strings.Contains(string(data), "lastRunContent") {
		t.Fatal(string(data))
	}
}
