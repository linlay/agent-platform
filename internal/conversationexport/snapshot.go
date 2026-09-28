package conversationexport

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

const (
	SnapshotVersion  = 1
	MaxSnapshotBytes = 20 * 1024 * 1024
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

func (e *SizeLimitError) Unwrap() error { return ErrTooLarge }

func newSizeLimitError(actual, limit int) error {
	return &SizeLimitError{Actual: actual, Limit: limit}
}

type AssistantV1 struct {
	Name     string `json:"name"`
	IconName string `json:"iconName,omitempty"`
}

type ResolveAssistant func(agentKey, teamID string) *AssistantV1

type Outcome string

const (
	OutcomeRunning   Outcome = "running"
	OutcomeCompleted Outcome = "completed"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeFailed    Outcome = "failed"
)

// SnapshotV1 contains only the facts needed to reconstruct a frozen timeline.
// It deliberately excludes live tool output and arbitrary protocol payloads.
type SnapshotV1 struct {
	Version     int            `json:"version"`
	Title       string         `json:"title"`
	Locale      string         `json:"locale"`
	CreatedAt   int64          `json:"createdAt"`
	CapturedAt  int64          `json:"capturedAt"`
	Turns       []TurnV1       `json:"turns"`
	Attachments []AttachmentV1 `json:"attachments"`
}

type SnapshotDocument struct {
	Snapshot SnapshotV1
	JSON     []byte
}

type TurnV1 struct {
	RunID     string       `json:"runId"`
	QueryAt   int64        `json:"queryAt"`
	StartedAt int64        `json:"startedAt"`
	EndedAt   *int64       `json:"endedAt,omitempty"`
	Outcome   Outcome      `json:"outcome"`
	Assistant *AssistantV1 `json:"assistant,omitempty"`
	Nodes     []NodeV1     `json:"nodes"`
	Tasks     []TaskV1     `json:"tasks,omitempty"`
}

type NodeV1 struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Role            string     `json:"role,omitempty"`
	Text            string     `json:"text,omitempty"`
	At              int64      `json:"at"`
	RunID           string     `json:"runId"`
	TaskID          string     `json:"taskId,omitempty"`
	ReasoningLabel  string     `json:"reasoningLabel,omitempty"`
	ContentID       string     `json:"contentId,omitempty"`
	ToolID          string     `json:"toolId,omitempty"`
	ToolName        string     `json:"toolName,omitempty"`
	ToolLabel       string     `json:"toolLabel,omitempty"`
	Description     string     `json:"description,omitempty"`
	ArgsText        string     `json:"argsText,omitempty"`
	ResultText      string     `json:"resultText,omitempty"`
	ResultIsCode    bool       `json:"resultIsCode,omitempty"`
	Status          string     `json:"status,omitempty"`
	StartedAt       int64      `json:"startedAt,omitempty"`
	EndedAt         int64      `json:"endedAt,omitempty"`
	DurationMs      int64      `json:"durationMs,omitempty"`
	SourcePublishID string     `json:"sourcePublishId,omitempty"`
	SourceQuery     string     `json:"sourceQuery,omitempty"`
	SourceCount     int        `json:"sourceCount,omitempty"`
	ChunkCount      int        `json:"chunkCount,omitempty"`
	Sources         []SourceV1 `json:"sources,omitempty"`
}

type SourceV1 struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	ChunkIndexes []int           `json:"chunkIndexes"`
	MinIndex     int             `json:"minIndex"`
	Chunks       []SourceChunkV1 `json:"chunks"`
}

type SourceChunkV1 struct {
	ChunkID string `json:"chunkId"`
	Index   int    `json:"index"`
	Content string `json:"content"`
	Path    string `json:"path,omitempty"`
}

type TaskV1 struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SubAgentKey      string `json:"subAgentKey,omitempty"`
	SubAgentName     string `json:"subAgentName,omitempty"`
	SubAgentIconName string `json:"subAgentIconName,omitempty"`
	Status           string `json:"status"`
	DurationMs       int64  `json:"durationMs,omitempty"`
	Error            string `json:"error,omitempty"`
}

type AttachmentV1 struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MIMEType  string `json:"mimeType"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	SourceRef string `json:"sourceRef"`
}

func sourceV1FromEvent(event stream.EventData) ([]SourceV1, int) {
	rawItems := objectSlice(event.Value("sources"))
	sources := make([]SourceV1, 0, len(rawItems))
	chunkCount := 0
	for sourceIndex, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawChunks := objectSlice(item["chunks"])
		if len(rawChunks) == 0 {
			continue
		}
		title, _ := item["title"].(string)
		name, _ := item["name"].(string)
		if name == "" {
			name = path.Base(title)
		}
		if name == "" {
			name = fmt.Sprintf("source_%d", sourceIndex+1)
		}
		id, _ := item["id"].(string)
		if id == "" {
			id = title
		}
		if id == "" {
			id = name
		}
		source := SourceV1{ID: id, Name: name, Title: title, ChunkIndexes: []int{}, Chunks: []SourceChunkV1{}}
		for chunkIndex, chunkRaw := range rawChunks {
			chunk, ok := chunkRaw.(map[string]any)
			if !ok {
				continue
			}
			chunkID, _ := chunk["chunkId"].(string)
			content, _ := chunk["content"].(string)
			chunkPath, _ := chunk["path"].(string)
			if chunkID == "" && content == "" && chunkPath == "" {
				continue
			}
			if chunkID == "" {
				chunkID = fmt.Sprintf("%s_%d", id, chunkIndex+1)
			}
			index := chunkIndex + 1
			if number, ok := chunk["index"].(float64); ok && number > 0 {
				index = int(number)
			}
			source.Chunks = append(source.Chunks, SourceChunkV1{ChunkID: chunkID, Index: index, Content: content, Path: chunkPath})
			source.ChunkIndexes = append(source.ChunkIndexes, index)
		}
		if len(source.Chunks) == 0 {
			continue
		}
		source.MinIndex = source.ChunkIndexes[0]
		chunkCount += len(source.Chunks)
		sources = append(sources, source)
	}
	return sources, chunkCount
}

func objectSlice(value any) []any {
	switch items := value.(type) {
	case []any:
		return items
	case []map[string]any:
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func toolResultFailed(value any) bool {
	if text, ok := value.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) != nil {
			return false
		}
		value = decoded
	}
	record, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"exitCode", "exit_code"} {
		switch number := record[key].(type) {
		case float64:
			return number != 0
		case int:
			return number != 0
		}
	}
	for _, key := range []string{"result", "data"} {
		if nested := record[key]; nested != nil {
			if toolResultFailed(nested) {
				return true
			}
		}
	}
	return false
}

func BuildSnapshotDocument(summary *chat.Summary, events []stream.EventData, attachments []AttachmentV1, capturedAt int64, locale string, resolveAssistant ResolveAssistant) (SnapshotDocument, error) {
	if summary == nil {
		return SnapshotDocument{}, ErrInvalidTimeline
	}
	if err := timecontract.ValidateEpochMillis(summary.CreatedAt, "createdAt", "conversation.snapshot.createdAt"); err != nil {
		return SnapshotDocument{}, err
	}
	if err := timecontract.ValidateEpochMillis(capturedAt, "capturedAt", "conversation.snapshot.capturedAt"); err != nil {
		return SnapshotDocument{}, err
	}
	if capturedAt < summary.CreatedAt {
		return SnapshotDocument{}, ErrInvalidTimeline
	}
	title := strings.TrimSpace(summary.ChatName)
	if title == "" {
		title = "Chat"
	}
	if len(title) > MaxTitleBytes {
		return SnapshotDocument{}, ErrTooLarge
	}
	if locale != "en-US" {
		locale = "zh-CN"
	}
	snapshot := SnapshotV1{Version: SnapshotVersion, Title: title, Locale: locale, CreatedAt: summary.CreatedAt, CapturedAt: capturedAt, Turns: []TurnV1{}, Attachments: append([]AttachmentV1{}, attachments...)}
	turnByRun := map[string]int{}
	toolPosition := map[string]struct{ turn, node int }{}
	toolByID := map[string]struct{ turn, node int }{}
	nodePosition := map[string]struct{ turn, node int }{}
	taskStartedAt := map[string]int64{}
	pendingTurn := -1
	for ordinal, event := range events {
		if err := timecontract.ValidateEpochMillis(event.Timestamp, "timestamp", "conversation.snapshot.events"); err != nil {
			return SnapshotDocument{}, err
		}
		if event.Type == "request.query" && event.String("taskId") == "" && api.QueryRoleVisible(event.String("role")) {
			text := event.String("message")
			if strings.TrimSpace(text) == "" {
				pendingTurn = -1
				continue
			}
			runID := strings.TrimSpace(event.String("runId"))
			if runID != "" {
				if _, exists := turnByRun[runID]; exists {
					return SnapshotDocument{}, ErrInvalidTimeline
				}
			}
			turn := TurnV1{RunID: runID, QueryAt: event.Timestamp, StartedAt: event.Timestamp, Outcome: OutcomeRunning,
				Nodes: []NodeV1{{ID: fmt.Sprintf("query_%d_%d", event.Seq, ordinal), Kind: "message", Role: "user", Text: text, At: event.Timestamp, RunID: runID}}}
			snapshot.Turns = append(snapshot.Turns, turn)
			index := len(snapshot.Turns) - 1
			if runID == "" {
				pendingTurn = index
			} else {
				turnByRun[runID] = index
				pendingTurn = -1
			}
			continue
		}
		if event.Type == "run.start" {
			runID := strings.TrimSpace(event.String("runId"))
			if runID != "" && pendingTurn >= 0 {
				snapshot.Turns[pendingTurn].RunID = runID
				snapshot.Turns[pendingTurn].Nodes[0].RunID = runID
				turnByRun[runID] = pendingTurn
				pendingTurn = -1
			}
		}
		runID := strings.TrimSpace(event.String("runId"))
		if runID == "" && event.Type == "tool.result" {
			if position, ok := toolByID[event.String("toolId")]; ok {
				runID = snapshot.Turns[position.turn].RunID
			}
		}
		turnIndex, found := turnByRun[runID]
		if !found {
			continue
		}
		turn := &snapshot.Turns[turnIndex]
		taskID := strings.TrimSpace(event.String("taskId"))
		if event.Type == "run.start" {
			turn.StartedAt = event.Timestamp
			if resolveAssistant != nil {
				turn.Assistant = resolveAssistant(event.String("agentKey"), event.String("teamId"))
			}
			continue
		}
		if event.Type == "run.complete" || event.Type == "run.error" || event.Type == "run.cancel" {
			ended := event.Timestamp
			turn.EndedAt = &ended
			switch event.Type {
			case "run.complete":
				turn.Outcome = OutcomeCompleted
			case "run.error":
				turn.Outcome = OutcomeFailed
			default:
				turn.Outcome = OutcomeCancelled
			}
			continue
		}
		if turn.EndedAt != nil {
			continue
		}
		id := fmt.Sprintf("node_%d_%d", event.Seq, ordinal)
		switch event.Type {
		case "reasoning.snapshot", "content.snapshot", "planning.snapshot":
			text := event.String("text")
			if strings.TrimSpace(text) == "" {
				continue
			}
			node := NodeV1{ID: id, Kind: "content", Role: "assistant", Text: text, At: event.Timestamp, RunID: runID, TaskID: taskID}
			if event.Type == "reasoning.snapshot" {
				node.Kind = "thinking"
				node.ReasoningLabel = event.String("reasoningLabel")
			}
			if event.Type == "planning.snapshot" {
				node.Kind = "planning"
			}
			if event.Type == "content.snapshot" {
				node.ContentID = event.String("contentId")
			}
			identity := node.ContentID
			if node.Kind == "thinking" {
				identity = event.String("reasoningId")
			}
			if node.Kind == "planning" {
				identity = event.String("planningId")
			}
			if identity != "" {
				key := runID + "\x00" + node.Kind + "\x00" + identity
				if position, exists := nodePosition[key]; exists {
					snapshot.Turns[position.turn].Nodes[position.node].Text = text
					continue
				}
				nodePosition[key] = struct{ turn, node int }{turnIndex, len(turn.Nodes)}
			}
			turn.Nodes = append(turn.Nodes, node)
		case "tool.snapshot", "tool.start":
			toolID := event.String("toolId")
			if toolID == "" {
				continue
			}
			if position, exists := toolPosition[runID+"\x00"+toolID]; exists {
				node := &snapshot.Turns[position.turn].Nodes[position.node]
				if name := event.String("toolName"); name != "" {
					node.ToolName = name
				}
				if label := event.String("toolLabel"); label != "" {
					node.ToolLabel = label
				}
				if description := event.String("description"); description != "" {
					node.Description = description
				}
				if args := event.String("arguments"); args != "" {
					node.ArgsText = args
				}
				continue
			}
			node := NodeV1{ID: id, Kind: "tool", Role: "assistant", At: event.Timestamp, RunID: runID, TaskID: taskID,
				ToolID: toolID, ToolName: event.String("toolName"), ToolLabel: event.String("toolLabel"), Description: event.String("description"), ArgsText: event.String("arguments"), Status: "running", StartedAt: event.Timestamp}
			turn.Nodes = append(turn.Nodes, node)
			toolPosition[runID+"\x00"+toolID] = struct{ turn, node int }{turnIndex, len(turn.Nodes) - 1}
			toolByID[toolID] = struct{ turn, node int }{turnIndex, len(turn.Nodes) - 1}
		case "tool.args":
			if position, exists := toolPosition[runID+"\x00"+event.String("toolId")]; exists {
				snapshot.Turns[position.turn].Nodes[position.node].ArgsText += event.String("delta")
			}
		case "tool.end":
			if position, exists := toolPosition[runID+"\x00"+event.String("toolId")]; exists {
				if args := event.String("arguments"); args != "" {
					snapshot.Turns[position.turn].Nodes[position.node].ArgsText = args
				}
			}
		case "tool.result":
			toolID := event.String("toolId")
			position, ok := toolPosition[runID+"\x00"+toolID]
			if !ok {
				if toolID == "" {
					continue
				}
				turn.Nodes = append(turn.Nodes, NodeV1{ID: id, Kind: "tool", Role: "assistant", At: event.Timestamp,
					RunID: runID, TaskID: taskID, ToolID: toolID, ToolName: event.String("toolName"),
					ToolLabel: event.String("toolLabel"), ArgsText: event.String("arguments")})
				position = struct{ turn, node int }{turnIndex, len(turn.Nodes) - 1}
				toolPosition[runID+"\x00"+toolID] = position
				toolByID[toolID] = position
			}
			node := &snapshot.Turns[position.turn].Nodes[position.node]
			if args := event.String("arguments"); args != "" {
				node.ArgsText = args
			}
			node.EndedAt = event.Timestamp
			if node.StartedAt > 0 {
				node.DurationMs = max(0, event.Timestamp-node.StartedAt)
			}
			value := event.Value("result")
			if value == nil {
				value = event.Value("output")
			}
			if value == nil {
				value = event.Value("text")
			}
			node.Status = "success"
			if event.String("error") != "" || toolResultFailed(value) {
				node.Status = "failed"
			}
			if text, ok := value.(string); ok {
				node.ResultText = text
			} else if value != nil {
				encoded, err := json.MarshalIndent(value, "", "  ")
				if err == nil {
					node.ResultText = string(encoded)
					node.ResultIsCode = true
				}
			}
		case "task.start":
			if taskID == "" {
				continue
			}
			task := TaskV1{ID: taskID, Name: event.String("taskName"), SubAgentKey: event.String("subAgentKey"), Status: "running"}
			if resolveAssistant != nil && task.SubAgentKey != "" {
				if agent := resolveAssistant(task.SubAgentKey, ""); agent != nil {
					task.SubAgentName, task.SubAgentIconName = agent.Name, agent.IconName
				}
			}
			turn.Tasks = append(turn.Tasks, task)
			taskStartedAt[runID+"\x00"+taskID] = event.Timestamp
		case "task.complete", "task.cancel", "task.error":
			for index := range turn.Tasks {
				if turn.Tasks[index].ID == taskID {
					switch event.Type {
					case "task.complete":
						turn.Tasks[index].Status = "completed"
					case "task.cancel":
						turn.Tasks[index].Status = "canceled"
					default:
						turn.Tasks[index].Status = "failed"
					}
					turn.Tasks[index].Error = event.String("error")
					if started := taskStartedAt[runID+"\x00"+taskID]; started > 0 {
						turn.Tasks[index].DurationMs = max(0, event.Timestamp-started)
					}
					break
				}
			}
		case "source.publish":
			sources, chunks := sourceV1FromEvent(event)
			if len(sources) == 0 {
				continue
			}
			turn.Nodes = append(turn.Nodes, NodeV1{ID: id, Kind: "source", Role: "assistant", At: event.Timestamp, RunID: runID, TaskID: taskID,
				SourcePublishID: event.String("publishId"), SourceQuery: event.String("query"),
				SourceCount: len(sources), ChunkCount: chunks, Sources: sources})
		}
		if len(turn.Nodes) > MaxItems {
			return SnapshotDocument{}, ErrTooLarge
		}
	}
	filtered := make([]TurnV1, 0, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		if turn.RunID == "" {
			continue
		}
		if turn.Assistant == nil && resolveAssistant != nil {
			turn.Assistant = resolveAssistant(summary.AgentKey, summary.TeamID)
		}
		filtered = append(filtered, turn)
	}
	snapshot.Turns = filtered
	if len(snapshot.Turns) == 0 {
		return SnapshotDocument{}, ErrNoRootTurn
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return SnapshotDocument{}, err
	}
	if len(encoded) > MaxSnapshotBytes {
		return SnapshotDocument{}, newSizeLimitError(len(encoded), MaxSnapshotBytes)
	}
	return SnapshotDocument{Snapshot: snapshot, JSON: encoded}, nil
}
