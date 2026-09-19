package webapp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestGrantOwnerRevocationAndFrozenOperations(t *testing.T) {
	g := NewGrants(context.Background())
	ops := map[string][]string{"wecom": {"meetings.list"}}
	issued, err := g.Issue("alice", "calendar", ops)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(issued)
	var wire struct {
		ExpiresAt int64 `json:"expiresAt"`
	}
	if err != nil || json.Unmarshal(encoded, &wire) != nil || wire.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatal("grant expiry must be future epoch milliseconds", string(encoded), err)
	}
	ops["wecom"][0] = "meetings.delete"
	scope, ctx, err := g.Scope(issued.Token)
	if err != nil || scope.Operations["wecom"][0] != "meetings.list" {
		t.Fatal("mutable grant", err)
	}
	scope.Operations["wecom"][0] = "meetings.delete"
	fresh, _, _ := g.Scope(issued.Token)
	if fresh.Operations["wecom"][0] != "meetings.list" {
		t.Fatal("caller mutated grant")
	}
	if g.Revoke("bob", issued.ID) == nil {
		t.Fatal("cross-owner revoke")
	}
	if g.Revoke("alice", issued.ID) != nil {
		t.Fatal("revoke failed")
	}
	if ctx.Err() == nil || scope.Check() == nil {
		t.Fatal("in-flight grant survived revoke")
	}
	if _, _, err = g.Scope(issued.Token); err == nil {
		t.Fatal("revoked token accepted")
	}
}
func TestGrantExpiresWithHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g := NewGrants(ctx)
	v, _ := g.Issue("alice", "app", nil)
	cancel()
	if _, _, err := g.Scope(v.Token); err == nil {
		t.Fatal("host shutdown retained grant")
	}
}
