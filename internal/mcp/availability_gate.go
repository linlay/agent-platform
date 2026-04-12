package mcp

import (
	"sort"
	"sync"
	"time"
)

type AvailabilityGate struct {
	mu                sync.Mutex
	failures          map[string]int
	nextRetry         map[string]time.Time
	reconnectInterval time.Duration
}

func NewAvailabilityGate() *AvailabilityGate {
	return &AvailabilityGate{
		failures:          map[string]int{},
		nextRetry:         map[string]time.Time{},
		reconnectInterval: 30 * time.Second,
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
	g.nextRetry[key] = time.Now().Add(g.reconnectInterval)
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

func (g *AvailabilityGate) IsUnavailable(serverKey string) bool {
	return g.IsBlocked(serverKey)
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
		}
	}
}
