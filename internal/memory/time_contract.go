package memory

import (
	"fmt"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/timecontract"
)

// validateStoredMemoryTimeContract is deliberately called while reading a
// persisted record, before any read-side bookkeeping can modify it. Historic
// rows are data at a public boundary: they must not be repaired by copying one
// timestamp into another or by substituting the current clock.
func validateStoredMemoryTimeContract(item api.StoredMemoryResponse, location string) error {
	location = memoryTimeLocation(location, item.ID)
	if err := timecontract.ValidateEpochMillis(item.CreatedAt, "createdAt", location+".createdAt"); err != nil {
		return err
	}
	if err := timecontract.ValidateEpochMillis(item.UpdatedAt, "updatedAt", location+".updatedAt"); err != nil {
		return err
	}
	if item.LastAccessedAt != nil {
		if err := timecontract.ValidateEpochMillis(*item.LastAccessedAt, "lastAccessedAt", location+".lastAccessedAt"); err != nil {
			return err
		}
	}
	return nil
}

func validateToolRecordTimeContract(record ToolRecord, location string) error {
	return validateStoredMemoryTimeContract(api.StoredMemoryResponse{
		ID:             record.ID,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
		LastAccessedAt: record.LastAccessedAt,
	}, location)
}

func validateHistoryTimeContract(event HistoryEvent, location string) error {
	location = strings.TrimSpace(location)
	if location == "" {
		location = "memory.history"
	}
	if strings.TrimSpace(event.ID) != "" {
		location = fmt.Sprintf("%s[%s]", location, event.ID)
	}
	return timecontract.ValidateEpochMillis(event.Timestamp, "ts", location+".ts")
}

func memoryTimeLocation(base string, id string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "memory.records"
	}
	if strings.TrimSpace(id) == "" {
		return base
	}
	return fmt.Sprintf("%s[%s]", base, strings.TrimSpace(id))
}
