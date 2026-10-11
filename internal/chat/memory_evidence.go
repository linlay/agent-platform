package chat

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// MemoryChats pages completed conversations independently of UI pinning/order.
// A chat is settled only when it has no active run and no pending interaction.
// Reopened chats become eligible again when their new run has completed.
type MemoryChat struct {
	ChatID      string
	CompletedAt int64
	RunCount    int
}

func (s *FileStore) MemoryChats(ctx context.Context, since, after int64, afterID string, limit int) ([]MemoryChat, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("invalid memory page size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT c.CHAT_ID_, MAX(r.COMPLETED_AT_) AS completed, COUNT(r.RUN_ID_)
 FROM CHATS c JOIN RUNS r ON r.CHAT_ID_=c.CHAT_ID_
 WHERE COALESCE(c.AWAITING_ID_,'')='' AND c.SOURCE_ NOT LIKE 'automation:%' AND c.SOURCE_ NOT LIKE 'run-query:%'
 AND NOT EXISTS(SELECT 1 FROM RUNS active WHERE active.CHAT_ID_=c.CHAT_ID_ AND active.COMPLETED_AT_=0)
 GROUP BY c.CHAT_ID_ HAVING completed>=? AND (completed>? OR (completed=? AND c.CHAT_ID_>?))
 ORDER BY completed,c.CHAT_ID_ LIMIT ?`, since, after, after, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryChat
	for rows.Next() {
		var c MemoryChat
		if err = rows.Scan(&c.ChatID, &c.CompletedAt, &c.RunCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type MemoryMessage struct {
	ID, Role, Content string
	At                int64
}

// MemoryMessages reads original physical user/assistant text, including text
// hidden by L1/L2. It never treats compaction, system prompts, tool output,
// approval audit messages or delegated instructions as user evidence.
func (s *FileStore) MemoryMessages(chatID, runID string) ([]MemoryMessage, error) {
	if !ValidChatID(chatID) {
		return nil, ErrChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := os.Stat(s.chatJSONLPath(chatID))
	if err != nil {
		return nil, err
	}
	if info.Size() > 64<<20 {
		return nil, fmt.Errorf("memory chat exceeds 64 MiB")
	}
	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return nil, err
	}
	return memoryMessagesFromLines(lines, runID), nil
}

func memoryMessagesFromLines(lines []map[string]any, runID string) []MemoryMessage {
	var out []MemoryMessage
	for _, line := range lines {
		if stringValue(line["runId"]) != runID || stringValue(line["taskId"]) != "" || stringValue(line["subAgentKey"]) != "" || stringValue(line["taskSubAgentKey"]) != "" {
			continue
		}
		at := int64FromAny(line["updatedAt"])
		switch stringValue(line["_type"]) {
		case "query":
			if lineIsSystemInitQuery(line) {
				continue
			}
			q := mapValue(line["query"])
			if role := stringValue(q["role"]); role != "" && role != "user" {
				continue
			}
			if stringValue(q["source"]) == "automation" || q["runOrigin"] != nil {
				continue
			}
			if text := stringValue(q["message"]); strings.TrimSpace(text) != "" {
				out = append(out, MemoryMessage{ID: runID + ":query", Role: "user", Content: text, At: at})
			}
		case "steer":
			if msg := mapValue(line["steer"]); len(msg) > 0 && (stringValue(msg["role"]) == "" || stringValue(msg["role"]) == "user") {
				if text := stringValue(msg["message"]); strings.TrimSpace(text) != "" {
					out = append(out, MemoryMessage{ID: fmt.Sprintf("%s:steer:%d", runID, at), Role: "user", Content: text, At: at})
				}
			}
		case StepLineTypeReact:
			for _, m := range anyMessageSlice(line["messages"]) {
				if internal, _ := m["_internalOnly"].(bool); internal {
					continue
				}
				if stringValue(m["role"]) != "assistant" || len(anyMessageSlice(m["tool_calls"])) > 0 {
					continue
				}
				text := stringValue(m["content"])
				if parts, ok := m["content"].([]any); ok {
					text = extractTextFromContent(parts)
				}
				if strings.TrimSpace(text) != "" {
					out = append(out, MemoryMessage{ID: fmt.Sprintf("%s:assistant:%d:%s", runID, at, stringValue(m["_msgId"])), Role: "assistant", Content: text, At: at})
				}
			}
		}
	}
	return out
}
