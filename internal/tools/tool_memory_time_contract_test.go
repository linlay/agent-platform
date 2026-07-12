package tools

import (
	"testing"

	"agent-platform/internal/memory"
	"agent-platform/internal/timecontract"
)

func TestMemoryToolRecordValueOmitsAbsentLastAccessedAt(t *testing.T) {
	value := memoryToolRecordValue(memory.ToolRecord{
		ID:        "mem-1",
		CreatedAt: timecontract.MinEpochMillis,
		UpdatedAt: timecontract.MinEpochMillis,
	})
	if _, exists := value["lastAccessedAt"]; exists {
		t.Fatalf("absent lastAccessedAt must be omitted, got %#v", value)
	}

	lastAccessedAt := timecontract.MinEpochMillis + 1
	value = memoryToolRecordValue(memory.ToolRecord{
		ID:             "mem-2",
		CreatedAt:      timecontract.MinEpochMillis,
		UpdatedAt:      timecontract.MinEpochMillis,
		LastAccessedAt: &lastAccessedAt,
	})
	if got, ok := value["lastAccessedAt"].(int64); !ok || got != lastAccessedAt {
		t.Fatalf("valid lastAccessedAt = %#v, want %d", value["lastAccessedAt"], lastAccessedAt)
	}
}
