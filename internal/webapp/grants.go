// Package webapp owns application capability grants, independent of HTTP and
// Desktop transport. Only a trusted host may issue a grant for an application.
package webapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorops"
)

var ErrDenied = errors.New("app_grant_required")

type Grant struct {
	ID         string `json:"grantId"`
	Token      string `json:"token,omitempty"`
	AppID      string `json:"appId"`
	ExpiresAt  int64  `json:"expiresAt"`
	allowWrite bool
	subject    string
	operations map[string][]string
	chats      map[string]bool
	context    context.Context
	cancel     context.CancelFunc
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
func (g *Grants) Issue(subject, app string, operations map[string][]string) (Grant, error) {
	return g.IssueWithChats(subject, app, operations, nil)
}

func (g *Grants) IssueWithChats(subject, app string, operations map[string][]string, chats []string) (Grant, error) {
	return g.IssueWithPermissions(subject, app, operations, chats, false)
}
func (g *Grants) IssueWithPermissions(subject, app string, operations map[string][]string, chats []string, allowWrite bool) (Grant, error) {
	if len(chats) > 128 {
		return Grant{}, ErrDenied
	}
	chatMap := map[string]bool{}
	for _, id := range chats {
		if !chat.ValidChatID(id) {
			return Grant{}, ErrDenied
		}
		chatMap[id] = true
	}
	if subject == "" || !connector.ValidID(app) || len(app) > 128 || len(operations) > 64 {
		return Grant{}, ErrDenied
	}
	copied := map[string][]string{}
	for id, ops := range operations {
		if !connector.ValidID(id) || len(ops) == 0 || len(ops) > 128 {
			return Grant{}, ErrDenied
		}
		for _, op := range ops {
			if !connector.ValidID(op) || len(op) > 128 {
				return Grant{}, ErrDenied
			}
		}
		copied[id] = append([]string{}, ops...)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune()
	if len(g.entries) >= 1024 {
		return Grant{}, ErrDenied
	}
	ctx, cancel := context.WithTimeout(g.root, 15*time.Minute)
	grant := Grant{ID: rand.Text(), Token: "wap_" + rand.Text(), AppID: app, ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli(), allowWrite: allowWrite, subject: subject, operations: copied, chats: chatMap, context: ctx, cancel: cancel}
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
func (g *Grants) Scope(token string) (connectorops.Scope, context.Context, error) {
	key := tokenKey(token)
	g.mu.Lock()
	g.prune()
	grant := g.entries[key]
	g.mu.Unlock()
	if grant == nil {
		return connectorops.Scope{}, nil, ErrDenied
	}
	copied := map[string][]string{}
	for k, v := range grant.operations {
		copied[k] = append([]string{}, v...)
	}
	check := func() error {
		g.mu.Lock()
		defer g.mu.Unlock()
		current := g.entries[key]
		if current != grant || grant.context.Err() != nil || time.Now().UnixMilli() >= grant.ExpiresAt {
			return ErrDenied
		}
		return nil
	}
	chatMap := map[string]bool{}
	for id := range grant.chats {
		chatMap[id] = true
	}
	return connectorops.Scope{AllowWrite: grant.allowWrite, Chats: chatMap, Subject: grant.subject, AppID: grant.AppID, Operations: copied, Check: check}, grant.context, nil
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
