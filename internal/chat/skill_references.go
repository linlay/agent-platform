package chat

import (
	"encoding/json"
	"os"
	"strings"

	"agent-platform/internal/contracts/queryinput"
)

// PriorSkillReferenceIDs returns the most recently selected distinct skills.
// Only platform-written query decisions and consumed steer payloads count;
// caller input, model messages and compact summaries cannot grant access.
func (s *FileStore) PriorSkillReferenceIDs(chatID, excludeRunID string, limit int) ([]string, error) {
	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return nil, os.ErrPermission
	}
	if limit <= 0 {
		return nil, nil
	}
	// Like LoadRawMessages, do not hold FileStore's global write lock while
	// reading/parsing history. A concurrent partial append fails this optional
	// lookup; session admission logs it and continues without inherited grants.
	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return nil, err
	}
	// Steer lines do not carry lane/task metadata. Establish their Run's
	// main-lane identity from query lines before inspecting any references.
	mainRuns := map[string]bool{}
	for _, line := range lines {
		if stringFromAny(line["_type"]) == "query" && !lineIsSystemInitQuery(line) &&
			strings.TrimSpace(stringFromAny(line["taskId"])) == "" && lineTaskAgentKey(line) == "" &&
			strings.TrimSpace(stringFromAny(line["lane"])) == "" {
			mainRuns[strings.TrimSpace(stringFromAny(line["runId"]))] = true
		}
	}
	excludeRunID = strings.TrimSpace(excludeRunID)
	seen := map[string]bool{}
	var ids []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		runID := strings.TrimSpace(stringFromAny(line["runId"]))
		if runID == "" || runID == excludeRunID || !mainRuns[runID] ||
			strings.TrimSpace(stringFromAny(line["taskId"])) != "" || lineTaskAgentKey(line) != "" ||
			strings.TrimSpace(stringFromAny(line["lane"])) != "" {
			continue
		}
		var value any
		switch stringFromAny(line["_type"]) {
		case "query":
			if lineIsSystemInitQuery(line) {
				continue
			}
			value = line["references"] // Never fall back to the caller's query.
		case "steer":
			value = anyMap(line["steer"])["references"]
		default:
			continue
		}
		raw, err := json.Marshal(value)
		var refs []queryinput.Reference
		if err != nil || json.Unmarshal(raw, &refs) != nil {
			continue
		}
		for _, id := range queryinput.ResourceIDs(refs, "skill") {
			if seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
			if len(ids) == limit {
				return ids, nil
			}
		}
	}
	return ids, nil
}
