package types

import "agent-platform/internal/chat"

// DeferredAwaiting is the restart-safe continuation record addressed by the
// public awaiting ID. It is shared across query continuation and its temporary
// server compatibility adapter without importing either implementation.
type DeferredAwaiting struct {
	ChatID           string
	AwaitingID       string
	RunID            string
	Mode             string
	CreatedAt        int64
	Ask              *chat.PersistedAwaitingAsk
	TerminalCode     string
	SupervisorCancel func()
}
