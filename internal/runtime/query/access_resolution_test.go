package query

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
	"testing"
)

func TestQueryAccessResolutionSources(t *testing.T) {
	def := catalog.AgentDefinition{Key: "a", RuntimeRevision: "v1", AccessConfig: contracts.AgentAccessConfig{Default: "full_access"}}
	for _, tc := range []struct{ requested, origin, level, source string }{
		{"", "user", "full_access", "agent_default"}, {"default", "user", "default", "explicit"},
		{"", "automation", "default", "automation_default"}, {"auto_approve", "automation", "auto_approve", "automation_config"},
	} {
		req := runtimetypes.QueryCommand{AccessLevel: tc.requested, Origin: &queryinput.Origin{Type: tc.origin}}
		ResolveQueryAccess(&req, def)
		if req.AccessLevel != tc.level || req.AccessGrant["source"] != tc.source {
			t.Fatalf("%+v -> %+v", tc, req)
		}
	}
	req := runtimetypes.QueryCommand{AccessLevel: "auto_approve", AccessGrant: map[string]any{"source": "agent_default", "agentRevision": "old"}}
	ResolveQueryAccess(&req, def)
	if req.AccessLevel != "auto_approve" || req.AccessGrant["agentRevision"] != "old" {
		t.Fatal("frozen mail authorization was recomputed")
	}
}

func TestDisabledAccessSelectionUsesConfiguredDefault(t *testing.T) {
	def := catalog.AgentDefinition{AccessConfig: contracts.AgentAccessConfig{Default: "full_access"}}
	c := def.Interaction()
	c.AccessLevel = false
	for _, level := range []string{"", "default", "full_access", " FULL_ACCESS "} {
		err := ValidateInteractionInput(c, runtimetypes.QueryCommand{AccessLevel: level}, def.AccessConfig.EffectiveDefault())
		if (err != nil) != (level == "default") {
			t.Fatalf("selection=%q: %v", level, err)
		}
	}
}
