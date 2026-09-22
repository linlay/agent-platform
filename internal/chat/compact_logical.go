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
	// Old snapshots are indivisible physical records. Summarize the whole snapshot
	// rather than copy its retained messages into another snapshot.
	last := -1
	for i, r := range records {
		if !lineIsCompacted(r.Value) && len(anyMessageSlice(r.Value["messages"])) > 0 && (r.Value["_type"] == RunCompactCheckpointLineType || r.Value["_type"] == CompactCheckpointLineType) {
			last = i
		}
	}
	if last < 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	boundary := len(records)
	order, first := activeRootRunOrder(records[last+1:], terminal)
	keep = min(max(0, keep), len(order))
	if keep > 0 {
		boundary = last + 1 + first[order[len(order)-keep]]
	}
	covered := rawMessagesFromJSONLLines(recordValues(records[:boundary]))
	tail := rawMessagesFromJSONLLines(recordValues(records[boundary:]))
	for _, m := range covered {
		if id := stringFromAny(m["runId"]); id != "" && !terminal[id] {
			return CompactSnapshot{}, ErrNoCompactableHistory
		}
	}
	if len(covered) == 0 {
		return CompactSnapshot{}, ErrNoCompactableHistory
	}
	return CompactSnapshot{ChatID: chatID, FileHash: jsonlContentHash(data), InsertAfterIndex: boundary - 1, CoveredLineCount: boundary, ProjectedMessageCount: len(covered), PreCompactEstimatedTokens: EstimateRawMessageTokens(rawMessagesFromJSONLLines(recordValues(records))), PostCompactEstimatedTokens: EstimateRawMessageTokens(tail), CoveredMessages: covered, TailMessages: tail, Prompt: buildCompactPrompt(covered)}, nil
}
