package platformcontrol

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversation"
)

type pinNotifications struct {
	types []string
	times []int64
}

func (n *pinNotifications) Broadcast(event string, data map[string]any) {
	n.types = append(n.types, event)
	n.times = append(n.times, data["updatedAt"].(int64))
}

func pinCaller() *contracts.ExecutionContext {
	return &contracts.ExecutionContext{Session: contracts.QuerySession{
		RunID: "run-1", ChatID: "current", AgentKey: "agent", Mode: "REACT",
		RunOwner: contracts.AgentRunOwner("agent", ""), ToolNames: []string{ToolName},
	}}
}

func callPin(t *testing.T, h *ToolHandler, caller *contracts.ExecutionContext, params map[string]any) contracts.ToolExecutionResult {
	t.Helper()
	result, err := h.Invoke(context.Background(), ToolName, map[string]any{"operation": "chat.set_pinned", "params": params}, caller)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestChatPinPersistsAndBroadcastsOnlyChanges(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, id := range []string{"current", "other"} {
		if _, _, err := store.EnsureChat(id, "agent", "", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.EnsureChat("pending", "agent", "", ""); err != nil {
		t.Fatal(err)
	}
	notifications := &pinNotifications{}
	service := conversation.NewService(store, nil, nil, nil)
	service.Notifications = notifications
	h := NewToolHandler(config.Config{PlatformControl: config.PlatformControlConfig{Enabled: true}}, nil, service)
	caller := pinCaller()
	// A request-body Chat ID must never override the trusted session default.
	caller.Request.ChatID = "other"
	for _, tc := range []struct {
		params  map[string]any
		id      string
		changed bool
		order   []string
	}{
		{map[string]any{"pinned": true}, "current", true, []string{"current"}},
		{map[string]any{"chatId": "other", "pinned": true}, "other", true, []string{"other", "current"}},
		{map[string]any{"pinned": true}, "current", false, []string{"other", "current"}},
		{map[string]any{"pinned": false}, "current", true, []string{"other"}},
		{map[string]any{"chatId": "missing", "pinned": false}, "missing", false, []string{"other"}},
	} {
		result := callPin(t, h, caller, tc.params)
		want := map[string]any{"chatId": tc.id, "pinned": tc.params["pinned"], "changed": tc.changed}
		if result.Error != "" || !reflect.DeepEqual(result.Structured["data"], want) || result.Structured["scope"] != "instance" {
			t.Fatalf("pin result: %+v", result)
		}
		if _, exists := result.Structured["revision"]; exists {
			t.Fatal("instance mutation exposed run revision")
		}
		pins, err := store.ChatPinned()
		if err != nil || !reflect.DeepEqual(pins.Order, tc.order) {
			t.Fatalf("pins: %+v %v", pins, err)
		}
	}
	for id, code := range map[string]string{"missing": "chat_not_found", "pending": "chat_pin_invalid_target"} {
		if result := callPin(t, h, caller, map[string]any{"chatId": id, "pinned": true}); result.Error != code {
			t.Fatalf("%s: %+v", id, result)
		}
	}
	if !reflect.DeepEqual(notifications.types, []string{"chats.order.changed", "chats.order.changed", "chats.order.changed"}) {
		t.Fatalf("notifications: %+v", notifications)
	}
	for i, timestamp := range notifications.times {
		if timestamp < 1_000_000_000_000 || (i > 0 && timestamp <= notifications.times[i-1]) {
			t.Fatal("invalid push timestamp")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pins, err := reopened.ChatPinned()
	if err != nil || !reflect.DeepEqual(pins.Order, []string{"other"}) {
		t.Fatalf("persisted pins: %+v %v", pins, err)
	}
}

func TestChatPinRejectsInvalidArgumentsAndCallers(t *testing.T) {
	h := NewToolHandler(config.Config{PlatformControl: config.PlatformControlConfig{Enabled: true}}, nil, nil)
	for _, params := range []map[string]any{
		{}, {"pinned": "true"}, {"pinned": nil}, {"pinned": 1},
		{"pinned": true, "chatId": ""}, {"pinned": true, "chatId": 1},
		{"pinned": true, "chatId": "../other"}, {"pinned": true, "extra": true},
	} {
		if r := callPin(t, h, pinCaller(), params); r.Error != "platform_control_invalid_params" {
			t.Fatalf("%+v: %+v", params, r)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*contracts.ExecutionContext)
	}{
		{"child", func(c *contracts.ExecutionContext) { c.Session.SubTaskID = "child" }},
		{"team", func(c *contracts.ExecutionContext) { c.Session.TeamID = "team" }},
		{"team-owner", func(c *contracts.ExecutionContext) { c.Session.RunOwner = contracts.TeamRunOwner("team", "agent") }},
		{"unmounted", func(c *contracts.ExecutionContext) { c.Session.ToolNames = nil }},
		{"acp", func(c *contracts.ExecutionContext) { c.Session.Mode = "CODER"; c.Session.ToolNames = nil }},
		{"proxy", func(c *contracts.ExecutionContext) { c.Session.Mode = "PROXY" }},
		{"channel", func(c *contracts.ExecutionContext) { c.Session.Mode = "CHANNEL" }},
		{"no-run", func(c *contracts.ExecutionContext) { c.Session.RunID = "" }},
		{"owner-mismatch", func(c *contracts.ExecutionContext) { c.Session.RunOwner = contracts.AgentRunOwner("other", "") }},
		{"planning", func(c *contracts.ExecutionContext) { c.ToolExecutionPolicy = "read_only" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := pinCaller()
			tc.edit(caller)
			r := callPin(t, h, caller, map[string]any{"pinned": true})
			want := "chat_pin_forbidden"
			if tc.name == "planning" {
				want = "platform_control_stage_forbidden"
			}
			if r.Error != want {
				t.Fatalf("result: %+v", r)
			}
			caps := h.capabilities(caller, nil)
			if slices.Contains(caps.Structured["operations"].([]string), "chat.set_pinned") {
				t.Fatal("advertised forbidden mutation")
			}
		})
	}
	if r := callPin(t, h, nil, map[string]any{"pinned": true}); r.Error != "chat_pin_forbidden" {
		t.Fatalf("nil caller: %+v", r)
	}
	caller := pinCaller()
	caller.Session.ChatID = ""
	if r := callPin(t, h, caller, map[string]any{"pinned": true}); r.Error != "chat_context_unavailable" {
		t.Fatalf("no current chat: %+v", r)
	}
	if r := callPin(t, h, caller, map[string]any{"chatId": "other", "pinned": true}); r.Error != "chat_pin_unavailable" {
		t.Fatalf("explicit target: %+v", r)
	}
	if !slices.Contains(h.capabilities(pinCaller(), nil).Structured["operations"].([]string), "chat.set_pinned") {
		t.Fatal("root capability missing")
	}
	h.cfg.PlatformControl.Enabled = false
	if r := callPin(t, h, pinCaller(), map[string]any{"pinned": true}); r.Error != "platform_control_disabled" {
		t.Fatalf("disabled: %+v", r)
	}
	d, _ := LookupOperation("chat.set_pinned")
	if !d.Barrier || d.ReadOnly || d.AllowsExecutionPolicy("read_only") {
		t.Fatalf("unsafe descriptor: %+v", d)
	}
}
