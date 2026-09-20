package adminsource

import (
	"agent-platform/internal/connector"
	"context"
	"errors"
	"testing"
)

func TestConnectorDeleteAcquiresAgentLockBeforeCoordinator(t *testing.T) {
	service := NewService()
	marker := errors.New("coordinator rejected")
	err := service.DeleteConnector(context.Background(), connector.Sources{}, nil, "demo", nil, func(ctx context.Context, mutate func(context.Context) error) error {
		if service.agentMutation.TryLock() {
			service.agentMutation.Unlock()
			t.Fatal("catalog guard entered without Agent mutation lock")
		}
		return marker
	})
	if !errors.Is(err, marker) {
		t.Fatalf("error = %v", err)
	}
	if !service.agentMutation.TryLock() {
		t.Fatal("Agent mutation lock not released")
	}
	service.agentMutation.Unlock()
}
