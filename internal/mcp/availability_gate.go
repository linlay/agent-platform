package mcp

import (
	"sync"
	"time"
)

type AvailabilityGate struct {
	mu         sync.Mutex
	failures   map[string]int
	nextProbe  map[string]time.Time
	maxBackoff time.Duration
}

func NewAvailabilityGate() *AvailabilityGate {
	return &AvailabilityGate{
		failures:   map[string]int{},
		nextProbe:  map[string]time.Time{},
		maxBackoff: 30 * time.Second,
	}
}

func (g *AvailabilityGate) Allow(serverKey string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	next := g.nextProbe[serverKey]
	return next.IsZero() || !time.Now().Before(next)
}

func (g *AvailabilityGate) MarkSuccess(serverKey string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.failures, serverKey)
	delete(g.nextProbe, serverKey)
	g.mu.Unlock()
}

func (g *AvailabilityGate) MarkFailure(serverKey string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures[serverKey]++
	backoff := time.Duration(1<<min(g.failures[serverKey]-1, 5)) * time.Second
	if backoff > g.maxBackoff {
		backoff = g.maxBackoff
	}
	g.nextProbe[serverKey] = time.Now().Add(backoff)
}

func (g *AvailabilityGate) IsUnavailable(serverKey string) bool {
	return !g.Allow(serverKey)
}

func min(value int, other int) int {
	if value < other {
		return value
	}
	return other
}
