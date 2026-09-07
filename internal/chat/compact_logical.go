package chat

func previousEffectiveCompactID(records []jsonLineRecord) string {
	for i := len(records) - 1; i >= 0; i-- {
		line := records[i].Value
		if lineIsCompacted(line) {
			continue
		}
		switch line["_type"] {
		case RunCompactCheckpointLineType, CompactCheckpointLineType, ToolCompactLineType:
			return stringFromAny(line["compactId"])
		}
	}
	return ""
}

// Once a checkpoint has replaced original steps, selecting another compact
// prefix must operate on that logical context. Replaying the original tail
// would resurrect content removed by an earlier run checkpoint.
func logicalCompactSnapshot(chatID string, records []jsonLineRecord, data []byte, keep int, terminal map[string]bool) (CompactSnapshot, error) {
	messages := rawMessagesFromJSONLLines(recordValues(records))
	var order []string
	seen := map[string]bool{}
	for _, message := range messages {
		id := stringFromAny(message["runId"])
		if id != "" && terminal[id] && !seen[id] {
			order = append(order, id)
			seen[id] = true
		}
	}
	keep = min(max(0, keep), max(0, len(order)-1))
	retained := map[string]bool{}
	for _, id := range order[len(order)-keep:] {
		retained[id] = true
	}
	var covered, tail []map[string]any
	for _, message := range messages {
		id := stringFromAny(message["runId"])
		if retained[id] || (id != "" && !terminal[id]) {
			tail = append(tail, message)
		} else {
			covered = append(covered, message)
		}
	}
	if len(covered) == 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	count := 0
	for _, record := range records {
		if !lineIsCompacted(record.Value) {
			count++
		}
	}
	return CompactSnapshot{
		LogicalSnapshot: true, ChatID: chatID, FileHash: jsonlContentHash(data),
		InsertAfterIndex: len(records) - 1, CoveredLineCount: count,
		ProjectedMessageCount: len(covered), PreCompactEstimatedTokens: EstimateRawMessageTokens(messages),
		PostCompactEstimatedTokens: EstimateRawMessageTokens(tail),
		CoveredMessages:            covered, TailMessages: tail, Prompt: buildCompactPrompt(covered),
	}, nil
}
