package automation

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/timecontract"
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

	firstID, err := store.RecordStart("daily", "Daily", "/tmp/daily.yml", "agent-a", "team-a", "Asia/Shanghai")
	if err != nil {
		t.Fatalf("record first start: %v", err)
	}
	if firstID == "" {
		t.Fatal("expected execution id")
	}
	items, total, err := store.ListByAutomation("daily", 10, 0)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one running item, total=%d items=%#v", total, items)
	}
	if items[0].Status != "running" || items[0].ZoneID != "Asia/Shanghai" || items[0].CompletedAt != nil || items[0].DurationMs != nil {
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

	secondID, err := store.RecordStart("daily", "Daily", "/tmp/daily.yml", "agent-a", "team-a", "UTC")
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
	if total != 2 || len(recent) != 1 || recent[0].ID != secondID || recent[0].ZoneID != "UTC" || recent[0].Status != "failed" || recent[0].Error != "boom" {
		t.Fatalf("unexpected recent executions total=%d items=%#v", total, recent)
	}

	paged, total, err := store.ListByAutomation("daily", 1, 1)
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
		if _, err := store.RecordStart("many", "Many", "", "", "", "UTC"); err != nil {
			t.Fatalf("record start %d: %v", i, err)
		}
	}
	items, total, err := store.ListByAutomation("many", 500, -10)
	if err != nil {
		t.Fatalf("list capped: %v", err)
	}
	if total != 105 || len(items) != 100 {
		t.Fatalf("expected capped page of 100/105, got len=%d total=%d", len(items), total)
	}
}

func TestExecutionStoreRejectsInvalidPersistedTimes(t *testing.T) {
	store, err := NewExecutionStore(t.TempDir(), "executions.db")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(`INSERT INTO AUTOMATION_EXECUTIONS (
		ID_, AUTOMATION_ID_, ZONE_ID_, STARTED_AT_, COMPLETED_AT_
	) VALUES ('exec_bad', 'daily', 'UTC', 1700000000, 0)`); err != nil {
		t.Fatalf("insert invalid legacy execution: %v", err)
	}
	if _, _, err := store.ListByAutomation("daily", 10, 0); !timecontract.IsViolation(err) {
		t.Fatalf("expected invalid persisted execution times to fail, got %v", err)
	}
	if _, err := store.db.Exec(`UPDATE AUTOMATION_EXECUTIONS SET STARTED_AT_ = ? WHERE ID_ = 'exec_bad'`, time.Now().UnixMilli()); err != nil {
		t.Fatalf("repair only test start field: %v", err)
	}
	if _, err := store.LastExecution("daily"); !timecontract.IsViolation(err) {
		t.Fatalf("expected zero completedAt to fail instead of becoming absent, got %v", err)
	}
	if err := store.RecordComplete("exec_bad", nil); !timecontract.IsViolation(err) {
		t.Fatalf("expected completion to reject invalid stored start time, got %v", err)
	}
}

func TestExecutionStoreRequiresValidZoneID(t *testing.T) {
	store, err := NewExecutionStore(t.TempDir(), "executions.db")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	defer store.Close()

	if _, err := store.RecordStart("daily", "Daily", "", "agent-a", "", ""); err == nil || !strings.Contains(err.Error(), "zoneId is required") {
		t.Fatalf("expected required zoneId error, got %v", err)
	}
	if _, err := store.RecordStart("daily", "Daily", "", "agent-a", "", "Not/A_Real_Zone"); err == nil || !strings.Contains(err.Error(), "invalid zoneId") {
		t.Fatalf("expected invalid zoneId error, got %v", err)
	}
}

func TestExecutionStoreRejectsLegacySchemaWithoutDeletingDatabase(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "executions.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE AUTOMATION_EXECUTIONS (
		ID_ TEXT PRIMARY KEY,
		AUTOMATION_ID_ TEXT NOT NULL,
		AUTOMATION_NAME_ TEXT NOT NULL DEFAULT '',
		SOURCE_FILE_ TEXT NOT NULL DEFAULT '',
		AGENT_KEY_ TEXT NOT NULL DEFAULT '',
		TEAM_ID_ TEXT NOT NULL DEFAULT '',
		STATUS_ TEXT NOT NULL DEFAULT 'running',
		ERROR_ TEXT NOT NULL DEFAULT '',
		STARTED_AT_ INTEGER NOT NULL,
		COMPLETED_AT_ INTEGER,
		DURATION_MS_ INTEGER
	)`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO AUTOMATION_EXECUTIONS (ID_, AUTOMATION_ID_, STARTED_AT_) VALUES ('legacy', 'daily', 1700000000000)`); err != nil {
		t.Fatalf("insert legacy execution: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := NewExecutionStore(root, "executions.db")
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "ZONE_ID_ is missing") || !strings.Contains(err.Error(), "delete "+dbPath) {
		t.Fatalf("expected incompatible schema deletion guidance, got %v", err)
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Fatalf("legacy database must not be deleted: %v", statErr)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen legacy database: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM AUTOMATION_EXECUTIONS WHERE ID_='legacy'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy data changed: count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('IDX_EXEC_AUTOMATION', 'IDX_EXEC_STARTED')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy schema was modified: index count=%d err=%v", count, err)
	}
}
