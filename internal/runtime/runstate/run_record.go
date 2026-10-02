package runstate

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/operationstate"
	"fmt"
	"log"
	"path/filepath"
	"time"
)

type persistedRunRecord struct {
	Snapshot contracts.RunSnapshot `json:"snapshot"`
	Origin   *contracts.RunOrigin  `json:"origin,omitempty"`
}

func (m *Manager) WithStateRoot(root string) *Manager {
	if root != "" {
		m.recordRoot = filepath.Join(root, "run-status")
	}
	return m
}
func (m *Manager) saveRunRecord(state *managedRun) {
	if m.recordRoot == "" || state == nil {
		return
	}
	status := runStatusInfoFromManagedRun(state)
	snapshot := contracts.RunSnapshot{RunID: status.RunID, ChatID: status.ChatID, AgentKey: status.AgentKey, TeamID: status.TeamID, Status: PublicStatus(status.State), AccessLevel: status.AccessLevel, StartedAt: status.StartedAt, CompletedAt: status.CompletedAt, Origin: cloneRunOrigin(state.runOrigin)}
	if state.eventBus != nil {
		ApplyEventSnapshot(&snapshot, state.eventBus.Snapshot())
	}
	snapshot.Content = ""
	if err := operationstate.Write(m.recordRoot, status.RunID, persistedRunRecord{Snapshot: snapshot, Origin: snapshot.Origin}); err != nil {
		log.Printf("[run] persist status runId=%s: %v", status.RunID, err)
	}
}
func (m *Manager) StoredRunSnapshot(runID string) (contracts.RunSnapshot, error) {
	if m.recordRoot == "" {
		return contracts.RunSnapshot{}, fmt.Errorf("run record storage unavailable")
	}
	var record persistedRunRecord
	if err := operationstate.Read(m.recordRoot, runID, &record); err != nil {
		return record.Snapshot, err
	}
	record.Snapshot.Origin = record.Origin
	if record.Snapshot.Status == "running" || record.Snapshot.Status == "awaiting" {
		record.Snapshot.Status = "interrupted"
		record.Snapshot.CompletedAt = time.Now().UnixMilli()
	}
	return record.Snapshot, nil
}
