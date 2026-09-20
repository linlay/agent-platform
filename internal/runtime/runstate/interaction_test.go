package runstate

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/interaction"
	"context"
	"testing"
)

func TestInteractionPolicyFrozenForActiveRun(t *testing.T) {
	m := NewManager()
	c := interaction.Defaults("KBASE")
	m.Register(context.Background(), contracts.QuerySession{RunID: "interaction-run", InteractionConfig: &c})
	// A caller mutating its definition must not change the registered policy.
	c.AccessLevel = true
	c.Attachment.ChatRecords = true
	ack := m.UpdateAccessLevel(api.AccessLevelRequest{RunID: "interaction-run", AccessLevel: "full_access"})
	if ack.Accepted || ack.Status != "interaction_disabled" {
		t.Fatalf("access ack: %+v", ack)
	}
	steer := m.Steer(api.SteerRequest{RunID: "interaction-run", References: []api.Reference{{Type: "chat"}}})
	if steer.Accepted || steer.Status != "interaction_disabled" {
		t.Fatalf("steer ack: %+v", steer)
	}
}
