package kbase

import (
	"agent-platform/internal/operationstate"
	"fmt"
	"path/filepath"
)

func (m *Manager) RefreshOperationStatus(agentKey, refreshID string) (string, error) {
	cfg, _, err := m.resolver.Resolve(agentKey)
	if err != nil {
		return "", err
	}
	var result RefreshResult
	if err = operationstate.Read(filepath.Join(cfg.StorageDir, "refresh-operations"), refreshID, &result); err != nil {
		return "", err
	}
	if result.AgentKey != agentKey || result.RefreshID != refreshID {
		return "", fmt.Errorf("refresh identity mismatch")
	}
	if result.Status == "running" {
		m.refresh.operationMu.Lock()
		_, active := m.refresh.operations[refreshID]
		m.refresh.operationMu.Unlock()
		if !active {
			return "interrupted", nil
		}
	}
	return result.Status, nil
}
