package chat

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDecodeJSONLRecordsAcceptsCurrentPhysicalLineSyntax(t *testing.T) {
	content := strings.Join([]string{
		`{"_type":"query","chatId":"chat-1","runId":"run-1","updatedAt":1700000000001,"query":{"role":"user","message":"hello"}}`,
		`{"_type":"react","chatId":"chat-1","runId":"run-1","updatedAt":1700000000002,"stage":"execute","messages":[]}`,
	}, "\r\n")

	records, err := decodeJSONLRecords([]byte(content), "chat.jsonl", true)
	if err != nil {
		t.Fatalf("decode current JSONL: %v", err)
	}
	if len(records) != 2 || stringFromAny(records[0].Value["_type"]) != "query" || stringFromAny(records[1].Value["stage"]) != "execute" {
		t.Fatalf("unexpected records %#v", records)
	}
	if err := ValidateJSONLContent(content, "chat.jsonl"); err != nil {
		t.Fatalf("validate CRLF JSONL without final newline: %v", err)
	}
	if err := ValidateJSONLContent(content+"\r\n", "chat.jsonl"); err != nil {
		t.Fatalf("validate CRLF JSONL with final newline: %v", err)
	}
}

func TestDecodeJSONLRecordsRejectsInvalidPhysicalLineSyntax(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		location string
		actual   string
	}{
		{name: "blank file line", content: "\n", location: "chat.jsonl[1]", actual: "blank"},
		{name: "blank line between records", content: validJSONLQueryLine("chat-1") + "\n\n" + validJSONLQueryLine("chat-1"), location: "chat.jsonl[2]", actual: "blank"},
		{name: "multi-line object", content: "{\n\"_type\":\"query\"\n}", location: "chat.jsonl[1]", actual: "invalid_json"},
		{name: "two objects on one line", content: validJSONLQueryLine("chat-1") + " " + validJSONLQueryLine("chat-1"), location: "chat.jsonl[1]", actual: "multiple_values"},
		{name: "array", content: `[{"_type":"query"}]`, location: "chat.jsonl[1]", actual: "[]interface {}"},
		{name: "scalar", content: `42`, location: "chat.jsonl[1]", actual: "json.Number"},
		{name: "syntax error", content: `{"_type":"query"`, location: "chat.jsonl[1]", actual: "invalid_json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeJSONLRecords([]byte(tc.content), "chat.jsonl", true)
			assertJSONLSchemaViolation(t, err, tc.location)
			data := JSONLSchemaErrorData(err)
			if data["actual"] != tc.actual {
				t.Fatalf("actual=%#v, want %q; error=%v", data["actual"], tc.actual, err)
			}
		})
	}
}

func TestDecodeJSONLRecordsRejectsUnsupportedSchema(t *testing.T) {
	cases := []struct {
		name    string
		content string
		field   string
	}{
		{name: "missing type", content: `{"updatedAt":1700000000001}`, field: "_type"},
		{name: "top-level type fallback", content: `{"type":"query","updatedAt":1700000000001}`, field: "_type"},
		{name: "plan execute line", content: `{"_type":"plan-execute","updatedAt":1700000000001}`, field: "_type"},
		{name: "step line", content: `{"_type":"step","updatedAt":1700000000001}`, field: "_type"},
		{name: "standalone system init", content: `{"_type":"system-init","createdAt":1700000000001}`, field: "_type"},
		{name: "unknown type", content: `{"_type":"future","updatedAt":1700000000001}`, field: "_type"},
		{name: "query systems", content: `{"_type":"query","updatedAt":1700000000001,"systems":[]}`, field: "system"},
		{name: "step systems", content: `{"_type":"react","updatedAt":1700000000001,"messages":[],"systems":[]}`, field: "system"},
		{name: "step inline system", content: `{"_type":"react","updatedAt":1700000000001,"messages":[],"system":{}}`, field: "system"},
		{name: "query system ref", content: `{"_type":"query","updatedAt":1700000000001,"systemRef":{}}`, field: "system"},
		{name: "imprecise step system ref", content: `{"_type":"react","updatedAt":1700000000001,"messages":[],"systemRef":{"agentKey":"a","cacheKey":"react:main","fingerprint":"sha256:x","mode":"react"}}`, field: "system"},
		{name: "awaiting event", content: `{"_type":"event","updatedAt":1700000000001,"event":{"type":"awaiting.ask","timestamp":1700000000001}}`, field: "event.type"},
		{name: "planning snapshot event", content: `{"_type":"event","updatedAt":1700000000001,"event":{"type":"planning.snapshot","timestamp":1700000000001}}`, field: "event.type"},
		{name: "query awaiting", content: `{"_type":"query","updatedAt":1700000000001,"awaiting":[]}`, field: "awaiting"},
		{name: "plan awaiting mode", content: `{"_type":"react","updatedAt":1700000000001,"messages":[],"awaiting":[{"type":"awaiting.ask","mode":"plan","timestamp":1700000000001}]}`, field: "awaiting[0].mode"},
		{name: "planning id fallback", content: `{"_type":"react","updatedAt":1700000000001,"messages":[],"awaiting":[{"type":"awaiting.ask","mode":"planning","timestamp":1700000000001,"planning":{"id":"p1","planningFile":"/tmp/p1.md"}}]}`, field: "awaiting[0].planning.planningId"},
		{name: "planning file inference", content: `{"_type":"react","updatedAt":1700000000001,"messages":[],"awaiting":[{"type":"awaiting.ask","mode":"planning","timestamp":1700000000001,"planning":{"planningId":"p1"}}]}`, field: "awaiting[0].planning.planningFile"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeJSONLRecords([]byte(tc.content), "chat.jsonl", true)
			assertJSONLSchemaViolation(t, err, "chat.jsonl[1]")
			if got := JSONLSchemaErrorData(err)["field"]; got != tc.field {
				t.Fatalf("field=%#v, want %q; error=%v", got, tc.field, err)
			}
		})
	}
}

func TestCurrentJSONLSchemaRejectsInvalidDataAcrossActiveReaders(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const chatID = "chat-schema-readers"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := completeRunForTest(store, RunCompletion{
		ChatID:          chatID,
		RunID:           "run-1",
		AgentKey:        "agent-a",
		InitialMessage:  "hello",
		AssistantText:   "answer",
		FinishReason:    "complete",
		UpdatedAtMillis: testEpochMillis(2),
	}); err != nil {
		t.Fatal(err)
	}
	invalid := validJSONLQueryLine(chatID) + "\n" + `{"type":"react","chatId":"` + chatID + `","runId":"run-1","updatedAt":1700000000002,"messages":[]}` + "\n"
	if err := os.WriteFile(store.chatJSONLPath(chatID), []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := map[string]func() error{
		"jsonl":       func() error { _, err := store.LoadJSONLContent(chatID); return err },
		"raw history": func() error { _, err := store.LoadRawMessages(chatID, 10); return err },
		"replay":      func() error { _, err := store.LoadChat(chatID); return err },
		"search":      func() error { _, err := store.SearchSession(chatID, "hello", 10); return err },
		"compact":     func() error { _, err := store.BuildCompactSnapshot(chatID, 1); return err },
		"btw":         func() error { _, err := store.CreateBTWBranch(chatID, "schema_check"); return err },
		"derive": func() error {
			_, err := store.DeriveChat(DeriveChatRequest{SourceChatID: chatID, SourceRunID: "run-1", ChatID: "chat-schema-derived"})
			return err
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			err := check()
			assertJSONLSchemaViolation(t, err, "chat.jsonl[2]")
		})
	}
}

func TestJSONLSchemaErrorDataDoesNotExposeRecordContent(t *testing.T) {
	const secret = "do-not-expose-system-prompt"
	_, err := decodeJSONLRecords([]byte(`{"type":"query","systemPrompt":"`+secret+`"}`), "chat.jsonl", true)
	assertJSONLSchemaViolation(t, err, "chat.jsonl[1]")
	data := JSONLSchemaErrorData(err)
	if data["code"] != ChatStorageSchemaViolationCode || data["status"] != 422 || data["retryable"] != false {
		t.Fatalf("unexpected public error data %#v", data)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed record content: %v", err)
	}
	for _, value := range data {
		if strings.Contains(stringFromAny(value), secret) {
			t.Fatalf("public error data exposed record content: %#v", data)
		}
	}
}

func validJSONLQueryLine(chatID string) string {
	return `{"_type":"query","chatId":"` + chatID + `","runId":"run-1","updatedAt":1700000000001,"query":{"role":"user","message":"hello"}}`
}

func assertJSONLSchemaViolation(t *testing.T, err error, location string) {
	t.Helper()
	if !IsJSONLSchemaViolation(err) {
		t.Fatalf("expected JSONL schema violation, got %v", err)
	}
	var violation *JSONLSchemaViolation
	if !errors.As(err, &violation) {
		t.Fatalf("schema violation has unexpected type %T", err)
	}
	if violation.Location != location {
		t.Fatalf("location=%q, want %q; error=%v", violation.Location, location, err)
	}
}
