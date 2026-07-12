package chat

import (
	"database/sql"
	"errors"
	"os"
	"sync"

	"agent-platform/internal/stream"
)

var ErrChatNotFound = errors.New("chat not found")
var ErrRunNotFound = errors.New("run not found")
var ErrRunIncomplete = errors.New("run is not complete")
var ErrChatPendingAwaiting = errors.New("chat has pending awaiting")
var ErrLegacyPlanningProtocol = errors.New("legacy planning protocol is unsupported")

type StepLineStore interface {
	AppendQueryLine(chatID string, line QueryLine) error
	AppendStepLine(chatID string, line StepLine) error
	AppendEventLine(chatID string, line EventLine) error
	AppendSubmitLine(chatID string, line SubmitLine) error
}

// RunStartRecorder persists the authoritative registration clock before a
// stream can emit or replay run.start. It is deliberately optional so test
// doubles and non-chat execution stores do not need to implement lifecycle
// persistence; the production FileStore does.
type RunStartRecorder interface {
	OnRunStarted(start RunStart) error
}

// RunStartReader retrieves the immutable lifecycle clock for a persisted run.
// It is deliberately separate from Store so non-persistent test doubles do
// not accidentally claim that a restart can reconstruct a run lifecycle.
type RunStartReader interface {
	LoadRunStartedAt(chatID string, runID string) (int64, error)
}

type Store interface {
	StepLineStore
	EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error)
	EnsureChatWithSource(chatID string, agentKey string, teamID string, firstMessage string, source string) (Summary, bool, error)
	DeriveChat(request DeriveChatRequest) (DeriveChatResult, error)
	RenameChat(chatID string, chatName string) (Summary, error)
	UpdateAgentKey(chatID string, agentKey string) error
	SetSourceChannel(chatID string, sourceChannel string) error
	SourceChannel(chatID string) (string, error)
	Summary(chatID string) (*Summary, error)
	LoadAllPendingAwaitings() ([]PendingAwaitingWithChat, error)
	LoadAwaitingAsk(chatID string, awaitingID string) (*PersistedAwaitingAsk, error)
	LoadAwaitingSubmit(chatID string, awaitingID string, submitID string) (*PersistedAwaitingSubmit, error)
	LoadLatestAwaitingSubmit(chatID string, awaitingID string) (*PersistedAwaitingSubmit, error)
	LoadRunQuery(chatID string, runID string) (*QueryLine, error)
	SetPendingAwaiting(chatID string, pending PendingAwaiting) error
	ClearPendingAwaiting(chatID string, awaitingID string) error
	AppendEvent(chatID string, event stream.EventData) error
	LoadSystemInit(chatID string, key SystemInitKey) (*SystemInitLine, error)
	LoadAllSystemInits(chatID string) (SystemInitIndex, error)
	LoadRawMessages(chatID string, k int) ([]map[string]any, error)
	LoadJSONLContent(chatID string) (string, error)
	OnRunCompleted(completion RunCompletion) error
	ListChats(lastRunID string, agentKey string) ([]Summary, error)
	RecentChatsByAgent(agentKey string, limit int) ([]Summary, error)
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

// TeamHistoryReader builds a member-scoped history view. Root coordinator
// tool chains and other members' tool/reasoning messages are excluded.
type TeamHistoryReader interface {
	LoadTeamMemberRawMessages(chatID string, k int, memberAgentKey string) ([]map[string]any, error)
}

// TeamCoordinatorHistoryReader builds the coordinator's actor-scoped view.
// It keeps root user messages, Team summaries, and final member bodies while
// excluding child prompts, reasoning, tools, and raw intermediate results.
type TeamCoordinatorHistoryReader interface {
	LoadTeamCoordinatorRawMessages(chatID string, k int) ([]map[string]any, error)
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

func (s *FileStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// RunIDAfter and related helpers are in run_id.go
