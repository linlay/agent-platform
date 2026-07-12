package terminal

import (
	"sort"
	"strings"

	"agent-platform/internal/timecontract"
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
	infos, _ := m.list(ownerKey, false)
	return infos
}

// ListStrict is the public-stream boundary. Terminal status has a required
// startedAt instant, so a malformed in-memory session must fail rather than
// become a zero/1970 timestamp on the wire.
func (m *Manager) ListStrict(ownerKey string) ([]SessionInfo, error) {
	return m.list(ownerKey, true)
}

func (m *Manager) list(ownerKey string, strict bool) ([]SessionInfo, error) {
	if m == nil {
		return nil, nil
	}
	ownerKey = strings.TrimSpace(ownerKey)
	if ownerKey == "" {
		return nil, nil
	}

	m.mu.RLock()
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil || session.Finished() || session.OwnerKey() != ownerKey {
			continue
		}
		if session.startedAt.IsZero() {
			if strict {
				m.mu.RUnlock()
				return nil, &timecontract.Violation{Field: "startedAt", Location: "terminal.sessions." + session.ID(), Reason: "is required"}
			}
			continue
		}
		startedAt := session.startedAt.UnixMilli()
		if err := timecontract.ValidateEpochMillis(startedAt, "startedAt", "terminal.sessions."+session.ID()+".startedAt"); err != nil {
			if strict {
				m.mu.RUnlock()
				return nil, err
			}
			continue
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
	return infos, nil
}
