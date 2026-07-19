package memory

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDecodeHistoryJSONPreservesEpochIntegerTokens(t *testing.T) {
	decoded := decodeHistoryJSON(`{"createdAt":1700000000000}`)
	if _, ok := decoded["createdAt"].(json.Number); !ok {
		t.Fatalf("createdAt lost JSON integer token: %#v", decoded)
	}
}

func TestHistoryWriteLeavesOpaqueMetadataUntouched(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir(), "memory.db")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	now := time.Now().UnixMilli()
	store.mu.Lock()
	err = store.recordHistoryLocked(HistoryEvent{
		Timestamp: now,
		Operation: "test",
		Meta: map[string]any{
			"createdAt": float64(now),
		},
	})
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("opaque metadata must not be interpreted as a platform time field: %v", err)
	}
}
