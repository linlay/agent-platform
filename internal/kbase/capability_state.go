package kbase

import (
	"strings"
	"sync"
)

type capabilityState struct {
	mu              sync.RWMutex
	startupFailures map[string]error
}

func newCapabilityState() *capabilityState {
	return &capabilityState{startupFailures: map[string]error{}}
}

func (s *capabilityState) ReplaceFailures(failures map[string]error) {
	copyOfFailures := make(map[string]error, len(failures))
	for key, err := range failures {
		copyOfFailures[strings.TrimSpace(key)] = err
	}
	s.mu.Lock()
	s.startupFailures = copyOfFailures
	s.mu.Unlock()
}

func (s *capabilityState) Failure(agentKey string) error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.startupFailures[strings.TrimSpace(agentKey)]
}

func (s *capabilityState) ClearFailure(agentKey string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.startupFailures, strings.TrimSpace(agentKey))
	s.mu.Unlock()
}

func (s *capabilityState) DegradedError(agentKey string) error {
	if failure := s.Failure(agentKey); failure != nil {
		return &PolicyError{Kind: ErrorUnavailable, Message: "KBASE capability is degraded: " + failure.Error()}
	}
	return nil
}
