package server

import (
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
)

const (
	queryAvailabilityCodeOK               = "ok"
	queryAvailabilityCodeConcurrencyLimit = "agent_concurrency_limit_exceeded"
	queryConcurrencyLimitMessage          = "agent concurrency limit exceeded"
)

type queryReleaseFunc func()

type agentQueryLimiter struct {
	mu     sync.Mutex
	active map[string]map[string]api.QueryActiveRunInfo
}

func newAgentQueryLimiter() *agentQueryLimiter {
	return &agentQueryLimiter{active: map[string]map[string]api.QueryActiveRunInfo{}}
}

func (l *agentQueryLimiter) TryAcquire(agentKey string, runID string, chatID string, teamID string, limit int) (queryReleaseFunc, api.QueryAvailabilityResponse) {
	agentKey = strings.TrimSpace(agentKey)
	runID = strings.TrimSpace(runID)
	chatID = strings.TrimSpace(chatID)
	teamID = strings.TrimSpace(teamID)
	if limit <= 0 {
		limit = 1
	}
	if l == nil {
		return func() {}, availabilityOK(agentKey, chatID, teamID, limit, nil)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	items := l.active[agentKey]
	activeRuns := sortedActiveRuns(items)
	if len(activeRuns) >= limit {
		return nil, availabilityLimited(agentKey, chatID, teamID, limit, activeRuns)
	}
	if items == nil {
		items = map[string]api.QueryActiveRunInfo{}
		l.active[agentKey] = items
	}
	items[runID] = api.QueryActiveRunInfo{
		RunID:     runID,
		ChatID:    chatID,
		AgentKey:  agentKey,
		StartedAt: time.Now().UnixMilli(),
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			l.release(agentKey, runID)
		})
	}
	return release, availabilityOK(agentKey, chatID, teamID, limit, sortedActiveRuns(items))
}

func (l *agentQueryLimiter) Snapshot(agentKey string, chatID string, teamID string, limit int) api.QueryAvailabilityResponse {
	agentKey = strings.TrimSpace(agentKey)
	chatID = strings.TrimSpace(chatID)
	teamID = strings.TrimSpace(teamID)
	if limit <= 0 {
		limit = 1
	}
	if l == nil {
		return availabilityOK(agentKey, chatID, teamID, limit, nil)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	activeRuns := sortedActiveRuns(l.active[agentKey])
	if len(activeRuns) >= limit {
		return availabilityLimited(agentKey, chatID, teamID, limit, activeRuns)
	}
	return availabilityOK(agentKey, chatID, teamID, limit, activeRuns)
}

func (l *agentQueryLimiter) release(agentKey string, runID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	items := l.active[agentKey]
	if len(items) == 0 {
		return
	}
	delete(items, runID)
	if len(items) == 0 {
		delete(l.active, agentKey)
	}
}

func availabilityOK(agentKey string, chatID string, teamID string, limit int, activeRuns []api.QueryActiveRunInfo) api.QueryAvailabilityResponse {
	return api.QueryAvailabilityResponse{
		CanQuery:    true,
		Code:        queryAvailabilityCodeOK,
		Message:     "ok",
		AgentKey:    agentKey,
		ChatID:      chatID,
		TeamID:      teamID,
		Concurrency: limit,
		ActiveCount: len(activeRuns),
		ActiveRuns:  activeRuns,
	}
}

func availabilityLimited(agentKey string, chatID string, teamID string, limit int, activeRuns []api.QueryActiveRunInfo) api.QueryAvailabilityResponse {
	return api.QueryAvailabilityResponse{
		CanQuery:    false,
		Code:        queryAvailabilityCodeConcurrencyLimit,
		Message:     queryConcurrencyLimitMessage,
		AgentKey:    agentKey,
		ChatID:      chatID,
		TeamID:      teamID,
		Concurrency: limit,
		ActiveCount: len(activeRuns),
		ActiveRuns:  activeRuns,
	}
}

func sortedActiveRuns(items map[string]api.QueryActiveRunInfo) []api.QueryActiveRunInfo {
	if len(items) == 0 {
		return []api.QueryActiveRunInfo{}
	}
	out := make([]api.QueryActiveRunInfo, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt == out[j].StartedAt {
			return out[i].RunID < out[j].RunID
		}
		return out[i].StartedAt < out[j].StartedAt
	})
	return out
}
