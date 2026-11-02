package models

import "testing"

func TestRealtimeVoiceModelCapabilities(t *testing.T) {
	for _, tc := range []struct {
		typ, protocol string
		valid         bool
	}{
		{ModelTypeASR, ProtocolDashScopeASR, true},
		{ModelTypeTTS, ProtocolDashScopeRealtimeTTS, true},
		{ModelTypeTTS, ProtocolDashScopeTTS, true},
		{ModelTypeTTS, ProtocolMiniMaxTTS, true},
		{ModelTypeChat, ProtocolDashScopeASR, false},
		{ModelTypeASR, ProtocolDashScopeTTS, false},
		{ModelTypeASR, "OPENAI", false},
		{ModelTypeChat, ProtocolDashScopeRealtimeTTS, false},
	} {
		if err := ValidateModelTypeProtocol(tc.typ, tc.protocol); (err == nil) != tc.valid {
			t.Errorf("%s/%s: %v", tc.typ, tc.protocol, err)
		}
	}
	for _, protocol := range TTSProtocols {
		if protocol == ProtocolDashScopeRealtimeTTS {
			t.Fatal("realtime protocol leaked into file speech protocols")
		}
	}
	for _, protocol := range []string{ProtocolDashScopeASR, ProtocolDashScopeRealtimeTTS} {
		p := ProviderDefinition{}
		if p.ResolvedEndpointPath(protocol) != "/api-ws/v1/inference" {
			t.Fatal("wrong WS default")
		}
		p.Protocols = map[string]ProtocolDefinition{protocol: {EndpointPath: "/workspace/voice"}}
		if p.ResolvedEndpointPath(protocol) != "/workspace/voice" {
			t.Fatal("override lost")
		}
	}
}
