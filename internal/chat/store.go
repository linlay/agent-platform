package chat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/stream"
)

var ErrChatNotFound = errors.New("chat not found")

type Store interface {
	EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error)
	Summary(chatID string) (*Summary, error)
	AppendEvent(chatID string, event stream.EventData) error
	AppendQueryLine(chatID string, line QueryLine) error
	AppendStepLine(chatID string, line StepLine) error
	AppendEventLine(chatID string, line EventLine) error
	AppendRawMessage(chatID string, message map[string]any) error
	LoadRawMessages(chatID string, k int) ([]map[string]any, error)
	OnRunCompleted(completion RunCompletion) error
	ListChats(lastRunID string, agentKey string) ([]Summary, error)
	LoadChat(chatID string) (Detail, error)
	MarkRead(chatID string) (Summary, error)
	ResolveResource(file string) (string, error)
	ChatDir(chatID string) string
}

type FileStore struct {
	root string
	mu   sync.Mutex
}

func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return Summary{}, false, err
	}

	if existing, ok := summaries[chatID]; ok {
		return existing, false, nil
	}

	now := time.Now().UnixMilli()
	summary := Summary{
		ChatID:     chatID,
		ChatName:   defaultChatName(firstMessage),
		AgentKey:   agentKey,
		TeamID:     teamID,
		CreatedAt:  now,
		UpdatedAt:  now,
		ReadStatus: 1,
	}
	summaries[chatID] = summary
	if err := s.writeIndexLocked(summaries); err != nil {
		return Summary{}, false, err
	}
	if err := os.MkdirAll(s.ChatDir(chatID), 0o755); err != nil {
		return Summary{}, false, err
	}
	return summary, true, nil
}

// AppendEvent writes a raw SSE event to events.jsonl (legacy path, kept for
// backward compatibility). New code should use StepWriter which calls
// AppendQueryLine / AppendStepLine / AppendEventLine.
func (s *FileStore) AppendEvent(chatID string, event stream.EventData) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "events.jsonl"), event)
}

func (s *FileStore) AppendQueryLine(chatID string, line QueryLine) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "events.jsonl"), line)
}

func (s *FileStore) AppendStepLine(chatID string, line StepLine) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "events.jsonl"), line)
}

func (s *FileStore) AppendEventLine(chatID string, line EventLine) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "events.jsonl"), line)
}

func (s *FileStore) Summary(chatID string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return nil, err
	}
	summary, ok := summaries[chatID]
	if !ok {
		return nil, nil
	}
	copy := summary
	return &copy, nil
}

func (s *FileStore) AppendRawMessage(chatID string, message map[string]any) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"), message)
}

func (s *FileStore) LoadRawMessages(chatID string, k int) ([]map[string]any, error) {
	if k <= 0 {
		k = 20
	}
	messages, err := readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
	if err != nil || len(messages) == 0 {
		return nil, err
	}

	// Group messages by runId, preserving order
	type runBucket struct {
		runID    string
		messages []map[string]any
	}
	var runs []*runBucket
	runIndex := map[string]*runBucket{}
	for _, msg := range messages {
		runID, _ := msg["runId"].(string)
		if runID == "" {
			bucket := &runBucket{messages: []map[string]any{msg}}
			runs = append(runs, bucket)
			continue
		}
		bucket, ok := runIndex[runID]
		if !ok {
			bucket = &runBucket{runID: runID}
			runIndex[runID] = bucket
			runs = append(runs, bucket)
		}
		bucket.messages = append(bucket.messages, msg)
	}

	if len(runs) > k {
		runs = runs[len(runs)-k:]
	}

	var result []map[string]any
	for _, bucket := range runs {
		result = append(result, bucket.messages...)
	}
	return result, nil
}

func (s *FileStore) OnRunCompleted(completion RunCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return err
	}

	summary, ok := summaries[completion.ChatID]
	if !ok {
		return ErrChatNotFound
	}

	summary.LastRunID = completion.RunID
	summary.LastRunContent = completion.AssistantText
	summary.UpdatedAt = completion.UpdatedAtMillis
	summary.ReadStatus = 0
	summary.ReadAt = nil
	summaries[completion.ChatID] = summary
	return s.writeIndexLocked(summaries)
}

func (s *FileStore) ListChats(lastRunID string, agentKey string) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return nil, err
	}

	items := make([]Summary, 0, len(summaries))
	for _, summary := range summaries {
		if agentKey != "" && summary.AgentKey != agentKey {
			continue
		}
		if lastRunID != "" && !RunIDAfter(summary.LastRunID, lastRunID) {
			continue
		}
		items = append(items, summary)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt == items[j].UpdatedAt {
			return items[i].ChatID > items[j].ChatID
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})

	return items, nil
}

func (s *FileStore) LoadChat(chatID string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return Detail{}, err
	}
	summary, ok := summaries[chatID]
	if !ok {
		return Detail{}, ErrChatNotFound
	}

	lines, err := readJSONLines(filepath.Join(s.ChatDir(chatID), "events.jsonl"))
	if err != nil {
		return Detail{}, err
	}
	rawMessages, err := readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
	if err != nil {
		return Detail{}, err
	}

	// Detect format: new format has _type field, old format has type field.
	if isNewFormat(lines) {
		return s.loadChatNewFormat(summary, lines, rawMessages)
	}
	return s.loadChatLegacyFormat(summary, lines, rawMessages)
}

// ---------------------------------------------------------------------------
// New format: _type = "query" / "step" / "event" (matching Java)
// ---------------------------------------------------------------------------

func (s *FileStore) loadChatNewFormat(summary Summary, lines []map[string]any, rawMessages []map[string]any) (Detail, error) {
	var plan *PlanState
	var artifact *ArtifactState

	// Parse lines and build SSE-style events for the frontend
	runs := map[string]*chatRunData{}
	var runOrder []string
	var chatStartEvent *stream.EventData

	seq := int64(0)
	nextSeq := func() int64 { seq++; return seq }

	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		chatID, _ := line["chatId"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case "query":
			// Reconstruct request.query event
			query, _ := line["query"].(map[string]any)
			if query == nil {
				query = map[string]any{}
			}
			payload := map[string]any{}
			for k, v := range query {
				payload[k] = v
			}
			if _, ok := payload["chatId"]; !ok {
				payload["chatId"] = chatID
			}

			rd := ensureRun(runs, &runOrder, runID)
			rd.events = append(rd.events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "request.query",
				Timestamp: int64FromAny(line["updatedAt"]),
				Payload:   payload,
			})

		case "step":
			rd := ensureRun(runs, &runOrder, runID)

			// Extract plan/artifact state from step
			if rawPlan, ok := line["plan"].(map[string]any); ok {
				plan = parsePlanFromStep(rawPlan)
			}
			if rawArt, ok := line["artifacts"].(map[string]any); ok {
				artifact = parseArtifactFromStep(rawArt)
			}

			// Reconstruct SSE events from stored messages
			stage, _ := line["_stage"].(string)
			taskID, _ := line["taskId"].(string)
			msgs, _ := line["messages"].([]any)
			for _, rawMsg := range msgs {
				msgMap, _ := rawMsg.(map[string]any)
				if msgMap == nil {
					continue
				}
				for _, ev := range storedMessageToEvents(msgMap, runID, taskID, stage, nextSeq) {
					rd.events = append(rd.events, ev)
				}
			}

		case "event":
			eventMap, _ := line["event"].(map[string]any)
			if eventMap == nil {
				continue
			}
			eventType, _ := eventMap["type"].(string)

			// Skip artifact.publish — promoted to top-level
			if eventType == "artifact.publish" {
				continue
			}

			rd := ensureRun(runs, &runOrder, runID)
			payload := map[string]any{}
			for k, v := range eventMap {
				if k == "type" || k == "seq" || k == "timestamp" {
					continue
				}
				payload[k] = v
			}
			rd.events = append(rd.events, stream.EventData{
				Seq:       nextSeq(),
				Type:      eventType,
				Timestamp: int64FromAny(eventMap["timestamp"]),
				Payload:   payload,
			})
		}
	}

	// Assemble final events list
	allEvents := make([]stream.EventData, 0)

	// Add chat.start as first event
	if chatStartEvent == nil && summary.ChatName != "" {
		allEvents = append(allEvents, stream.EventData{
			Seq:       nextSeq(),
			Type:      "chat.start",
			Timestamp: summary.CreatedAt,
			Payload:   map[string]any{"chatId": summary.ChatID, "chatName": summary.ChatName},
		})
	}

	for _, runID := range runOrder {
		rd := runs[runID]
		// Insert run.start before run body if not already present
		hasRunStart := false
		for _, ev := range rd.events {
			if ev.Type == "run.start" {
				hasRunStart = true
				break
			}
		}
		if !hasRunStart && runID != "" {
			allEvents = append(allEvents, stream.EventData{
				Seq:       nextSeq(),
				Type:      "run.start",
				Timestamp: 0,
				Payload:   map[string]any{"runId": runID, "chatId": summary.ChatID, "agentKey": rd.agentKey},
			})
		}
		allEvents = append(allEvents, rd.events...)
	}

	// Re-sequence
	for i := range allEvents {
		allEvents[i].Seq = int64(i + 1)
	}

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		Events:      allEvents,
		RawMessages: rawMessages,
		References:  nil,
		Plan:        plan,
		Artifact:    artifact,
	}, nil
}

// ---------------------------------------------------------------------------
// Legacy format: raw SSE events with "type" field (old Go format)
// ---------------------------------------------------------------------------

func (s *FileStore) loadChatLegacyFormat(summary Summary, events []map[string]any, rawMessages []map[string]any) (Detail, error) {
	events = rebuildSnapshotEvents(events)

	plan, artifact := deriveRunState(events)
	orderedEvents := make([]stream.EventData, 0, len(events))
	for _, event := range events {
		eventType, _ := event["type"].(string)
		if eventType == "plan.create" || eventType == "plan.update" || eventType == "artifact.publish" ||
			eventType == "stage.marker" {
			continue
		}
		orderedEvents = append(orderedEvents, stream.EventDataFromMap(event))
	}

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		Events:      orderedEvents,
		RawMessages: rawMessages,
		References:  nil,
		Plan:        plan,
		Artifact:    artifact,
	}, nil
}

// ---------------------------------------------------------------------------
// Format detection
// ---------------------------------------------------------------------------

func isNewFormat(lines []map[string]any) bool {
	for _, line := range lines {
		if _, ok := line["_type"]; ok {
			return true
		}
		if _, ok := line["type"]; ok {
			return false
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Step line → SSE events reconstruction
// ---------------------------------------------------------------------------

func storedMessageToEvents(msg map[string]any, runID, taskID, stage string, nextSeq func() int64) []stream.EventData {
	role, _ := msg["role"].(string)
	var events []stream.EventData

	switch role {
	case "assistant":
		// Reasoning snapshot
		if rc, ok := msg["reasoning_content"]; ok {
			text := extractTextFromContent(rc)
			if text != "" {
				reasoningID, _ := msg["_reasoningId"].(string)
				events = append(events, stream.EventData{
					Seq:  nextSeq(),
					Type: "reasoning.snapshot",
					Payload: map[string]any{
						"reasoningId": reasoningID,
						"runId":       runID,
						"text":        text,
						"taskId":      taskID,
					},
				})
			}
		}
		// Content snapshot
		if c, ok := msg["content"]; ok {
			text := extractTextFromContent(c)
			if text != "" {
				contentID, _ := msg["_contentId"].(string)
				events = append(events, stream.EventData{
					Seq:  nextSeq(),
					Type: "content.snapshot",
					Payload: map[string]any{
						"contentId": contentID,
						"runId":     runID,
						"text":      text,
						"taskId":    taskID,
					},
				})
			}
		}
		// Tool/Action snapshots
		if tcs, ok := msg["tool_calls"].([]any); ok {
			actionID, _ := msg["_actionId"].(string)
			toolID, _ := msg["_toolId"].(string)
			for _, tc := range tcs {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				fn, _ := tcMap["function"].(map[string]any)
				if fn == nil {
					fn = map[string]any{}
				}
				callID, _ := tcMap["id"].(string)
				fnName, _ := fn["name"].(string)
				fnArgs, _ := fn["arguments"].(string)

				if actionID != "" {
					events = append(events, stream.EventData{
						Seq:  nextSeq(),
						Type: "action.snapshot",
						Payload: map[string]any{
							"actionId":   callID,
							"runId":      runID,
							"actionName": fnName,
							"taskId":     taskID,
							"arguments":  fnArgs,
						},
					})
				} else {
					id := toolID
					if id == "" {
						id = callID
					}
					events = append(events, stream.EventData{
						Seq:  nextSeq(),
						Type: "tool.snapshot",
						Payload: map[string]any{
							"toolId":   id,
							"runId":    runID,
							"toolName": fnName,
							"taskId":   taskID,
							"arguments": fnArgs,
						},
					})
				}
			}
		}

	case "tool":
		// Tool/Action result
		text := extractTextFromContent(msg["content"])
		actionID, _ := msg["_actionId"].(string)
		toolID, _ := msg["_toolId"].(string)
		toolCallID, _ := msg["tool_call_id"].(string)

		if actionID != "" {
			events = append(events, stream.EventData{
				Seq:  nextSeq(),
				Type: "action.result",
				Payload: map[string]any{
					"actionId": toolCallID,
					"result":   text,
				},
			})
		} else {
			id := toolID
			if id == "" {
				id = toolCallID
			}
			events = append(events, stream.EventData{
				Seq:  nextSeq(),
				Type: "tool.result",
				Payload: map[string]any{
					"toolId": id,
					"result": text,
				},
			})
		}
	}

	return events
}

func extractTextFromContent(v any) string {
	// Handle []ContentPart format: [{"type":"text","text":"..."}]
	if parts, ok := v.([]any); ok {
		var sb strings.Builder
		for _, part := range parts {
			if pMap, ok := part.(map[string]any); ok {
				if text, ok := pMap["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	}
	// Handle plain string
	if text, ok := v.(string); ok {
		return text
	}
	return ""
}

func parsePlanFromStep(raw map[string]any) *PlanState {
	planID, _ := raw["planId"].(string)
	plan := &PlanState{PlanID: planID, Tasks: []PlanTaskState{}}
	tasks, _ := raw["tasks"].([]any)
	for _, t := range tasks {
		tMap, _ := t.(map[string]any)
		if tMap == nil {
			continue
		}
		plan.Tasks = append(plan.Tasks, PlanTaskState{
			TaskID:      stringValue(tMap["taskId"]),
			Description: stringValue(tMap["description"]),
			Status:      stringValue(tMap["status"]),
		})
	}
	return plan
}

func parseArtifactFromStep(raw map[string]any) *ArtifactState {
	art := &ArtifactState{}
	items, _ := raw["items"].([]any)
	for _, item := range items {
		iMap, _ := item.(map[string]any)
		if iMap == nil {
			continue
		}
		art.Items = append(art.Items, ArtifactItemState{
			ArtifactID: stringValue(iMap["artifactId"]),
			Type:       stringValue(iMap["type"]),
			Name:       stringValue(iMap["name"]),
			URL:        stringValue(iMap["url"]),
		})
	}
	return art
}

type chatRunData struct {
	runID    string
	agentKey string
	events   []stream.EventData
}

func ensureRun(runs map[string]*chatRunData, order *[]string, runID string) *chatRunData {
	if rd, ok := runs[runID]; ok {
		return rd
	}
	rd := &chatRunData{runID: runID}
	runs[runID] = rd
	*order = append(*order, runID)
	return rd
}

func int64FromAny(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Legacy format helpers (kept for backward compatibility with old JSONL files)
// ---------------------------------------------------------------------------

func deriveRunState(events []map[string]any) (*PlanState, *ArtifactState) {
	var plan *PlanState
	var artifact *ArtifactState
	for _, event := range events {
		eventType, _ := event["type"].(string)
		switch eventType {
		case "plan.create", "plan.update":
			planID, _ := event["planId"].(string)
			next := &PlanState{PlanID: planID, Tasks: []PlanTaskState{}}

			// The "plan" field may be the tasks array directly or a map.
			rawPlan := event["plan"]
			if items, ok := rawPlan.([]any); ok {
				for _, item := range items {
					mapped, _ := item.(map[string]any)
					if mapped == nil {
						continue
					}
					next.Tasks = append(next.Tasks, PlanTaskState{
						TaskID:      stringValue(mapped["taskId"]),
						Description: stringValue(mapped["description"]),
						Status:      stringValue(mapped["status"]),
					})
				}
				plan = next
				continue
			}
			// Try map form
			if rawMap, ok := rawPlan.(map[string]any); ok {
				var rawTasks any
				rawTasks = rawMap["tasks"]
				if rawTasks == nil {
					rawTasks = rawMap["plan"]
				}
				if items, ok := rawTasks.([]any); ok {
					for _, item := range items {
						mapped, _ := item.(map[string]any)
						if mapped == nil {
							continue
						}
						next.Tasks = append(next.Tasks, PlanTaskState{
							TaskID:      stringValue(mapped["taskId"]),
							Description: stringValue(mapped["description"]),
							Status:      stringValue(mapped["status"]),
						})
					}
				}
			}
			plan = next

		case "artifact.publish":
			if artifact == nil {
				artifact = &ArtifactState{}
			}
			item, _ := event["artifact"].(map[string]any)
			if item == nil {
				continue
			}
			artifact.Items = append(artifact.Items, ArtifactItemState{
				ArtifactID: stringValue(event["artifactId"]),
				Type:       stringValue(item["type"]),
				Name:       stringValue(item["name"]),
				URL:        stringValue(item["url"]),
			})
		}
	}
	return plan, artifact
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func (s *FileStore) MarkRead(chatID string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return Summary{}, err
	}
	summary, ok := summaries[chatID]
	if !ok {
		return Summary{}, ErrChatNotFound
	}
	now := time.Now().UnixMilli()
	summary.ReadStatus = 1
	summary.ReadAt = &now
	summaries[chatID] = summary
	if err := s.writeIndexLocked(summaries); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func (s *FileStore) ResolveResource(file string) (string, error) {
	clean := filepath.Clean(file)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", os.ErrPermission
	}
	path := filepath.Join(s.root, clean)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *FileStore) ChatDir(chatID string) string {
	return filepath.Join(s.root, chatID)
}

func (s *FileStore) appendJSONLine(path string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	return encoder.Encode(payload)
}

func (s *FileStore) readIndexLocked() (map[string]Summary, error) {
	path := filepath.Join(s.root, "index.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var summaries map[string]Summary
	if err := json.NewDecoder(file).Decode(&summaries); err != nil {
		return nil, err
	}
	if summaries == nil {
		return map[string]Summary{}, nil
	}
	return summaries, nil
}

func (s *FileStore) writeIndexLocked(summaries map[string]Summary) error {
	path := filepath.Join(s.root, "index.json")
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(summaries); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSONLines(path string) ([]map[string]any, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var items []map[string]any
	scanner := bufio.NewScanner(file)
	// Increase scanner buffer for large lines (step lines with many messages)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			return nil, fmt.Errorf("parse JSONL line: %w", err)
		}
		items = append(items, payload)
	}
	return items, scanner.Err()
}

func defaultChatName(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "default"
	}
	runes := []rune(message)
	if len(runes) > 24 {
		return string(runes[:24])
	}
	return message
}
