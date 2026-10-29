package contracts

import (
	"strings"
	"sync"
)

type AwaitingResolutionState uint8

const (
	AwaitingResolutionUnowned AwaitingResolutionState = iota
	AwaitingResolutionOwned
	AwaitingResolutionFinished
)

// AwaitingHasLiveExecutor includes the interval after submit removes an awaiting
// and before the executor persists its results.
func AwaitingHasLiveExecutor(runs RunManager, runID string) bool {
	if runs == nil {
		return false
	}
	status, ok := runs.RunStatus(runID)
	return ok && status.CompletedAt == 0
}

// AwaitingResolutionOwner reports who owns an awaiting's terminal writes: a
// live run (Owned), a finished run (Finished), or nobody (Unowned). Callers may
// write only for Unowned.
func AwaitingResolutionOwner(runs RunManager, runID string) AwaitingResolutionState {
	if runs == nil {
		return AwaitingResolutionUnowned
	}
	if status, ok := runs.RunStatus(runID); ok {
		if status.CompletedAt > 0 {
			return AwaitingResolutionFinished
		}
		return AwaitingResolutionOwned
	}
	return AwaitingResolutionUnowned
}

// AwaitingResolutionCoordinator provides per-awaiting leases for continuation
// stores without exposing the query implementation to transport adapters.
type AwaitingResolutionCoordinator struct {
	mu          sync.Mutex
	resolutions map[awaitingResolutionKey]*awaitingResolutionLock
}

type awaitingResolutionKey struct {
	chatID, runID, awaitingID string
}

type awaitingResolutionLock struct {
	mu   sync.Mutex
	refs int
}

// LockResolution serializes persisted reconciliation for one awaiting. It is
// separate from the registry lock: reconciliation may register a terminal item
// and cancel its supervisor. Callers must re-read persisted state after locking.
func (s *AwaitingResolutionCoordinator) LockResolution(chatID, runID, awaitingID string) func() {
	key := awaitingResolutionKey{strings.TrimSpace(chatID), strings.TrimSpace(runID), strings.TrimSpace(awaitingID)}
	s.mu.Lock()
	if s.resolutions == nil {
		s.resolutions = make(map[awaitingResolutionKey]*awaitingResolutionLock)
	}
	lock := s.resolutions[key]
	if lock == nil {
		lock = &awaitingResolutionLock{}
		s.resolutions[key] = lock
	}
	lock.refs++
	s.mu.Unlock()
	lock.mu.Lock()
	return sync.OnceFunc(func() {
		lock.mu.Unlock()
		s.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.resolutions, key)
		}
		s.mu.Unlock()
	})
}
