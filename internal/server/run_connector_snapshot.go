package server

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type runConnectorSnapshot struct {
	RunID  string                      `json:"runId"`
	Agents []catalog.ConnectorSnapshot `json:"agents"`
}

func (s *Server) connectorSnapshotPath(runID string) string {
	return filepath.Join(s.deps.Config.Paths.EffectiveStateDir(), "run-connectors", filepath.Base(s.connectorSources().RunPinPath(runID)))
}
func (s *Server) freezeRunConnectors(prepared preparedQuery) error {
	snapshot := runConnectorSnapshot{RunID: prepared.req.RunID}
	snapshot.Agents = append(snapshot.Agents, prepared.agentDef.FreezeConnectors())
	if prepared.teamSnapshot != nil {
		for _, key := range prepared.teamSnapshot.ValidAgentKeys {
			if def, ok := prepared.teamSnapshot.AgentDefinition(key); ok {
				snapshot.Agents = append(snapshot.Agents, def.FreezeConnectors())
			}
		}
	}
	var mounts []connector.AgentRuntime
	for _, agent := range snapshot.Agents {
		for _, mount := range agent.Mounts {
			mounts = append(mounts, connector.AgentRuntime{AgentKey: agent.AgentKey, ID: mount.ID, Dir: mount.Dir, Digest: mount.Digest})
		}
	}
	if len(mounts) == 0 {
		return nil
	}
	path := s.connectorSnapshotPath(snapshot.RunID)
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	// Bind once: resuming an existing Run must retain its original package versions.
	if previous, err := os.ReadFile(path); err == nil {
		if string(previous) != string(data) {
			return fmt.Errorf("run connector snapshot conflict")
		}
		return s.connectorSources().PinRun(snapshot.RunID, mounts)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return err
	}
	return s.connectorSources().PinRun(snapshot.RunID, mounts)
}
func (s *Server) restoreRunConnectors(runID string, def *catalog.AgentDefinition) error {
	var snapshot runConnectorSnapshot
	err := connector.ReadJSON(s.connectorSnapshotPath(runID), &snapshot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if snapshot.RunID != runID {
		return fmt.Errorf("run connector snapshot identity mismatch")
	}
	for _, agent := range snapshot.Agents {
		if agent.AgentKey == def.Key {
			return def.RestoreConnectors(s.connectorSources(), agent)
		}
	}
	return fmt.Errorf("Agent missing from frozen connector snapshot")
}
func (s *Server) finishRunConnectorPins(runID, chatID string) {
	if s.deps.Chats != nil {
		summary, err := s.deps.Chats.Summary(chatID)
		if err != nil {
			return
		}
		if summary != nil && summary.PendingAwaiting != nil && summary.PendingAwaiting.RunID == runID {
			return
		}
	}
	_ = os.Remove(s.connectorSources().RunPinPath(runID))
	// Retain the small immutable snapshot to reject conflicting Run ID reuse.
}
