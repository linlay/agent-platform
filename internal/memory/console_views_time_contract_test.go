package memory

import (
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/timecontract"
)

func TestFactRawFieldsOmitAbsentTimesAndRejectStoredZero(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir(), "memory.db")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         "fact_time_contract",
		AgentKey:   "agent-a",
		Kind:       KindFact,
		ScopeType:  ScopeAgent,
		ScopeKey:   "agent:agent-a",
		Title:      "time contract",
		Summary:    "raw field contract",
		SourceType: "tool-write",
		Category:   "fact",
		Importance: 5,
		Status:     StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Write(item); err != nil {
		t.Fatalf("write fact: %v", err)
	}
	detail, err := store.ReadConsoleDetail("agent-a", item.ID)
	if err != nil {
		t.Fatalf("read detail: %v", err)
	}
	for _, key := range []string{"lastConfirmedAt", "expiresAt"} {
		if _, exists := detail.RawFields[key]; exists {
			t.Fatalf("expected null optional raw field %s to be omitted: %#v", key, detail.RawFields)
		}
	}
	if _, err := store.db.Exec(`UPDATE MEMORY_FACTS SET EXPIRES_AT_ = 0 WHERE ID_ = ?`, item.ID); err != nil {
		t.Fatalf("inject invalid expiry: %v", err)
	}
	if _, err := store.ReadConsoleDetail("agent-a", item.ID); !timecontract.IsViolation(err) {
		t.Fatalf("expected stored zero expiry violation, got %v", err)
	}
}

func TestSQLiteStoreRejectsInvalidPersistedProjectionTimesWithoutRepair(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir(), "memory.db")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         "memory_projection_time_contract",
		AgentKey:   "agent-a",
		Kind:       KindFact,
		ScopeType:  ScopeAgent,
		ScopeKey:   "agent:agent-a",
		Title:      "projection timestamp",
		Summary:    "must not be repaired on read",
		SourceType: "tool-write",
		Category:   "fact",
		Importance: 5,
		Status:     StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Write(item); err != nil {
		t.Fatalf("write fact: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE MEMORIES SET TS_ = 0, UPDATED_AT_ = 1700000000 WHERE ID_ = ?`, item.ID); err != nil {
		t.Fatalf("inject invalid persisted values: %v", err)
	}
	if _, err := store.ListAll("agent-a"); !timecontract.IsViolation(err) {
		t.Fatalf("expected invalid persisted memory times to fail, got %v", err)
	}
	var createdAt, updatedAt int64
	if err := store.db.QueryRow(`SELECT TS_, UPDATED_AT_ FROM MEMORIES WHERE ID_ = ?`, item.ID).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read stored values: %v", err)
	}
	if createdAt != 0 || updatedAt != 1_700_000_000 {
		t.Fatalf("read path must not repair stored values, got createdAt=%d updatedAt=%d", createdAt, updatedAt)
	}
}
