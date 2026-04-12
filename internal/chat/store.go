package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/stream"

	_ "modernc.org/sqlite"
)

var ErrChatNotFound = errors.New("chat not found")

type Store interface {
	EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error)
	Summary(chatID string) (*Summary, error)
	AppendEvent(chatID string, event stream.EventData) error
	AppendQueryLine(chatID string, line QueryLine) error
	AppendStepLine(chatID string, line StepLine) error
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

// ---------------------------------------------------------------------------
// SQLite index (replaces index.json, matching Java chats.db)
// ---------------------------------------------------------------------------

func (s *FileStore) initDB() error {
	dbPath := filepath.Join(s.root, "chats.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open chats.db: %w", err)
	}
	s.db = db

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS CHATS (
			CHAT_ID_          TEXT PRIMARY KEY,
			CHAT_NAME_        TEXT NOT NULL,
			AGENT_KEY_        TEXT NOT NULL DEFAULT '',
			TEAM_ID_          TEXT,
			CREATED_AT_       INTEGER NOT NULL,
			UPDATED_AT_       INTEGER NOT NULL,
			LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
			READ_STATUS_      INTEGER NOT NULL DEFAULT 1,
			READ_AT_          INTEGER
		);
		CREATE INDEX IF NOT EXISTS IDX_CHATS_LAST_RUN_ID_ ON CHATS(LAST_RUN_ID_);
	`)
	if err != nil {
		return fmt.Errorf("create chats table: %w", err)
	}

	// Migrate from index.json if it exists and DB is empty
	s.migrateFromIndexJSON()
	return nil
}

func (s *FileStore) migrateFromIndexJSON() {
	path := filepath.Join(s.root, "index.json")
	file, err := os.Open(path)
	if err != nil {
		return // no index.json, nothing to migrate
	}
	defer file.Close()

	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM CHATS").Scan(&count)
	if count > 0 {
		return // DB already has data
	}

	var summaries map[string]Summary
	if err := json.NewDecoder(file).Decode(&summaries); err != nil || len(summaries) == 0 {
		return
	}
	for _, sum := range summaries {
		_, _ = s.db.Exec(`INSERT OR IGNORE INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_, READ_AT_)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sum.ChatID, sum.ChatName, sum.AgentKey, nilIfEmpty(sum.TeamID),
			sum.CreatedAt, sum.UpdatedAt, sum.LastRunID, sum.LastRunContent,
			sum.ReadStatus, sum.ReadAt)
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *FileStore) EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if exists
	var existing Summary
	err := s.db.QueryRow("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_, READ_AT_ FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&existing.ChatID, &existing.ChatName, &existing.AgentKey, &existing.TeamID, &existing.CreatedAt, &existing.UpdatedAt, &existing.LastRunID, &existing.LastRunContent, &existing.ReadStatus, &existing.ReadAt)
	if err == nil {
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
	_, err = s.db.Exec(`INSERT INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_)
		VALUES (?, ?, ?, ?, ?, ?, '', '', 1)`,
		chatID, summary.ChatName, agentKey, nilIfEmpty(teamID), now, now)
	if err != nil {
		return Summary{}, false, err
	}
	// Create directory for uploads/attachments
	_ = os.MkdirAll(s.ChatDir(chatID), 0o755)
	return summary, true, nil
}

// AppendEvent writes a raw SSE event to events.jsonl (legacy path).
func (s *FileStore) AppendEvent(chatID string, event stream.EventData) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "events.jsonl"), event)
}

func (s *FileStore) AppendQueryLine(chatID string, line QueryLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

func (s *FileStore) AppendStepLine(chatID string, line StepLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

// chatJSONLPath returns the path to {chatId}.jsonl (flat file, matching Java).
func (s *FileStore) chatJSONLPath(chatID string) string {
	return filepath.Join(s.root, chatID+".jsonl")
}

func (s *FileStore) Summary(chatID string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSummary(chatID)
}

func (s *FileStore) loadSummary(chatID string) (*Summary, error) {
	var sum Summary
	err := s.db.QueryRow("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_, READ_AT_ FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.ReadStatus, &sum.ReadAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

// LoadRawMessages loads conversation history from {chatId}.jsonl step lines,
// falling back to {chatId}/raw_messages.jsonl for old chats.
func (s *FileStore) LoadRawMessages(chatID string, k int) ([]map[string]any, error) {
	if k <= 0 {
		k = 20
	}

	// Try loading from step lines in {chatId}.jsonl (Java-compatible path)
	messages := s.loadRawMessagesFromJSONL(chatID)
	if len(messages) == 0 {
		// Fallback to old raw_messages.jsonl
		var err error
		messages, err = readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
		if err != nil || len(messages) == 0 {
			return nil, err
		}
	}

	// Group by runId, keep last K runs (sliding window)
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

// loadRawMessagesFromJSONL extracts OpenAI-format messages from step lines.
func (s *FileStore) loadRawMessagesFromJSONL(chatID string) []map[string]any {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil
	}
	if !isNewFormat(lines) {
		return nil
	}

	var messages []map[string]any
	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case "query":
			query, _ := line["query"].(map[string]any)
			if query == nil {
				continue
			}
			msg := map[string]any{
				"runId":   runID,
				"role":    stringValue(query["role"]),
				"content": stringValue(query["message"]),
				"ts":      line["updatedAt"],
			}
			messages = append(messages, msg)

		case "step", "react", "plan-execute":
			rawMsgs, _ := line["messages"].([]any)
			for _, raw := range rawMsgs {
				m, _ := raw.(map[string]any)
				if m == nil {
					continue
				}
				role, _ := m["role"].(string)
				msg := map[string]any{"runId": runID}
				for k, v := range m {
					msg[k] = v
				}
				// Flatten content parts to plain text for LLM context
				if role == "user" || role == "assistant" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
					if parts, ok := m["reasoning_content"].([]any); ok {
						msg["reasoning_content"] = extractTextFromContent(parts)
					}
				}
				if role == "tool" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
				}
				messages = append(messages, msg)
			}
		}
	}
	return messages
}

func (s *FileStore) OnRunCompleted(completion RunCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE CHATS SET LAST_RUN_ID_=?, LAST_RUN_CONTENT_=?, UPDATED_AT_=?, READ_STATUS_=0, READ_AT_=NULL WHERE CHAT_ID_=?`,
		completion.RunID, completion.AssistantText, completion.UpdatedAtMillis, completion.ChatID)
	return err
}

func (s *FileStore) ListChats(lastRunID string, agentKey string) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := "SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_, READ_AT_ FROM CHATS WHERE 1=1"
	var args []any
	if agentKey != "" {
		query += " AND AGENT_KEY_=?"
		args = append(args, agentKey)
	}
	query += " ORDER BY UPDATED_AT_ DESC, CHAT_ID_ DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Summary
	for rows.Next() {
		var sum Summary
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.ReadStatus, &sum.ReadAt); err != nil {
			return nil, err
		}
		if lastRunID != "" && !RunIDAfter(sum.LastRunID, lastRunID) {
			continue
		}
		items = append(items, sum)
	}
	return items, rows.Err()
}

func (s *FileStore) LoadChat(chatID string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return Detail{}, err
	}
	if sum == nil {
		return Detail{}, ErrChatNotFound
	}

	// Read {chatId}.jsonl (flat file, Java format). Fallback to {chatId}/events.jsonl (old Go format).
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return Detail{}, err
	}
	if len(lines) == 0 {
		lines, err = readJSONLines(filepath.Join(s.ChatDir(chatID), "events.jsonl"))
		if err != nil {
			return Detail{}, err
		}
	}

	// Load raw messages for includeRawMessages support
	rawMessages := s.loadRawMessagesFromJSONL(chatID)
	if len(rawMessages) == 0 {
		rawMessages, _ = readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
	}

	// Detect format: new format has _type field, old format has type field.
	if isNewFormat(lines) {
		return s.loadChatNewFormat(*sum, lines, rawMessages)
	}
	return s.loadChatLegacyFormat(*sum, lines, rawMessages)
}

// ---------------------------------------------------------------------------
// New format: _type = "query" / "step" / "event" (matching Java)
// ---------------------------------------------------------------------------

func (s *FileStore) loadChatNewFormat(summary Summary, lines []map[string]any, rawMessages []map[string]any) (Detail, error) {
	var plan *PlanState
	var artifact *ArtifactState

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

		case "react", "plan-execute", "step":
			rd := ensureRun(runs, &runOrder, runID)

			if rawPlan, ok := line["plan"].(map[string]any); ok {
				plan = parsePlanFromStep(rawPlan)
			}
			if rawArt, ok := line["artifacts"].(map[string]any); ok {
				artifact = parseArtifactFromStep(rawArt)
			}

			// new format uses "stage", legacy uses "_stage"
			stage, _ := line["stage"].(string)
			if stage == "" {
				stage, _ = line["_stage"].(string)
			}
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
		}
	}

	allEvents := make([]stream.EventData, 0)

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
		// Synthesize run.complete for the frontend (not persisted in JSONL).
		if runID != "" {
			allEvents = append(allEvents, stream.EventData{
				Seq:     nextSeq(),
				Type:    "run.complete",
				Payload: map[string]any{"runId": runID, "finishReason": "stop"},
			})
		}
	}

	for i := range allEvents {
		allEvents[i].Seq = int64(i + 1)
	}

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		RawMessages: rawMessages,
		Events:      allEvents,
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
		RawMessages: rawMessages,
		Events:      orderedEvents,
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
							"toolId":    id,
							"runId":     runID,
							"toolName":  fnName,
							"taskId":    taskID,
							"arguments": fnArgs,
						},
					})
				}
			}
		}

	case "tool":
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
// Legacy format helpers
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

	now := time.Now().UnixMilli()
	result, err := s.db.Exec("UPDATE CHATS SET READ_STATUS_=1, READ_AT_=? WHERE CHAT_ID_=?", now, chatID)
	if err != nil {
		return Summary{}, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return Summary{}, ErrChatNotFound
	}
	sum, err := s.loadSummary(chatID)
	if err != nil || sum == nil {
		return Summary{}, ErrChatNotFound
	}
	return *sum, nil
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

// readJSONLines reads a JSONL file. Uses json.Decoder so it handles both
// single-line JSON objects (Go's writer) and pretty-printed multi-line JSON
// objects (Java may write either format).
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
	decoder := json.NewDecoder(file)
	for {
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse JSONL: %w", err)
		}
		if payload != nil {
			items = append(items, payload)
		}
	}
	return items, nil
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

// RunIDAfter and related helpers are in run_id.go
