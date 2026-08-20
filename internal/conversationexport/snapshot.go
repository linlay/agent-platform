package conversationexport

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

const (
	SnapshotVersion  = 1
	MaxSnapshotBytes = 20 * 1024 * 1024
	MaxHTMLBytes     = 20 * 1024 * 1024
	MaxTemplateBytes = 256 * 1024
	MaxItems         = 2_000
	MaxTitleBytes    = 300
	MaxLabelBytes    = 300
)

var (
	ErrNoRootTurn      = errors.New("conversation has no exportable root turn")
	ErrNoCompletedTurn = errors.New("conversation has no completed question and answer")
	ErrTooLarge        = errors.New("conversation export exceeds size limit")
	ErrInvalidTimeline = errors.New("conversation timeline is invalid")
)

type SizeLimitError struct {
	Actual int
	Limit  int
}

func (e *SizeLimitError) Error() string {
	return fmt.Sprintf("conversation export is %d bytes; limit is %d bytes (20 MiB)", e.Actual, e.Limit)
}

func (e *SizeLimitError) Unwrap() error {
	return ErrTooLarge
}

func newSizeLimitError(actual, limit int) error {
	return &SizeLimitError{Actual: actual, Limit: limit}
}

type SnapshotV1 struct {
	Version    int      `json:"version"`
	Title      string   `json:"title"`
	CreatedAt  int64    `json:"createdAt"`
	CapturedAt int64    `json:"capturedAt"`
	Turns      []TurnV1 `json:"turns"`
}

type TurnV1 struct {
	StartedAt int64    `json:"startedAt"`
	EndedAt   *int64   `json:"endedAt,omitempty"`
	Outcome   Outcome  `json:"outcome"`
	Items     []ItemV1 `json:"items"`
}

type Outcome string

const (
	OutcomeRunning   Outcome = "running"
	OutcomeCompleted Outcome = "completed"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeFailed    Outcome = "failed"
)

type ItemV1 struct {
	Kind  ItemKind `json:"kind"`
	Text  string   `json:"text"`
	Label string   `json:"label,omitempty"`
	At    int64    `json:"at"`
}

type ItemKind string

const (
	ItemUser      ItemKind = "user"
	ItemReasoning ItemKind = "reasoning"
	ItemAssistant ItemKind = "assistant"
)

type projectedTurn struct {
	turn         TurnV1
	runID        string
	lowerBoundAt int64
	runStarted   bool
	itemsStarted bool
	terminalSeen bool
}

func BuildSnapshot(summary *chat.Summary, events []stream.EventData, capturedAt int64) (SnapshotV1, error) {
	if summary == nil {
		return SnapshotV1{}, ErrInvalidTimeline
	}
	if err := timecontract.ValidateEpochMillis(summary.CreatedAt, "createdAt", "conversation.snapshot.createdAt"); err != nil {
		return SnapshotV1{}, err
	}
	if err := timecontract.ValidateEpochMillis(capturedAt, "capturedAt", "conversation.snapshot.capturedAt"); err != nil {
		return SnapshotV1{}, err
	}
	if capturedAt < summary.CreatedAt {
		return SnapshotV1{}, ErrInvalidTimeline
	}
	title := strings.TrimSpace(summary.ChatName)
	if title == "" {
		title = "Chat"
	}
	if len(title) > MaxTitleBytes {
		return SnapshotV1{}, ErrTooLarge
	}

	projectedTextBytes := len(title)
	turns := make([]projectedTurn, 0)
	turnByRunID := make(map[string]int)
	pendingTurn := -1
	for _, event := range events {
		if err := timecontract.ValidateEpochMillis(event.Timestamp, "timestamp", "conversation.snapshot.events"); err != nil {
			return SnapshotV1{}, err
		}
		switch event.Type {
		case "request.query":
			if strings.TrimSpace(event.String("taskId")) != "" || !api.QueryRoleVisible(event.String("role")) {
				pendingTurn = -1
				continue
			}
			message := event.String("message")
			if strings.TrimSpace(message) == "" {
				pendingTurn = -1
				continue
			}
			projectedTextBytes += len(message)
			if projectedTextBytes > MaxSnapshotBytes {
				return SnapshotV1{}, newSizeLimitError(projectedTextBytes, MaxSnapshotBytes)
			}
			runID := strings.TrimSpace(event.String("runId"))
			if runID != "" {
				if _, exists := turnByRunID[runID]; exists {
					return SnapshotV1{}, ErrInvalidTimeline
				}
			}
			index := len(turns)
			turns = append(turns, projectedTurn{
				turn: TurnV1{
					StartedAt: event.Timestamp,
					Outcome:   OutcomeRunning,
					Items: []ItemV1{{
						Kind: ItemUser,
						Text: message,
						At:   event.Timestamp,
					}},
				},
				runID:        runID,
				lowerBoundAt: event.Timestamp,
			})
			if runID == "" {
				pendingTurn = index
			} else {
				turnByRunID[runID] = index
				pendingTurn = -1
			}
		case "run.start":
			runID := strings.TrimSpace(event.String("runId"))
			if runID == "" {
				continue
			}
			if _, exists := turnByRunID[runID]; exists {
				continue
			}
			if pendingTurn < 0 {
				continue
			}
			turns[pendingTurn].runID = runID
			turnByRunID[runID] = pendingTurn
			pendingTurn = -1
		}
	}

	for _, event := range events {
		runID := strings.TrimSpace(event.String("runId"))
		index, exists := turnByRunID[runID]
		if !exists {
			continue
		}
		turn := &turns[index]
		if strings.TrimSpace(event.String("taskId")) != "" {
			continue
		}
		switch event.Type {
		case "run.start":
			if turn.runStarted || turn.itemsStarted || turn.terminalSeen {
				return SnapshotV1{}, ErrInvalidTimeline
			}
			turn.runStarted = true
			turn.turn.StartedAt = event.Timestamp
			if event.Timestamp > turn.lowerBoundAt {
				turn.lowerBoundAt = event.Timestamp
			}
		case "reasoning.snapshot", "content.snapshot":
			if turn.terminalSeen || event.Timestamp < turn.lowerBoundAt {
				return SnapshotV1{}, ErrInvalidTimeline
			}
			text := event.String("text")
			if strings.TrimSpace(text) == "" {
				continue
			}
			if len(turn.turn.Items) >= MaxItems {
				return SnapshotV1{}, ErrTooLarge
			}
			item := ItemV1{Text: text, At: event.Timestamp}
			if event.Type == "reasoning.snapshot" {
				item.Kind = ItemReasoning
				item.Label = strings.TrimSpace(event.String("reasoningLabel"))
				if len(item.Label) > MaxLabelBytes {
					return SnapshotV1{}, ErrTooLarge
				}
			} else {
				item.Kind = ItemAssistant
			}
			projectedTextBytes += len(item.Text) + len(item.Label)
			if projectedTextBytes > MaxSnapshotBytes {
				return SnapshotV1{}, newSizeLimitError(projectedTextBytes, MaxSnapshotBytes)
			}
			turn.turn.Items = append(turn.turn.Items, item)
			turn.itemsStarted = true
			turn.lowerBoundAt = event.Timestamp
		case "run.complete", "run.cancel", "run.error":
			if turn.terminalSeen || event.Timestamp < turn.lowerBoundAt {
				return SnapshotV1{}, ErrInvalidTimeline
			}
			endedAt := event.Timestamp
			turn.turn.EndedAt = &endedAt
			switch event.Type {
			case "run.complete":
				turn.turn.Outcome = OutcomeCompleted
			case "run.cancel":
				turn.turn.Outcome = OutcomeCancelled
			case "run.error":
				turn.turn.Outcome = OutcomeFailed
			}
			turn.terminalSeen = true
			turn.lowerBoundAt = event.Timestamp
		}
	}

	snapshot := SnapshotV1{
		Version:    SnapshotVersion,
		Title:      title,
		CreatedAt:  summary.CreatedAt,
		CapturedAt: capturedAt,
		Turns:      make([]TurnV1, 0, len(turns)),
	}
	itemCount := 0
	for _, turn := range turns {
		if turn.runID == "" {
			continue
		}
		itemCount += len(turn.turn.Items)
		if itemCount > MaxItems {
			return SnapshotV1{}, ErrTooLarge
		}
		snapshot.Turns = append(snapshot.Turns, turn.turn)
	}
	if len(snapshot.Turns) == 0 {
		return SnapshotV1{}, ErrNoRootTurn
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return SnapshotV1{}, err
	}
	if len(encoded) > MaxSnapshotBytes {
		return SnapshotV1{}, newSizeLimitError(len(encoded), MaxSnapshotBytes)
	}
	return snapshot, nil
}
