package chat

import "testing"

func TestMailHeaderSurvivesHistoryButNotReplay(t *testing.T) {
	line := map[string]any{"_type": "query", "runId": "r", "mailId": "m", "mailKind": "letter", "mailHeader": "[Letter m]\n", "messages": []any{map[string]any{"role": "user", "content": "work"}}}
	raw := rawMessagesFromJSONLLines([]map[string]any{line})
	if len(raw) != 1 || raw[0]["_mailHeader"] != "[Letter m]\n" {
		t.Fatal(raw)
	}
	if payload := requestPayloadFromLine(line); payload["mailHeader"] != nil {
		t.Fatal("private header leaked", payload)
	}
}
