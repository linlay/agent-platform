package chat

import (
	"encoding/json"
	"os"
	"testing"

	"agent-platform/internal/timecontract"
)

func TestPersistedTimeContractRejectsCompactAliasAndNestedFallbacks(t *testing.T) {
	valid := json.Number("1700000000000")
	for _, line := range []map[string]any{
		{"_type": CompactCheckpointLineType, "updatedAt": 0},
		{"_type": StepLineTypeReact, "updatedAt": valid, "messages": []any{map[string]any{"role": "assistant", "ts": 0}}},
		{"_type": "event", "updatedAt": valid, "event": map[string]any{"type": "content.delta", "timestamp": "1700000000000"}},
	} {
		if err := validatePersistedTimeContract([]map[string]any{line}, "chat.jsonl"); !timecontract.IsViolation(err) {
			t.Fatalf("expected violation for %#v, got %v", line, err)
		}
	}
}

func TestMessageAndStepSystemClonesPreserveTimeJSONIntegers(t *testing.T) {
	value := map[string]any{
		"timestamp": json.Number("1700000000001"),
		"content":   []any{map[string]any{"ts": json.Number("1700000000002")}},
	}
	for name, cloned := range map[string]map[string]any{
		"message": cloneMessageMap(value),
		"system":  cloneStepSystemPayload(value),
	} {
		if _, ok := cloned["timestamp"].(json.Number); !ok {
			t.Fatalf("%s clone timestamp type = %T, want json.Number", name, cloned["timestamp"])
		}
		content, _ := cloned["content"].([]any)
		item, _ := content[0].(map[string]any)
		if _, ok := item["ts"].(json.Number); !ok {
			t.Fatalf("%s clone nested ts type = %T, want json.Number", name, item["ts"])
		}
	}
}

func TestMissingPersistedMessageTsFailsRawAndReplayReads(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chatID = "chat-missing-message-ts"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatal(err)
	}
	line := `{"_type":"query","chatId":"` + chatID + `","runId":"run-missing-message-ts","updatedAt":1700000000001,"query":{"role":"user","message":"hello"},"messages":[{"role":"user","content":"hello"}]}` + "\n"
	if err := os.WriteFile(store.chatJSONLPath(chatID), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRawMessages(chatID, 1); !timecontract.IsViolation(err) {
		t.Fatalf("expected raw-history missing ts violation, got %v", err)
	}
	if _, err := store.LoadChat(chatID); !timecontract.IsViolation(err) {
		t.Fatalf("expected replay missing ts violation, got %v", err)
	}
}

func TestReplayUsesAuthoritativeRunLifecycleTimes(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chatID, runID = "chat-lifecycle-contract", "run-lifecycle-contract"
	startedAt := testEpochMillis(100)
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := store.OnRunStarted(RunStart{ChatID: chatID, RunID: runID, AgentKey: "agent-a", StartedAtMillis: startedAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{Type: "query", ChatID: chatID, RunID: runID, UpdatedAt: testEpochMillis(500), Query: map[string]any{"role": "user", "message": "hello"}}); err != nil {
		t.Fatal(err)
	}
	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	foundStart := false
	for _, event := range detail.Events {
		if event.Type == "run.start" && event.String("runId") == runID {
			foundStart = event.Timestamp == startedAt
		}
		if event.Type == "run.complete" && event.String("runId") == runID {
			t.Fatalf("active run must not synthesize completion: %#v", detail.Events)
		}
	}
	if !foundStart {
		t.Fatalf("missing authoritative run.start: %#v", detail.Events)
	}
	if runs, err := store.ListRuns(chatID); err != nil || len(runs) != 0 {
		t.Fatalf("active run leaked through public runs: %#v err=%v", runs, err)
	}
	completedAt := testEpochMillis(900)
	if err := store.OnRunCompleted(RunCompletion{ChatID: chatID, RunID: runID, AgentKey: "agent-a", StartedAtMillis: startedAt, UpdatedAtMillis: completedAt}); err != nil {
		t.Fatal(err)
	}
	if runs, err := store.ListRuns(chatID); err != nil || len(runs) != 1 || runs[0].StartedAt != startedAt || runs[0].CompletedAt != completedAt {
		t.Fatalf("completion did not preserve start: %#v err=%v", runs, err)
	}
}

func TestLoadRunStartedAtRejectsMissingAndInvalidLifecycleRows(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const chatID, runID = "chat-lifecycle-reader", "run-lifecycle-reader"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRunStartedAt(chatID, runID); !timecontract.IsViolation(err) {
		t.Fatalf("missing lifecycle start error = %v, want time contract violation", err)
	}

	startedAt := testEpochMillis(100)
	if err := store.OnRunStarted(RunStart{ChatID: chatID, RunID: runID, AgentKey: "agent-a", StartedAtMillis: startedAt}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadRunStartedAt(chatID, runID); err != nil || got != startedAt {
		t.Fatalf("load lifecycle start = %d, %v; want %d", got, err, startedAt)
	}
	if _, err := store.db.Exec(`UPDATE RUNS SET STARTED_AT_=0 WHERE RUN_ID_=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRunStartedAt(chatID, runID); !timecontract.IsViolation(err) {
		t.Fatalf("invalid lifecycle start error = %v, want time contract violation", err)
	}
}

func TestWriteAndBTWBoundariesScopeTimeValidationToPlatformFields(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chatID = "chat-write-contract"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{Type: "query", ChatID: chatID, RunID: "run-bad", UpdatedAt: 0, Query: map[string]any{"role": "user", "message": "hello"}}); !timecontract.IsViolation(err) {
		t.Fatalf("expected invalid write rejection, got %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{Type: "query", ChatID: chatID, RunID: "run-opaque", UpdatedAt: testEpochMillis(1), Query: map[string]any{"role": "user", "timestamp": float64(testEpochMillis(1))}}); err != nil {
		t.Fatalf("opaque query business field should not be interpreted as time: %v", err)
	}
	if err := os.WriteFile(store.chatJSONLPath(chatID), []byte(`{"_type":"compact.checkpoint","chatId":"`+chatID+`","updatedAt":0}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBTWBranch(chatID, "btw-contract"); !timecontract.IsViolation(err) {
		t.Fatalf("expected invalid parent rejection, got %v", err)
	}
}

func TestDeepClonePreservesSourceTimestampAsJSONInteger(t *testing.T) {
	value := map[string]any{
		"sources": []any{map[string]any{"timestamp": json.Number("1700000000001")}},
	}
	cloned := cloneMapDeep(value)
	sources, _ := cloned["sources"].([]any)
	item, _ := sources[0].(map[string]any)
	if _, ok := item["timestamp"].(json.Number); !ok {
		t.Fatalf("source timestamp type = %T, want json.Number", item["timestamp"])
	}
	if _, err := validateJSONLLinePayload(map[string]any{
		"_type":     StepLineTypeReactTool,
		"updatedAt": int64(1_700_000_000_002),
		"sources":   map[string]any{"items": []any{item}},
	}, "chat.jsonl.write"); err != nil {
		t.Fatalf("valid source timestamp rejected after clone: %v", err)
	}
}
