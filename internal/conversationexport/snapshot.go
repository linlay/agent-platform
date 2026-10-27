package conversationexport

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

const (
	SnapshotVersion = 1
	MaxItems        = 2_000
	MaxTitleBytes   = 300
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
	return fmt.Sprintf("conversation export is %d bytes; limit is %d bytes", e.Actual, e.Limit)
}

func (e *SizeLimitError) Unwrap() error { return ErrTooLarge }

type AssistantV1 struct {
	Name      string `json:"name"`
	IconName  string `json:"iconName,omitempty"`
	Mode      string `json:"mode,omitempty"`
	IconColor string `json:"iconColor,omitempty"`
}

type ResolveAssistant func(agentKey string) *AssistantV1

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
	Version          int                 `json:"version"`
	Title            string              `json:"title"`
	Locale           string              `json:"locale"`
	CreatedAt        int64               `json:"createdAt"`
	CapturedAt       int64               `json:"capturedAt"`
	Turns            []TurnV1            `json:"turns"`
	Attachments      []AttachmentV1      `json:"attachments"`
	OmittedResources []OmittedResourceV1 `json:"omittedResources,omitempty"`
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
	AttachmentIDs   []string           `json:"attachmentIds,omitempty"`
	SteerID         string             `json:"steerId,omitempty"`
	Origin          *queryinput.Origin `json:"origin,omitempty"`
	ID              string             `json:"id"`
	Kind            string             `json:"kind"`
	Role            string             `json:"role,omitempty"`
	Text            string             `json:"text,omitempty"`
	At              int64              `json:"at"`
	RunID           string             `json:"runId"`
	TaskID          string             `json:"taskId,omitempty"`
	ReasoningLabel  string             `json:"reasoningLabel,omitempty"`
	ContentID       string             `json:"contentId,omitempty"`
	ToolID          string             `json:"toolId,omitempty"`
	ToolName        string             `json:"toolName,omitempty"`
	ToolLabel       string             `json:"toolLabel,omitempty"`
	Description     string             `json:"description,omitempty"`
	ArgsText        string             `json:"argsText,omitempty"`
	ResultText      string             `json:"resultText,omitempty"`
	ResultIsCode    bool               `json:"resultIsCode,omitempty"`
	Status          string             `json:"status,omitempty"`
	StartedAt       int64              `json:"startedAt,omitempty"`
	EndedAt         int64              `json:"endedAt,omitempty"`
	DurationMs      int64              `json:"durationMs,omitempty"`
	SourcePublishID string             `json:"sourcePublishId,omitempty"`
	SourceQuery     string             `json:"sourceQuery,omitempty"`
	SourceCount     int                `json:"sourceCount,omitempty"`
	ChunkCount      int                `json:"chunkCount,omitempty"`
	Sources         []SourceV1         `json:"sources,omitempty"`
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
	ID                string `json:"id"`
	Name              string `json:"name"`
	TaskAgentKey      string `json:"taskAgentKey,omitempty"`
	SubAgentName      string `json:"subAgentName,omitempty"`
	SubAgentIconName  string `json:"subAgentIconName,omitempty"`
	SubAgentMode      string `json:"subAgentMode,omitempty"`
	SubAgentIconColor string `json:"subAgentIconColor,omitempty"`
	Status            string `json:"status"`
	DurationMs        int64  `json:"durationMs,omitempty"`
	Error             string `json:"error,omitempty"`
}

type AttachmentV1 struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	MIMEType  string            `json:"mimeType"`
	Size      int64             `json:"size"`
	SHA256    string            `json:"sha256,omitempty"`
	SourceRef string            `json:"sourceRef"`
	MediaRefs map[string]string `json:"mediaRefs,omitempty"`
}

func sourceV1FromEvent(event stream.EventData) ([]SourceV1, int) {
	sources := []SourceV1{}
	chunkCount := 0
	for _, raw := range objectSlice(event.Value("sources")) {
		item, _ := raw.(map[string]any)
		title := stringField(item, "title")
		name := cmp.Or(stringField(item, "name"), path.Base(title))
		source := SourceV1{ID: cmp.Or(stringField(item, "id"), title, name), Name: name, Title: title}
		for index, raw := range objectSlice(item["chunks"]) {
			item, _ := raw.(map[string]any)
			chunk := SourceChunkV1{ChunkID: stringField(item, "chunkId"), Content: stringField(item, "content"), Path: stringField(item, "path")}
			if chunk.ChunkID == "" && chunk.Content == "" && chunk.Path == "" {
				continue
			}
			chunk.ChunkID = cmp.Or(chunk.ChunkID, fmt.Sprintf("%s_%d", source.ID, index+1))
			chunk.Index = index + 1
			if number, ok := item["index"].(float64); ok && number > 0 {
				chunk.Index = int(number)
			}
			source.Chunks = append(source.Chunks, chunk)
			source.ChunkIndexes = append(source.ChunkIndexes, chunk.Index)
		}
		if len(source.Chunks) > 0 {
			source.MinIndex = source.ChunkIndexes[0]
			chunkCount += len(source.Chunks)
			sources = append(sources, source)
		}
	}
	return sources, chunkCount
}

func stringField(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}

func objectSlice(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	items, _ := value.([]map[string]any)
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

func toolResultFailed(value any) bool {
	if text, ok := value.(string); ok && json.Unmarshal([]byte(text), &value) != nil {
		return false
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
		if nested := record[key]; nested != nil && toolResultFailed(nested) {
			return true
		}
	}
	return false
}

// BuildSnapshot projects ordinary conversation reads without resource I/O or JSON encoding.
func BuildSnapshot(summary *chat.Summary, events []stream.EventData, capturedAt int64, locale string, resolveAssistant ResolveAssistant) (SnapshotV1, error) {
	return projectSnapshot(summary, events, capturedAt, locale, resolveAssistant, nil)
}

func projectSnapshot(summary *chat.Summary, events []stream.EventData, capturedAt int64, locale string, resolveAssistant ResolveAssistant, resourceRefs map[string][]string) (SnapshotV1, error) {
	forExport := resourceRefs != nil
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
	if i18n.ResolveLocale(i18n.LocaleZhCN, locale) == i18n.LocaleEN {
		locale = "en-US"
	} else {
		locale = i18n.LocaleZhCN
	}
	snapshot := SnapshotV1{Version: SnapshotVersion, Title: title, Locale: locale, CreatedAt: summary.CreatedAt, CapturedAt: capturedAt, Turns: []TurnV1{}, Attachments: []AttachmentV1{}}
	turnByRun := map[string]int{}
	toolPosition := map[string]int{}
	toolByID := map[string]string{}
	nodePosition := map[string]int{}
	taskStartedAt := map[string]int64{}
	pendingTurn := -1
	for ordinal, event := range events {
		if err := timecontract.ValidateEpochMillis(event.Timestamp, "timestamp", "conversation.snapshot.events"); err != nil {
			return SnapshotV1{}, err
		}
		if event.Type == "request.query" && event.String("taskId") == "" && requestQueryVisible(event) {
			text := event.String("message")
			var refs []string
			if forExport {
				refs = queryResourceRefs(event.Value("references"))
			}
			if strings.TrimSpace(text) == "" && len(refs) == 0 {
				pendingTurn = -1
				continue
			}
			runID := strings.TrimSpace(event.String("runId"))
			if runID != "" {
				if _, exists := turnByRun[runID]; exists {
					return SnapshotV1{}, ErrInvalidTimeline
				}
			}
			turn := TurnV1{RunID: runID, QueryAt: event.Timestamp, StartedAt: event.Timestamp, Outcome: OutcomeRunning,
				Nodes: []NodeV1{{ID: fmt.Sprintf("query_%d_%d", event.Seq, ordinal), Kind: "message", Role: "user", Text: text, At: event.Timestamp, RunID: runID}}}
			if forExport {
				resourceRefs[turn.Nodes[0].ID] = refs
			}
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
			runID = toolByID[event.String("toolId")]
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
				turn.Assistant = resolveAssistant(event.String("agentKey"))
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
		case "request.steer":
			var origin *queryinput.Origin
			if raw := event.Value("origin"); raw != nil {
				data, err := json.Marshal(raw)
				if err != nil || json.Unmarshal(data, &origin) != nil {
					return SnapshotV1{}, ErrInvalidTimeline
				}
			}
			var refs []string
			if forExport {
				refs = queryResourceRefs(event.Value("references"))
				if strings.TrimSpace(event.String("message")) == "" && len(refs) == 0 {
					continue
				}
			} else if origin == nil {
				continue
			}
			turn.Nodes = append(turn.Nodes, NodeV1{ID: id, Kind: "message", Role: "user", Text: event.String("message"), At: event.Timestamp, RunID: runID, SteerID: event.String("steerId"), Origin: origin})
			if forExport {
				resourceRefs[id] = refs
			}
		case "reasoning.snapshot", "content.snapshot", "planning.snapshot":
			text := event.String("text")
			if strings.TrimSpace(text) == "" {
				continue
			}
			kind := strings.TrimSuffix(event.Type, ".snapshot")
			identity := event.String(kind + "Id")
			node := NodeV1{ID: id, Kind: kind, Role: "assistant", Text: text, At: event.Timestamp, RunID: runID, TaskID: taskID}
			switch kind {
			case "reasoning":
				node.Kind, node.ReasoningLabel = "thinking", event.String("reasoningLabel")
			case "content":
				node.ContentID = identity
			}
			if identity != "" {
				key := runID + "\x00" + node.Kind + "\x00" + identity
				if position, exists := nodePosition[key]; exists {
					turn.Nodes[position].Text = text
					continue
				}
				nodePosition[key] = len(turn.Nodes)
			}
			turn.Nodes = append(turn.Nodes, node)
		case "tool.snapshot", "tool.start", "tool.args", "tool.end", "tool.result":
			toolID := event.String("toolId")
			if toolID == "" {
				continue
			}
			key := runID + "\x00" + toolID
			position, exists := toolPosition[key]
			if !exists {
				if event.Type == "tool.args" || event.Type == "tool.end" {
					continue
				}
				node := NodeV1{ID: id, Kind: "tool", Role: "assistant", At: event.Timestamp, RunID: runID, TaskID: taskID,
					ToolID: toolID, ToolName: event.String("toolName"), ToolLabel: event.String("toolLabel"), ArgsText: event.String("arguments")}
				if event.Type != "tool.result" {
					node.Status, node.StartedAt = "running", event.Timestamp
				}
				position = len(turn.Nodes)
				turn.Nodes = append(turn.Nodes, node)
				toolPosition[key], toolByID[toolID] = position, runID
			}
			node := &turn.Nodes[position]
			if event.Type == "tool.args" {
				node.ArgsText += event.String("delta")
				continue
			}
			if args := event.String("arguments"); args != "" {
				node.ArgsText = args
			}
			if event.Type == "tool.end" {
				continue
			}
			if event.Type != "tool.result" {
				node.ToolName = cmp.Or(event.String("toolName"), node.ToolName)
				node.ToolLabel = cmp.Or(event.String("toolLabel"), node.ToolLabel)
				node.Description = cmp.Or(event.String("description"), node.Description)
				if exists {
					continue
				}
				break
			}
			node.EndedAt = event.Timestamp
			if node.StartedAt > 0 {
				node.DurationMs = max(0, event.Timestamp-node.StartedAt)
			}
			value := event.Value("result")
			for _, key := range []string{"output", "text"} {
				if value == nil {
					value = event.Value(key)
				}
			}
			node.Status = "success"
			if event.String("error") != "" || toolResultFailed(value) {
				node.Status = "failed"
			}
			if forExport && node.Status == "success" && node.ToolName == "image_generate" {
				resourceRefs[node.ID] = generatedImageRefs(value)
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
			task := TaskV1{ID: taskID, Name: event.String("taskName"), TaskAgentKey: event.String("taskAgentKey"), Status: "running"}
			if resolveAssistant != nil && task.TaskAgentKey != "" {
				if agent := resolveAssistant(task.TaskAgentKey); agent != nil {
					task.SubAgentName, task.SubAgentIconName = agent.Name, agent.IconName
					task.SubAgentMode, task.SubAgentIconColor = agent.Mode, agent.IconColor
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
			return SnapshotV1{}, ErrTooLarge
		}
	}
	filtered := make([]TurnV1, 0, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		if turn.RunID == "" {
			continue
		}
		if turn.Assistant == nil && resolveAssistant != nil {
			turn.Assistant = resolveAssistant(summary.AgentKey)
		}
		filtered = append(filtered, turn)
	}
	snapshot.Turns = filtered
	if len(snapshot.Turns) == 0 {
		return SnapshotV1{}, ErrNoRootTurn
	}
	return snapshot, nil
}

// requestQueryVisible reports conversation input. Automation input is not part
// of the conversation; role marks it on events recorded before origin.
func requestQueryVisible(event stream.EventData) bool {
	if origin := queryinput.OriginFromPayload(event.Value("origin")); origin != nil && origin.Type == queryinput.OriginAutomation {
		return false
	}
	return api.QueryRoleVisible(event.String("role"))
}
