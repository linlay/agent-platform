package mcp

import (
	"sort"
	"sync"
	"time"

	"agent-platform/internal/retry"
)

type AvailabilityGate struct {
	mu             sync.Mutex
	failures       map[string]int
	nextRetry      map[string]time.Time
	currentBackoff map[string]time.Duration
	policy         retry.BackoffPolicy
}

func NewAvailabilityGate() *AvailabilityGate {
	return NewAvailabilityGateWithPolicy(retry.BackoffPolicy{
		Min:    30 * time.Second,
		Max:    30 * time.Second,
		Factor: 2,
	})
}

func NewAvailabilityGateWithPolicy(policy retry.BackoffPolicy) *AvailabilityGate {
	return &AvailabilityGate{
		failures:       map[string]int{},
		nextRetry:      map[string]time.Time{},
		currentBackoff: map[string]time.Duration{},
		policy:         policy,
	}
}

func (g *AvailabilityGate) Allow(serverKey string) bool {
	return !g.IsBlocked(serverKey)
}

func (g *AvailabilityGate) IsBlocked(serverKey string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	next := g.nextRetry[normalizeKey(serverKey)]
	return !next.IsZero() && time.Now().Before(next)
}

func (g *AvailabilityGate) MarkSuccess(serverKey string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.failures, normalizeKey(serverKey))
	delete(g.nextRetry, normalizeKey(serverKey))
	delete(g.currentBackoff, normalizeKey(serverKey))
	g.mu.Unlock()
}

func (g *AvailabilityGate) MarkFailure(serverKey string) {
	if g == nil {
		return
	}
	key := normalizeKey(serverKey)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures[key]++
	backoff := g.policy.Next(g.currentBackoff[key])
	g.currentBackoff[key] = backoff
	g.nextRetry[key] = time.Now().Add(backoff)
}

func (g *AvailabilityGate) ReadyToRetry(serverKeys []string) []string {
	if g == nil {
		return nil
	}
	now := time.Now()
	ready := make([]string, 0, len(serverKeys))
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, serverKey := range serverKeys {
		key := normalizeKey(serverKey)
		next := g.nextRetry[key]
		if !next.IsZero() && !now.Before(next) {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)
	return ready
}

func (g *AvailabilityGate) Prune(activeServerKeys []string) {
	if g == nil {
		return
	}
	allowed := map[string]struct{}{}
	for _, key := range activeServerKeys {
		if normalized := normalizeKey(key); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for key := range g.failures {
		if _, ok := allowed[key]; !ok {
			delete(g.failures, key)
			delete(g.nextRetry, key)
			delete(g.currentBackoff, key)
		}
	}
}
