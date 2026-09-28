package connectorops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"agent-platform/internal/connector"
)

var ErrDenied = errors.New("connector_grant_required")

type Grant struct {
	ID                   string `json:"grantId"`
	Token                string `json:"token,omitempty"`
	IdempotencyNamespace string `json:"idempotencyNamespace"`
	ExpiresAt            int64  `json:"expiresAt"`
	subject              string
	execution            []Permission
	context              context.Context
	cancel               context.CancelFunc
}
type Grants struct {
	mu      sync.Mutex
	entries map[string]*Grant
	root    context.Context
}

func NewGrants(ctx context.Context) *Grants { return &Grants{entries: map[string]*Grant{}, root: ctx} }
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Issue freezes connector permissions for an authenticated authority. The namespace
// only partitions durable receipts; it grants no permissions or resource ownership.
func (g *Grants) Issue(subject, namespace string, execution []Permission) (Grant, error) {
	if subject == "" || !connector.ValidID(namespace) || len(namespace) > 128 || len(execution) > 64 {
		return Grant{}, ErrDenied
	}
	copied := append([]Permission{}, execution...)
	seen := map[Permission]bool{}
	for _, p := range copied {
		if !connector.ValidID(p.ConnectorID) || (p.Adapter != "cli" && p.Adapter != "mcp") || seen[p] {
			return Grant{}, ErrDenied
		}
		seen[p] = true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune()
	if len(g.entries) >= 1024 {
		return Grant{}, ErrDenied
	}
	ctx, cancel := context.WithTimeout(g.root, 15*time.Minute)
	grant := Grant{ID: rand.Text(), Token: "cxg_" + rand.Text(), IdempotencyNamespace: namespace, ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli(), subject: subject, execution: copied, context: ctx, cancel: cancel}
	stored := grant
	stored.Token = ""
	g.entries[tokenKey(grant.Token)] = &stored
	return grant, nil
}
func (g *Grants) prune() {
	for key, v := range g.entries {
		if v.context.Err() != nil || time.Now().UnixMilli() >= v.ExpiresAt {
			v.cancel()
			delete(g.entries, key)
		}
	}
}
func (g *Grants) Scope(token string) (Scope, context.Context, error) {
	key := tokenKey(token)
	g.mu.Lock()
	g.prune()
	grant := g.entries[key]
	g.mu.Unlock()
	if grant == nil {
		return Scope{}, nil, ErrDenied
	}
	copied := append([]Permission{}, grant.execution...)
	check := func() error {
		g.mu.Lock()
		defer g.mu.Unlock()
		current := g.entries[key]
		if current != grant || grant.context.Err() != nil || time.Now().UnixMilli() >= grant.ExpiresAt {
			return ErrDenied
		}
		return nil
	}
	return Scope{Subject: grant.subject, IdempotencyNamespace: grant.IdempotencyNamespace, Execution: copied, Check: check}, grant.context, nil
}
func (g *Grants) Revoke(subject, id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, v := range g.entries {
		if v.ID == id && v.subject == subject {
			v.cancel()
			delete(g.entries, key)
			return nil
		}
	}
	return ErrDenied
}
