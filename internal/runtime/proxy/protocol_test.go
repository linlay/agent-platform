package proxy

import (
	"reflect"
	"testing"

	"agent-platform/internal/catalog"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func TestForwardParamsWorkspaceTrustBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		proxy     *catalog.ProxyConfig
		workspace string
		cwd       any
	}{
		{"no route", nil, "/private/workspace", nil},
		{"loopback proxy", &catalog.ProxyConfig{BaseURL: "http://127.0.0.1:17071"}, "/private/workspace", nil},
		{"channel", &catalog.ProxyConfig{ChannelID: "peer"}, "/private/workspace", nil},
		{"ACP", &catalog.ProxyConfig{LocalACP: true}, "/private/workspace", "/private/workspace"},
		{"ACP missing workspace", &catalog.ProxyConfig{LocalACP: true}, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, params := range []map[string]any{nil, {"channel": "desktop", "cwd": "/untrusted"}} {
				req := runtimetypes.QueryCommand{Params: params}
				got := ForwardParams(req, tc.proxy, tc.workspace)
				if got["cwd"] != tc.cwd {
					t.Fatalf("cwd = %#v, want %#v", got["cwd"], tc.cwd)
				}
				if got["channel"] != params["channel"] {
					t.Fatalf("lost params: %#v", got)
				}
				if params != nil && params["cwd"] != "/untrusted" {
					t.Fatalf("mutated original request: %#v", params)
				}
				frame := QueryPayloadWithWorkspace(req, tc.proxy, nil, tc.workspace)
				if !reflect.DeepEqual(frame["payload"].(map[string]any)["params"], got) {
					t.Fatalf("WS payload differs from HTTP params: %#v", frame)
				}
			}
		})
	}
}

func TestNormalizeQueryEventRemovesEchoedExecutionCWD(t *testing.T) {
	params := map[string]any{"cwd": "/private/workspace", "channel": "desktop"}
	event := NormalizeEventIdentity(stream.EventData{Type: "request.query", Payload: map[string]any{"params": params}}, runtimetypes.QueryCommand{ChatID: "local-chat"})
	got := event.Payload["params"].(map[string]any)
	if _, exists := got["cwd"]; exists || got["channel"] != "desktop" || event.String("chatId") != "local-chat" {
		t.Fatalf("unexpected public query event: %#v", event)
	}
	if params["cwd"] != "/private/workspace" {
		t.Fatal("mutated upstream params")
	}
}

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

func TestDecodeProxyApprovalDropsOptionDescription(t *testing.T) {
	eventJSON := `{"seq":2,"type":"awaiting.ask","timestamp":1700000000000,"mode":"approval","approvals":[{"id":"permission-1","command":"echo ok","description":"approval title","options":[{"decision":"approve","description":"ACP option text"}]}]}`
	for _, tc := range []struct {
		name string
		data string
	}{
		{"HTTP event", eventJSON},
		{"WebSocket frame", `{"frame":"stream","id":"request-1","event":` + eventJSON + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame, ok, err := DecodeFrameAt([]byte(tc.data), "proxy.test")
			if err != nil || !ok || !frame.HasEvent {
				t.Fatalf("decode = %#v, ok=%v, err=%v", frame, ok, err)
			}
			approval := frame.Event.Payload["approvals"].([]any)[0].(map[string]any)
			option := approval["options"].([]any)[0].(map[string]any)
			if !reflect.DeepEqual(option, map[string]any{"decision": "approve"}) || approval["description"] != "approval title" {
				t.Fatalf("unexpected approval payload: %#v", approval)
			}
		})
	}
}
