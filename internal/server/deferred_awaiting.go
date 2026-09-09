package server

import runtimetypes "agent-platform/internal/runtime/types"

type DeferredAwaiting = runtimetypes.DeferredAwaiting

type DeferredAwaitingStore interface {
	Register(DeferredAwaiting)
	Lookup(string) (DeferredAwaiting, bool)
	Remove(string)
	LockResolution(chatID, runID, awaitingID string) func()
}
