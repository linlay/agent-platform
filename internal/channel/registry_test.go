package channel

import (
	"reflect"
	"testing"

	"agent-platform-runner-go/internal/config"
)

func TestRegistryLookupAndDefaultAgent(t *testing.T) {
	r := NewRegistry([]config.ChannelConfig{
		{
			ID:           "wecom",
			Name:         "WeCom",
			Type:         config.ChannelTypeBridge,
			DefaultAgent: "customer-service",
			AllAgents:    true,
		},
		{
			ID:        "feishu",
			Name:      "Feishu",
			Type:      config.ChannelTypeBridge,
			AllAgents: false,
			Agents:    []string{"assistant", "code-helper"},
		},
	})

	def, ok := r.Lookup("wecom")
	if !ok {
		t.Fatalf("expected wecom channel")
	}
	if def.Name != "WeCom" || def.Type != config.ChannelTypeBridge {
		t.Fatalf("unexpected definition: %#v", def)
	}
	if got := r.DefaultAgent("wecom"); got != "customer-service" {
		t.Fatalf("default agent = %q, want customer-service", got)
	}
	if got := r.DefaultAgent("missing"); got != "" {
		t.Fatalf("default agent for missing channel = %q, want empty", got)
	}
}

func TestRegistryIsAgentAllowed(t *testing.T) {
	r := NewRegistry([]config.ChannelConfig{
		{
			ID:        "feishu",
			Type:      config.ChannelTypeBridge,
			AllAgents: false,
			Agents:    []string{"assistant", "code-helper"},
		},
		{
			ID:        "mobile",
			Type:      config.ChannelTypeGateway,
			AllAgents: true,
		},
	})

	if !r.IsAgentAllowed("feishu", "assistant") {
		t.Fatalf("assistant should be allowed on feishu")
	}
	if r.IsAgentAllowed("feishu", "customer-service") {
		t.Fatalf("customer-service should not be allowed on feishu")
	}
	if !r.IsAgentAllowed("mobile", "anything") {
		t.Fatalf("all-agents channel should allow any agent")
	}
	if !r.IsAgentAllowed("unknown", "anything") {
		t.Fatalf("unknown channel should stay compatible")
	}
}

func TestRegistryAllowedAgentKeysAreSorted(t *testing.T) {
	r := NewRegistry([]config.ChannelConfig{
		{
			ID:        "wecom",
			Type:      config.ChannelTypeBridge,
			AllAgents: false,
			Agents:    []string{"zeta", "alpha", "alpha", "beta"},
		},
	})

	if got, want := r.AllowedAgentKeys("wecom"), []string{"alpha", "beta", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed agent keys = %#v, want %#v", got, want)
	}
	if got := r.AllowedAgentKeys("unknown"); got != nil {
		t.Fatalf("unknown channel allowed agents = %#v, want nil", got)
	}
}

func TestChannelForChatID(t *testing.T) {
	cases := map[string]string{
		"wecom#single#user1#abc": "wecom",
		"mobile#chat#123":        "mobile",
		"":                       "",
		"plain-chat-id":          "",
		"#leading":               "",
	}
	for input, want := range cases {
		if got := ChannelForChatID(input); got != want {
			t.Fatalf("ChannelForChatID(%q) = %q, want %q", input, got, want)
		}
	}
}
