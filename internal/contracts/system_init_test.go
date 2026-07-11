package contracts

import "testing"

func TestTakePendingSystemInitPayloadConsumesValidSnapshotOnce(t *testing.T) {
	session := QuerySession{
		PendingSystemInitKeys: map[string]bool{"coder:execute": true},
		SystemInitCache: map[string]SystemInitSnapshot{
			"coder:execute": {
				AgentKey: "coder", Fingerprint: "sha256:test",
				SystemMessage: map[string]any{"role": "system", "content": []any{map[string]any{"text": "prompt"}}},
				Tools:         []any{map[string]any{"type": "function", "function": map[string]any{"name": "bash"}}},
			},
		},
	}
	payload := TakePendingSystemInitPayload(&session, "coder:execute")
	if payload == nil || payload["agentKey"] != "coder" || payload["fingerprint"] != "sha256:test" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if second := TakePendingSystemInitPayload(&session, "coder:execute"); second != nil {
		t.Fatalf("pending system should only be consumed once, got %#v", second)
	}
	system := payload["systemMessage"].(map[string]any)
	system["role"] = "changed"
	if session.SystemInitCache["coder:execute"].SystemMessage["role"] != "system" {
		t.Fatal("payload mutation leaked into cached snapshot")
	}
}

func TestTakePendingSystemInitPayloadKeepsInvalidSnapshotPending(t *testing.T) {
	session := QuerySession{
		PendingSystemInitKeys: map[string]bool{"react:main": true},
		SystemInitCache: map[string]SystemInitSnapshot{
			"react:main": {AgentKey: "agent"},
		},
	}
	if payload := TakePendingSystemInitPayload(&session, "react:main"); payload != nil {
		t.Fatalf("expected invalid snapshot to be rejected, got %#v", payload)
	}
	if !session.PendingSystemInitKeys["react:main"] {
		t.Fatal("invalid snapshot should remain pending")
	}
}
