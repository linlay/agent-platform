package channel

import (
	"testing"

	"agent-platform/internal/config"
)

func TestRegistryLookupCanonicalChannel(t *testing.T) {
	r := NewRegistry([]config.ChannelConfig{
		{
			ID:        "wecom",
			Name:      "WeCom",
			Mode:      config.ChannelModeClient,
			Transport: config.ChannelTransportWebSocket,
			Protocol:  config.ChannelProtocolPlatformWS,
			Endpoint:  config.ChannelEndpointConfig{URL: "wss://bridge.example.com/ws"},
		},
	})

	def, ok := r.Lookup("wecom")
	if !ok {
		t.Fatalf("expected wecom channel")
	}
	if def.Name != "WeCom" || def.Mode != config.ChannelModeClient || def.Endpoint.URL == "" {
		t.Fatalf("unexpected definition: %#v", def)
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("unexpected missing channel")
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
