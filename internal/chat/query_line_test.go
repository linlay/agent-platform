package chat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

func writeClientQueryForTest(t *testing.T, store *FileStore, chatID string, raw string, payload map[string]any) map[string]any {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "agent", "hello"); err != nil {
		t.Fatal(err)
	}
	writer := NewStepWriter(store, chatID, "run-1", "GENERAL")
	writer.SetOriginalQuery(json.RawMessage(raw))
	onEventForTest(writer, stream.EventData{Seq: 1, Type: "request.query", Timestamp: testEpochMillis(1), Payload: payload})
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil || len(lines) != 1 {
		t.Fatalf("read query line: %#v %v", lines, err)
	}
	return lines[0]
}

func TestQueryLineKeepsCallerInputAndPlatformDecisionsApart(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const raw = `{"message":"hello","stream":false,"detached":null,"planningMode":false,"references":[],"params":{"big":9007199254740993},"sourceUser":"remote"}`
	line := writeClientQueryForTest(t, store, "chat-raw", raw, map[string]any{
		"requestId": "run-1", "runId": "run-1", "chatId": "chat-raw", "agentKey": "agent", "message": "hello",
		"params": map[string]any{"big": json.Number("9007199254740993")}, "accessLevel": "default", "editingMode": true,
		"model":  map[string]any{"key": "model-a"},
		"origin": (&queryinput.Origin{Type: queryinput.OriginUser, UserID: "alice"}).Payload(),
	})

	var want map[string]any
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(line["query"], want) {
		t.Fatalf("query must be the request exactly as received\n got: %#v\nwant: %#v", line["query"], want)
	}
	persisted, err := store.LoadJSONLContent("chat-raw")
	if err != nil || !strings.Contains(persisted, `"big":9007199254740993`) {
		t.Fatalf("large integers keep their exact digits: %v %s", err, persisted)
	}
	for key, value := range map[string]any{"requestId": "run-1", "agentKey": "agent", "accessLevel": "default", "editingMode": true} {
		if !reflect.DeepEqual(line[key], value) {
			t.Fatalf("top-level %s = %#v, want %#v in %#v", key, line[key], value, line)
		}
	}
	if anyMap(line["origin"])["userId"] != "alice" || anyMap(line["model"])["key"] != "model-a" {
		t.Fatalf("missing origin or model beside the query: %#v", line)
	}
	for _, key := range []string{"role", "kind", "lane", "resolvedQuery"} {
		if _, ok := line[key]; ok {
			t.Fatalf("query line must not record %s: %#v", key, line)
		}
	}

	payload := requestPayloadFromLine(line)
	if payload["message"] != "hello" || payload["agentKey"] != "agent" || payload["editingMode"] != true || payload["accessLevel"] != "default" {
		t.Fatalf("projection lost platform decisions: %#v", payload)
	}
	for _, key := range []string{"stream", "detached", "sourceUser", "planningMode"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("projection must not replay transport field %s: %#v", key, payload)
		}
	}
	if text, ok := lineUserMessage(line); !ok || text != "hello" {
		t.Fatalf("a user's query is memory evidence, got %q %v", text, ok)
	}
}

func TestQueryLineOriginDecidesMemoryAndSearch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		origin     *queryinput.Origin
		memory     bool
		searchable bool
	}{
		{"user", &queryinput.Origin{Type: queryinput.OriginUser}, true, true},
		{"agent", &queryinput.Origin{Type: queryinput.OriginAgent, AgentKey: "caller", ChatID: "parent", RunID: "parent-run", ToolID: "call"}, false, true},
		{"automation", &queryinput.Origin{Type: queryinput.OriginAutomation, AutomationID: "daily"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			chatID := "chat-origin-" + tc.name
			line := writeClientQueryForTest(t, store, chatID, `{"message":"needle text"}`, map[string]any{
				"requestId": "run-1", "runId": "run-1", "chatId": chatID, "message": "needle text", "origin": tc.origin.Payload(),
			})
			if _, ok := lineUserMessage(line); ok != tc.memory {
				t.Fatalf("memory evidence = %v, want %v", ok, tc.memory)
			}
			hits, err := store.SearchSession(chatID, "needle", 10)
			if err != nil {
				t.Fatal(err)
			}
			if (len(hits) > 0) != tc.searchable {
				t.Fatalf("searchable = %v, want %v: %#v", len(hits) > 0, tc.searchable, hits)
			}
		})
	}
}

func TestSideLaneQueryLineRecordsOnlyTheLane(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	line := writeClientQueryForTest(t, store, "chat-side", `{"message":"why","sideId":"side_1"}`, map[string]any{
		"requestId": "run-1", "runId": "run-1", "chatId": "chat-side", "message": "why",
		"lane": SideLaneExplain, "sideId": "side_1", "hidden": true,
	})
	if line["lane"] != SideLaneExplain {
		t.Fatalf("expected the explain lane beside the query: %#v", line)
	}
	// The side lane's platform-decided visibility is stored like any other
	// computed value; the branch identity lives in the file name.
	if line["hidden"] != true {
		t.Fatalf("side query line must record its effective visibility: %#v", line)
	}
	for _, key := range []string{"sideId", "btwId", "parentChatId", "kind"} {
		if _, ok := line[key]; ok {
			t.Fatalf("side query line must not record %s: %#v", key, line)
		}
	}
	if payload := requestPayloadFromLine(line); payload["hidden"] != true || payload["lane"] != SideLaneExplain {
		t.Fatalf("side lane replays hidden: %#v", payload)
	}
}

func TestQueryLineStoresAutomationHiddenDefault(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{`{"message":"nightly"}`, true},
		{`{"message":"nightly","hidden":null}`, true},
		{`{"message":"nightly","hidden":false}`, false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			line := writeClientQueryForTest(t, store, "automation-hidden", tc.input, map[string]any{
				"requestId": "run-1", "message": "nightly", "hidden": tc.want,
				"origin": (&queryinput.Origin{Type: queryinput.OriginAutomation}).Payload(),
			})
			// The platform default is stored, not recomputed when reading.
			if stored, _ := line["hidden"].(bool); stored != tc.want {
				t.Fatalf("stored hidden=%#v, live hidden=%v", line["hidden"], tc.want)
			}
			if got, _ := requestPayloadFromLine(line)["hidden"].(bool); got != tc.want {
				t.Fatalf("replayed hidden=%#v, live hidden=%v", got, tc.want)
			}
		})
	}
}

func TestSystemSnapshotQueryLineHasNoInput(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsureChat("chat-snapshot", "agent", "hello"); err != nil {
		t.Fatal(err)
	}
	writer := NewStepWriter(store, "chat-snapshot", "run-1", "GENERAL")
	writer.SetOriginalQuery(json.RawMessage(`{"message":"hello"}`))
	onEventForTest(writer, stream.EventData{Seq: 1, Type: "request.query", Timestamp: testEpochMillis(1), Payload: map[string]any{
		"requestId": "run-1", "runId": "run-1", "chatId": "chat-snapshot", "message": "hello",
	}})
	onEventForTest(writer, stream.EventData{Seq: 2, Type: "request.query", Timestamp: testEpochMillis(2), Payload: map[string]any{
		"requestId": "run-1", "runId": "run-1", "chatId": "chat-snapshot",
		"system": map[string]any{
			"agentKey": "agent", "cacheKey": "react:execute", "fingerprint": "sha256:x",
			"systemMessage": map[string]any{"role": "system", "content": "system"}, "tools": []any{},
		},
	}})
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	lines, err := readJSONLines(store.chatJSONLPath("chat-snapshot"))
	if err != nil || len(lines) != 2 {
		t.Fatalf("read lines: %#v %v", lines, err)
	}
	if _, ok := lines[1]["query"]; ok || !lineIsSystemInitQuery(lines[1]) || lineIsSystemInitQuery(lines[0]) {
		t.Fatalf("a system snapshot is recognised by carrying no query: %#v", lines)
	}
	if stream.IsClientVisibleEventData(stream.EventData{Type: "request.query", Payload: map[string]any{"system": map[string]any{}}}) {
		t.Fatal("system snapshot events stay storage-only")
	}
}

func TestLegacyQueryLinesKeepReading(t *testing.T) {
	legacy := map[string]any{"_type": "query", "chatId": "chat", "runId": "run", "query": map[string]any{
		"requestId": "req", "runId": "run", "chatId": "chat", "role": "automation", "message": "nightly", "accessLevel": "auto_approve",
	}}
	// Older lines kept decisions inside query; they are shown, not resumed, so
	// only what display needs is read from there.
	payload := requestPayloadFromLine(legacy)
	if payload["message"] != "nightly" || payload["requestId"] != "req" || anyMap(payload["origin"])["type"] != "automation" {
		t.Fatalf("legacy query must stay displayable: %#v", payload)
	}
	if payload["accessLevel"] != "auto_approve" {
		t.Fatalf("clients restore the access level of an older Run: %#v", payload)
	}
	if _, ok := requestPayloadFromLine(map[string]any{"_type": "query", "query": map[string]any{"message": "plan", "planningMode": true}})["planningMode"]; ok {
		t.Fatal("decisions that steer a resumed Run are read only from the top level")
	}
	if origin := lineOrigin(legacy); origin == nil || origin.Type != queryinput.OriginAutomation {
		t.Fatalf("legacy automation role maps to its origin: %#v", origin)
	}
	if lineRequestID(legacy) != "req" {
		t.Fatal("legacy requestId lives inside query")
	}
	agentLine := map[string]any{"_type": "query", "query": map[string]any{"message": "child", "runOrigin": map[string]any{"agentKey": "caller"}}}
	if _, ok := lineUserMessage(agentLine); ok {
		t.Fatal("legacy chat_start input is not user memory evidence")
	}
	snapshot := map[string]any{"_type": "query", "query": map[string]any{"kind": "system-init", "role": "system"}}
	if !lineIsSystemInitQuery(snapshot) {
		t.Fatal("legacy system-init lines are still snapshots")
	}
}

func TestQueryLineFromMapPreservesSnapshots(t *testing.T) {
	line := queryLineFromMap(map[string]any{
		"requestId": "run", "runId": "run", "query": map[string]any{"message": "hello"},
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"system": map[string]any{
			"agentKey": "agent", "cacheKey": "react:execute", "fingerprint": "sha256:x",
			"systemMessage": map[string]any{"role": "system", "content": "system"}, "tools": []any{},
		},
	}, "chat")
	if len(line.Messages) != 1 || line.Messages[0]["content"] != "hello" || line.System == nil || line.System.Fingerprint != "sha256:x" {
		t.Fatalf("query trace lost messages or system snapshot: %#v", line)
	}
}
