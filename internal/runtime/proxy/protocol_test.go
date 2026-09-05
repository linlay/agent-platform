package proxy

import (
	"testing"

	"agent-platform/internal/catalog"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestQueryPayloadDoesNotInjectHostWorkspace(t *testing.T) {
	payload := QueryPayloadWithWorkspace(runtimetypes.QueryCommand{
		RequestID: "request-1", RunID: "run-1", ChatID: "chat-1", AgentKey: "local-agent",
		Message: "hello", Params: map[string]any{"channel": "desktop"},
	}, &catalog.ProxyConfig{AgentKey: "remote-agent"}, nil, "/private/workspace")
	inner, _ := payload["payload"].(map[string]any)
	params, _ := inner["params"].(map[string]any)
	if _, exists := params["cwd"]; exists {
		t.Fatalf("proxy payload leaked cwd: %#v", payload)
	}
	if inner["agentKey"] != "remote-agent" {
		t.Fatalf("proxy agent key = %#v", inner["agentKey"])
	}
}

func TestDecodeFramePreservesEventIdentity(t *testing.T) {
	frame, ok, err := DecodeFrameAt([]byte(`{"frame":"stream","id":"request-1","event":{"seq":2,"type":"content.delta","timestamp":1700000000000,"delta":"hi"}}`), "proxy.test")
	if err != nil || !ok || !frame.HasEvent {
		t.Fatalf("decode = %#v, ok=%v, err=%v", frame, ok, err)
	}
	if frame.Event.Seq != 2 || frame.Event.String("delta") != "hi" {
		t.Fatalf("event = %#v", frame.Event)
	}
}
