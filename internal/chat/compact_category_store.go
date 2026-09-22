package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func (s *FileStore) commitL1Policies(chatID, id string, records []jsonLineRecord, data []byte, policies map[int]map[string]bool) error {
	if strings.TrimSpace(id) == "" {
		return ErrNoCompactableHistory
	}
	var out bytes.Buffer
	for i, record := range records {
		raw := record.Raw
		if keep, ok := policies[i]; ok {
			line := cloneJSONLineMap(record.Value)
			line["_compact"] = compactMarker("L1", id, keep)
			var err error
			raw, err = json.Marshal(line)
			if err != nil {
				return err
			}
		}
		out.Write(bytes.TrimSpace(raw))
		out.WriteByte('\n')
	}
	return replaceChatJSONLWithValidatedCompact(s.chatJSONLPath(chatID), filepath.Join(s.ChatDir(chatID), ".compact-backups"), id, data, out.Bytes())
}

// Compare semantic categories, not storage message boundaries: the stream can
// persist reasoning and content separately although providers combine them.
type compactAtom struct{ key, category string }

func compactMessageAtoms(m map[string]any) []compactAtom {
	var atoms []compactAtom
	add := func(category string, value any) {
		raw, _ := json.Marshal(value)
		atoms = append(atoms, compactAtom{category + ":" + string(raw), category})
	}
	role := stringFromAny(m["role"])
	if role == "tool" {
		add("tool", map[string]any{"id": compactToolResultID(m), "content": strings.TrimSpace(anyCompactText(m["content"]))})
		return atoms
	}
	if hasCompactContent(m["content"]) {
		content := compactIdentityContent(m["content"])
		add("content", map[string]any{"role": role, "content": content})
	}
	if text := strings.TrimSpace(anyCompactText(m["reasoning_content"])); text != "" {
		add("reasoning", text)
	}
	for _, call := range anyMessageSlice(m["tool_calls"]) {
		add("tool", call)
	}
	return atoms
}

func (s *FileStore) commitRunSummary(chatID string, line RunCompactCheckpointLine, records []jsonLineRecord, data []byte) error {
	if len(line.Messages) == 0 {
		return ErrNoCompactableHistory
	}
	summary := anyCompactText(line.Messages[0]["content"])
	prefix := "以下是此前对话的上下文压缩摘要。它替代所有已标记 _compact 的历史。\n\n"
	if !strings.HasPrefix(summary, prefix) {
		return fmt.Errorf("run summary checkpoint must start with summary")
	}
	retained := map[string]int{}
	candidates := map[string]int{}
	for _, m := range line.Messages[1:] {
		for _, atom := range compactMessageAtoms(m) {
			retained[atom.key]++
		}
	}
	for _, m := range line.CompactCoveredMessages {
		for _, atom := range compactMessageAtoms(m) {
			candidates[atom.key]++
		}
	}
	if len(candidates) == 0 {
		return ErrNoCompactableHistory
	}
	boundary := len(records)
	covered := map[int]bool{}
	effectiveStart := 0
	for i, r := range records {
		if !lineIsCompacted(r.Value) && len(anyMessageSlice(r.Value["messages"])) > 0 && (r.Value["_type"] == RunCompactCheckpointLineType || r.Value["_type"] == CompactCheckpointLineType) {
			effectiveStart = i
		}
	}
	for i := len(records) - 1; i >= effectiveStart; i-- {
		source := records[i].Value
		if lineIsCompacted(source) || stringFromAny(source["taskSubAgentKey"]) != "" || lineIsSystemInitQuery(source) {
			continue
		}
		kind := stringFromAny(source["_type"])
		if kind == "event" || kind == "submit" || kind == ToolCompactLineType {
			continue
		}
		messages := rawMessagesFromJSONLLines([]map[string]any{source})
		kept, selected, unknown := 0, 0, 0
		for _, m := range messages {
			for _, atom := range compactMessageAtoms(m) {
				if retained[atom.key] > 0 {
					retained[atom.key]--
					kept++
				} else if candidates[atom.key] > 0 {
					candidates[atom.key]--
					selected++
				} else if atom.category != "reasoning" {
					unknown++
				}
			}
		}
		if kept > 0 && selected > 0 || selected > 0 && unknown > 0 {
			return fmt.Errorf("summary boundary splits a persisted model round")
		}
		if kept > 0 {
			boundary = i
		} else if selected > 0 {
			covered[i] = true
		}
	}
	for _, remaining := range retained {
		if remaining > 0 {
			return ErrCompactHistoryChanged
		}
	}
	for _, remaining := range candidates {
		if remaining > 0 {
			return ErrCompactHistoryChanged
		}
	}
	if len(covered) == 0 {
		return ErrNoCompactableHistory
	}
	// A covered replacement snapshot must never reveal its superseded originals.
	if covered[effectiveStart] {
		for i := 0; i < effectiveStart; i++ {
			if len(rawMessagesFromJSONLLines([]map[string]any{records[i].Value})) > 0 {
				covered[i] = true
			}
		}
	}
	checkpoint := CompactCheckpointLine{Type: CompactCheckpointLineType, Version: 3, ChatID: chatID, CompactID: line.CompactID, UpdatedAt: line.UpdatedAt, Trigger: line.Trigger, Summary: strings.TrimPrefix(summary, prefix), SummarySource: line.SummarySource, PreCompactEstimatedTokens: line.PreCompactEstimatedTokens, PostCompactEstimatedTokens: line.PostCompactEstimatedTokens, CompressionRatio: line.CompressionRatio, RemainingRatio: line.RemainingRatio, ReleasedRatio: line.ReleasedRatio, TokensFreed: line.TokensFreed, CompactionUsage: line.CompactionUsage}
	raw, err := validateJSONLLinePayload(checkpoint, "chat.jsonl.summary.write")
	if err != nil {
		return err
	}
	var out bytes.Buffer
	for i, record := range records {
		if i == boundary {
			out.Write(raw)
			out.WriteByte('\n')
		}
		original := record.Raw
		if covered[i] {
			value := cloneJSONLineMap(record.Value)
			value["_compact"] = compactMarker("L2", line.CompactID, nil)
			original, err = json.Marshal(value)
			if err != nil {
				return err
			}
		}
		out.Write(bytes.TrimSpace(original))
		out.WriteByte('\n')
	}
	if boundary == len(records) {
		out.Write(raw)
		out.WriteByte('\n')
	}
	return replaceChatJSONLWithValidatedCompact(s.chatJSONLPath(chatID), filepath.Join(s.ChatDir(chatID), ".compact-backups"), line.CompactID, data, out.Bytes())
}

func compactIdentityContent(value any) any {
	if parts, ok := value.([]any); ok {
		for _, part := range parts {
			if m, ok := part.(map[string]any); ok && stringFromAny(m["type"]) != "text" {
				return value
			}
		}
	}
	if text := anyCompactText(value); text != "" {
		return strings.TrimSpace(text)
	}
	return value
}
