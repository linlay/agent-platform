package types

import (
	"encoding/json"
	"testing"
)

func TestExecuteResultKeepsEmptySuccessObject(t *testing.T) {
	data, err := json.Marshal(ExecuteResult{ChatID: "chat-1", Status: "success", Output: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["output"].(map[string]any); !ok {
		t.Fatalf("required object was omitted: %s", data)
	}
	data, err = json.Marshal(ExecuteResult{ChatID: "chat-1", Status: "failure", Reason: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	// Decode into a fresh map because Unmarshal otherwise preserves previous keys.
	payload = nil
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["output"]; exists {
		t.Fatalf("failure gained output: %s", data)
	}
}
