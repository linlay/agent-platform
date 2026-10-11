package chat

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

func TestAgentSteerSourcePersistenceReplaySearchAndModelInput(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.EnsureChat("chat", "agent", "original")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.OnRunStarted(RunStart{ChatID: "chat", RunID: "run", AgentKey: "agent", StartedAtMillis: testEpochMillis(1)}); err != nil {
		t.Fatal(err)
	}
	writer := NewStepWriter(store, "chat", "run", "GENERAL")
	writer.OnEvent(stream.EventData{Seq: 1, Type: "request.query", Timestamp: testEpochMillis(1), Payload: map[string]any{"chatId": "chat", "runId": "run", "role": "user", "message": "original"}})
	source := (&queryinput.SteerSource{Type: "agent", AgentKey: "caller", ChatID: "parent-chat", RunID: "parent-run", ToolID: "call"}).Payload()
	writer.OnEvent(stream.EventData{Seq: 2, Type: "request.steer", Timestamp: testEpochMillis(2), Payload: map[string]any{"chatId": "chat", "runId": "run", "steerId": "steer-id", "role": "user", "message": "unique revised goal", "source": source}})
	writer.Flush()
	if writer.Err() != nil {
		t.Fatal(writer.Err())
	}
	raw, err := os.ReadFile(store.chatJSONLPath("chat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateJSONLContent(string(raw), "chat.jsonl"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"source":`) {
		t.Fatal(string(raw))
	}
	detail, err := store.LoadChat("chat")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range detail.Events {
		if event.Type == "request.steer" {
			found = true
			if !reflect.DeepEqual(event.Value("source"), source) || event.String("role") != "user" {
				t.Fatal(event)
			}
		}
	}
	if !found {
		t.Fatal("steer missing from replay")
	}
	hits, err := store.SearchSession("chat", "unique revised", 20)
	if err != nil || len(hits) != 1 || !reflect.DeepEqual(hits[0].Meta["source"], source) || hits[0].Meta["steerId"] != "steer-id" {
		t.Fatalf("%#v %v", hits, err)
	}
	messages, err := store.LoadRawMessages("chat", 0)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(messages)
	if !strings.Contains(string(encoded), "unique revised goal") || strings.Contains(string(encoded), "parent-run") || strings.Contains(string(encoded), "caller") || strings.Contains(string(encoded), `"source"`) {
		t.Fatal(string(encoded))
	}
}

func TestSteerSourceSchemaRejectsMalformedMetadata(t *testing.T) {
	valid := map[string]any{"type": "agent", "agentKey": "caller", "chatId": "parent-chat", "runId": "parent-run", "toolId": "call"}
	for _, kind := range []string{"absent", "valid", "null", "wrong_type", "missing_id", "unknown_field"} {
		t.Run(kind, func(t *testing.T) {
			steer := map[string]any{"chatId": "chat", "runId": "run", "steerId": "id", "message": "goal", "role": "user"}
			source := map[string]any{}
			for k, v := range valid {
				source[k] = v
			}
			switch kind {
			case "absent":
			case "null":
				steer["source"] = nil
			case "wrong_type":
				source["type"] = "user"
				steer["source"] = source
			case "missing_id":
				delete(source, "toolId")
				steer["source"] = source
			case "unknown_field":
				source["subject"] = "alice"
				steer["source"] = source
			default:
				steer["source"] = source
			}
			line := map[string]any{"_type": "steer", "chatId": "chat", "runId": "run", "updatedAt": testEpochMillis(1), "steer": steer}
			data, _ := json.Marshal(line)
			err := ValidateJSONLContent(string(data), "chat.jsonl")
			wantValid := kind == "absent" || kind == "valid"
			if (err == nil) != wantValid {
				t.Fatalf("%s: %v", data, err)
			}
		})
	}
}
