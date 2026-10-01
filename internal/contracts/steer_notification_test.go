package contracts

import (
	"agent-platform/internal/api"
	"context"
	"testing"
)

func TestSteerNotificationBroadcastAndRearm(t *testing.T) {
	c := NewRunControl(context.Background(), "run")
	defer c.Finish()
	first, second := c.SteerAvailable(), c.SteerAvailable()
	if !c.EnqueueSteer(api.SteerRequest{Message: "one"}) {
		t.Fatal("rejected")
	}
	for _, ch := range []<-chan struct{}{first, second, c.SteerAvailable()} {
		select {
		case <-ch:
		default:
			t.Fatal("missed wake")
		}
	}
	if len(c.DrainSteers()) != 1 {
		t.Fatal("notification consumed input")
	}
	next := c.SteerAvailable()
	select {
	case <-next:
		t.Fatal("stale wake")
	default:
	}
	c.EnqueueSteer(api.SteerRequest{Message: "two"})
	select {
	case <-next:
	default:
		t.Fatal("did not rearm")
	}
}
