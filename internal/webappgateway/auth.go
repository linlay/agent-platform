// Package webappgateway serves only authenticated application origins. It
// never forwards an application request into the Platform API router.
package webappgateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"time"
)

type binding struct {
	appID, generation, policy, owner string
	ctx                              context.Context
}
type ticket struct {
	binding
	destination string
	expires     time.Time
	stop        func() bool
}
type session struct {
	binding
	csrf              string
	created, lastUsed time.Time
	stop              func() bool
}
type authorization struct {
	mu       sync.Mutex
	tickets  map[[32]byte]*ticket
	sessions map[[32]byte]*session
	now      func() time.Time
}

func newAuthorization() *authorization {
	return &authorization{tickets: map[[32]byte]*ticket{}, sessions: map[[32]byte]*session{}, now: time.Now}
}
func token(prefix string) string { return prefix + rand.Text() + rand.Text() }
func (a *authorization) collect() {
	now := a.now()
	for k, v := range a.tickets {
		if now.After(v.expires) || v.ctx.Err() != nil {
			v.stop()
			delete(a.tickets, k)
		}
	}
	for k, v := range a.sessions {
		if now.Sub(v.lastUsed) > 30*time.Minute || now.Sub(v.created) > 8*time.Hour || v.ctx.Err() != nil {
			v.stop()
			delete(a.sessions, k)
		}
	}
}
func (a *authorization) revokeGeneration(id, generation string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.tickets {
		if v.appID == id && v.generation == generation {
			v.stop()
			delete(a.tickets, k)
		}
	}
	for k, v := range a.sessions {
		if v.appID == id && v.generation == generation {
			v.stop()
			delete(a.sessions, k)
		}
	}
}
func (a *authorization) revokeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.tickets {
		v.stop()
		delete(a.tickets, k)
	}
	for k, v := range a.sessions {
		v.stop()
		delete(a.sessions, k)
	}
}
func (a *authorization) issue(b binding, destination string) (string, time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.collect()
	if len(a.tickets) >= 1024 || b.ctx.Err() != nil {
		return "", time.Time{}, false
	}
	raw := token("wlt_")
	t := &ticket{binding: b, destination: destination, expires: a.now().Add(time.Minute)}
	t.stop = context.AfterFunc(b.ctx, func() { a.revokeGeneration(b.appID, b.generation) })
	a.tickets[sha256.Sum256([]byte(raw))] = t
	return raw, t.expires, true
}
func (a *authorization) exchange(raw string, current binding) (string, *session, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.collect()
	key := sha256.Sum256([]byte(raw))
	t := a.tickets[key]
	if t == nil {
		return "", nil, "", false
	}
	delete(a.tickets, key)
	t.stop()
	if len(a.sessions) >= 2048 || t.appID != current.appID || t.generation != current.generation || t.policy != current.policy || t.ctx.Err() != nil {
		return "", nil, "", false
	}
	raw = token("was_")
	now := a.now()
	s := &session{binding: t.binding, csrf: token("wcsrf_"), created: now, lastUsed: now}
	s.stop = context.AfterFunc(s.ctx, func() { a.revokeGeneration(s.appID, s.generation) })
	a.sessions[sha256.Sum256([]byte(raw))] = s
	return raw, s, t.destination, true
}
func (a *authorization) authenticate(raw string, b binding) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.collect()
	s := a.sessions[sha256.Sum256([]byte(raw))]
	if s == nil || s.appID != b.appID || s.generation != b.generation || s.policy != b.policy || s.ctx.Err() != nil {
		return "", false
	}
	s.lastUsed = a.now()
	return s.csrf, true
}
