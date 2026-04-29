package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutionStoreRecordsAndListsExecutions(t *testing.T) {
	root := t.TempDir()
	store, err := NewExecutionStore(root, "executions.db")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	defer store.Close()

	if _, err := os.Stat(filepath.Join(root, "executions.db")); err != nil {
		t.Fatalf("expected db file: %v", err)
	}

	firstID, err := store.RecordStart("daily", "Daily", "/tmp/daily.yml", "agent-a", "team-a")
	if err != nil {
		t.Fatalf("record first start: %v", err)
	}
	if firstID == "" {
		t.Fatal("expected execution id")
	}
	items, total, err := store.ListBySchedule("daily", 10, 0)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one running item, total=%d items=%#v", total, items)
	}
	if items[0].Status != "running" || items[0].CompletedAt != nil || items[0].DurationMs != nil {
		t.Fatalf("unexpected running execution %#v", items[0])
	}

	time.Sleep(time.Millisecond)
	if err := store.RecordComplete(firstID, nil); err != nil {
		t.Fatalf("record success: %v", err)
	}
	last, err := store.LastExecution("daily")
	if err != nil {
		t.Fatalf("last execution: %v", err)
	}
	if last == nil || last.ID != firstID || last.Status != "success" || last.CompletedAt == nil || last.DurationMs == nil {
		t.Fatalf("unexpected successful execution %#v", last)
	}

	secondID, err := store.RecordStart("daily", "Daily", "/tmp/daily.yml", "agent-a", "team-a")
	if err != nil {
		t.Fatalf("record second start: %v", err)
	}
	if err := store.RecordComplete(secondID, errors.New("boom")); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	recent, total, err := store.ListRecent(1, 0)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if total != 2 || len(recent) != 1 || recent[0].ID != secondID || recent[0].Status != "failed" || recent[0].Error != "boom" {
		t.Fatalf("unexpected recent executions total=%d items=%#v", total, recent)
	}

	paged, total, err := store.ListBySchedule("daily", 1, 1)
	if err != nil {
		t.Fatalf("list paged: %v", err)
	}
	if total != 2 || len(paged) != 1 || paged[0].ID != firstID {
		t.Fatalf("unexpected paged executions total=%d items=%#v", total, paged)
	}
}

func TestExecutionStoreDefaultPagingAndMissingLast(t *testing.T) {
	store, err := NewExecutionStore(t.TempDir(), "")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	defer store.Close()

	last, err := store.LastExecution("missing")
	if err != nil {
		t.Fatalf("missing last: %v", err)
	}
	if last != nil {
		t.Fatalf("expected nil last execution, got %#v", last)
	}

	for i := 0; i < 105; i++ {
		if _, err := store.RecordStart("many", "Many", "", "", ""); err != nil {
			t.Fatalf("record start %d: %v", i, err)
		}
	}
	items, total, err := store.ListBySchedule("many", 500, -10)
	if err != nil {
		t.Fatalf("list capped: %v", err)
	}
	if total != 105 || len(items) != 100 {
		t.Fatalf("expected capped page of 100/105, got len=%d total=%d", len(items), total)
	}
}
