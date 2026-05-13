package chat

import (
	"database/sql"
	"errors"
	"os"
	"sync"

	"agent-platform-runner-go/internal/stream"
)

var ErrChatNotFound = errors.New("chat not found")
var ErrRunNotFound = errors.New("run not found")

type Store interface {
	EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error)
	UpdateAgentKey(chatID string, agentKey string) error
	SetSourceChannel(chatID string, sourceChannel string) error
	SourceChannel(chatID string) (string, error)
	Summary(chatID string) (*Summary, error)
	LoadAllPendingAwaitings() ([]PendingAwaitingWithChat, error)
	LoadAwaitingAsk(chatID string, awaitingID string) (*PersistedAwaitingAsk, error)
	SetPendingAwaiting(chatID string, pending PendingAwaiting) error
	ClearPendingAwaiting(chatID string, awaitingID string) error
	AppendEvent(chatID string, event stream.EventData) error
	AppendQueryLine(chatID string, line QueryLine) error
	AppendStepLine(chatID string, line StepLine) error
	AppendEventLine(chatID string, line EventLine) error
	AppendSubmitLine(chatID string, line SubmitLine) error
	LoadSystemInit(chatID string, cacheKey string) (*SystemInitLine, error)
	LoadAllSystemInits(chatID string) (map[string]*SystemInitLine, error)
	// AppendSystemInitLine is retained for legacy imports/backfills. Main write paths store system snapshots inline on QueryLine.Systems.
	AppendSystemInitLine(chatID string, line SystemInitLine) error
	LoadRawMessages(chatID string, k int) ([]map[string]any, error)
	OnRunCompleted(completion RunCompletion) error
	ListChats(lastRunID string, agentKey string) ([]Summary, error)
	ListRuns(chatID string) ([]RunSummary, error)
	LoadChat(chatID string) (Detail, error)
	LoadRunTrace(chatID string, runID string) (RunTrace, error)
	SearchSession(chatID string, query string, limit int) ([]SearchHit, error)
	SearchGlobal(query string, agentKey string, teamID string, limit int) ([]GlobalSearchHit, error)
	MarkRead(chatID string, runID string) (Summary, error)
	MarkAllRead(agentKey string) (int, error)
	SetFeedback(chatID, runID, feedbackType, comment string) (int64, error)
	DeleteChat(chatID string) error
	AgentChatStats() (map[string]AgentChatStats, error)
	ResolveResource(file string) (string, error)
	ChatDir(chatID string) string
}

type FileStore struct {
	root string
	mu   sync.Mutex
	db   *sql.DB
}

func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	store := &FileStore{root: root}
	if err := store.initDB(); err != nil {
		return nil, err
	}
	return store, nil
}

// RunIDAfter and related helpers are in run_id.go
