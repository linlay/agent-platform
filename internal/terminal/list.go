package terminal

import (
	"sort"
	"strings"
)

type SessionInfo struct {
	TerminalID  string `json:"terminalId"`
	OwnerKey    string `json:"-"`
	AgentKey    string `json:"agentKey"`
	TerminalKey string `json:"terminalKey"`
	Scope       string `json:"scope"`
	CWD         string `json:"cwd"`
	Shell       string `json:"shell"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt"`
}

func (m *Manager) List(ownerKey string) []SessionInfo {
	if m == nil {
		return nil
	}
	ownerKey = strings.TrimSpace(ownerKey)
	if ownerKey == "" {
		return nil
	}

	m.mu.RLock()
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil || session.Finished() || session.OwnerKey() != ownerKey {
			continue
		}
		startedAt := int64(0)
		if !session.startedAt.IsZero() {
			startedAt = session.startedAt.UnixMilli()
		}
		infos = append(infos, SessionInfo{
			TerminalID:  session.ID(),
			OwnerKey:    session.OwnerKey(),
			AgentKey:    session.AgentKey(),
			TerminalKey: session.TerminalKey(),
			Scope:       session.scope,
			CWD:         session.CWD(),
			Shell:       session.Shell(),
			Status:      session.Status(),
			StartedAt:   startedAt,
		})
	}
	m.mu.RUnlock()

	sort.Slice(infos, func(i, j int) bool {
		if infos[i].AgentKey != infos[j].AgentKey {
			return infos[i].AgentKey < infos[j].AgentKey
		}
		if infos[i].TerminalKey != infos[j].TerminalKey {
			return infos[i].TerminalKey < infos[j].TerminalKey
		}
		return infos[i].TerminalID < infos[j].TerminalID
	})
	return infos
}
