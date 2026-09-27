package llm

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/modelresponses"
)

// Some compatible endpoints end a successful stream with an empty output list.
// Complete output_item.done snapshots are sufficient only if every observed
// item is done and the indices form a dense list. Partial terminal snapshots,
// incomplete responses and delta-only items never use this fallback. The caller
// still validates all item identities, payloads and deltas before committing.
func responsesOutputFromDone(response modelresponses.Response, state *responsesTurnState) (modelresponses.Response, bool) {
	if response.Status != "completed" || len(response.Output) != 0 || len(state.items) == 0 {
		return response, false
	}
	output := make([]modelresponses.Item, 0, len(state.items))
	for index := 0; index < len(state.items); index++ {
		item, ok := state.items[index]
		if !ok || !state.done[index] || (item.Status != "" && item.Status != "completed") {
			return response, false
		}
		output = append(output, item)
	}
	// A delta referencing an item outside this complete list remains a hard
	// error in validateResponsesFinal, rather than being dropped or executed.
	response.Output = output
	return response, true
}

func responsesSummaryText(item modelresponses.Item) string {
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(item.Summary, &parts)
	text := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "\n\n")
}
