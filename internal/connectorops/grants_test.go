package connectorops

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestGrantOwnerRevocationAndFrozenOperations(t *testing.T) {
	g := NewGrants(context.Background())
	ops := []Permission{{ConnectorID: "wecom", Adapter: "cli"}}
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
	ops[0].Adapter = "mcp"
	scope, ctx, err := g.Scope(issued.Token)
	if err != nil || scope.Execution[0].Adapter != "cli" {
		t.Fatal("mutable grant", err)
	}
	scope.Execution[0].Adapter = "mcp"
	fresh, _, _ := g.Scope(issued.Token)
	if fresh.Execution[0].Adapter != "cli" {
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

func TestGrantExpiryAndAdapterBoundaries(t *testing.T) {
	g := NewGrants(context.Background())
	grant, err := g.Issue("authority", "stable", []Permission{{ConnectorID: "demo", Adapter: "cli"}})
	if err != nil {
		t.Fatal(err)
	}
	scope, ctx, err := g.Scope(grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.permits("demo", "cli") || scope.permits("demo", "mcp") || scope.permits("other", "cli") {
		t.Fatal("permission escaped connector/adapter")
	}
	// Receipt namespaces never grant access.
	scope.IdempotencyNamespace = "other"
	if scope.permits("other", "cli") {
		t.Fatal("receipt namespace became an authorization")
	}
	g.mu.Lock()
	g.entries[tokenKey(grant.Token)].ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	g.mu.Unlock()
	if _, _, err = g.Scope(grant.Token); err == nil || ctx.Err() == nil || scope.Check() == nil {
		t.Fatal("expired capability remained usable")
	}
}
