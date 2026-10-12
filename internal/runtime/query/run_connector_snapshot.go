package query

import (
	"encoding/json"
	"fmt"
	"os"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
)

// runConnectorSnapshot is the connector mounts and tools a Run was admitted
// with. It is held in process memory until the Run ends, so a Run resumed
// after waiting for the user keeps its original package versions. Nothing is
// written to disk: a restart cancels every unfinished Run.
type runConnectorSnapshot struct {
	RunID  string                      `json:"runId"`
	Agents []catalog.ConnectorSnapshot `json:"agents"`
}

func (s *Service) finishRunConnectorPins(runID, chatID string) {
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
	s.connectorSnapshots.Delete(runID)
}

func (s *Service) FreezeRunConnectors(prepared preparedQuery) error {
	snapshot := runConnectorSnapshot{RunID: prepared.Req.RunID}
	snapshot.Agents = append(snapshot.Agents, prepared.AgentDef.FreezeConnectors())
	if prepared.TeamSnapshot != nil {
		for _, key := range prepared.TeamSnapshot.ValidAgentKeys {
			if def, ok := prepared.TeamSnapshot.AgentDefinition(key); ok {
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
	// Bind once: resuming an existing Run must retain its original package versions.
	if previous, held := s.connectorSnapshots.Load(snapshot.RunID); held {
		before, _ := json.Marshal(previous)
		after, _ := json.Marshal(snapshot)
		if string(before) != string(after) {
			return fmt.Errorf("run connector snapshot conflict")
		}
	} else {
		s.connectorSnapshots.Store(snapshot.RunID, snapshot)
	}
	return s.connectorSources().PinRun(snapshot.RunID, mounts)
}

func (s *Service) RestoreRunConnectors(runID string, def *catalog.AgentDefinition) error {
	snapshot, held := s.connectorSnapshots.Load(runID)
	if !held {
		return nil
	}
	for _, agent := range snapshot.Agents {
		if agent.AgentKey == def.Key {
			return def.RestoreConnectors(s.connectorSources(), agent)
		}
	}
	return fmt.Errorf("Agent missing from frozen connector snapshot")
}
