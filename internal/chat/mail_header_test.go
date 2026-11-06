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

func TestQueryLineKeepsMailFieldsOnlyForAgentMail(t *testing.T) {
	user := queryLineDecisionsFromPayload(map[string]any{"requestId": "req", "mailOrigin": "user", "mailId": "m1", "mailKind": "letter", "mailAttempt": 1, "mailHop": 0})
	if user.MailInput != nil || user.RequestID != "req" {
		t.Fatalf("user message kept mail fields: %+v", user)
	}
	agent := queryLineDecisionsFromPayload(map[string]any{"mailOrigin": "agent", "mailId": "m2", "mailKind": "letter", "mailAttempt": 1, "mailHop": 1})
	if agent.MailInput == nil || agent.MailInput.ID != "m2" || agent.MailInput.Hop != 1 {
		t.Fatalf("agent mail lost its fields: %+v", agent)
	}
}
