package acp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/catalog"
	dto "agent-platform/internal/contracts/agentengine"
)

const configPreviewTTL = 30 * time.Second

type configPreview struct {
	result dto.ConfigPreview
	at     time.Time
}
type previewOperation struct {
	key    string
	done   chan struct{}
	result dto.ConfigPreview
	err    error
}

// Preview discovers new-session defaults without reserving a Chat. Cache reads
// never spawn. Refreshes coalesce and use only spare capacity, never reclaiming
// user sessions. Actual sends still prepare and confirm their own configuration.
func (s *Sessions) Preview(ctx context.Context, agentKey string, refresh bool) (dto.ConfigPreview, error) {
	def, ok := s.resolve(agentKey)
	if !ok {
		return dto.ConfigPreview{}, agentengine.Error("engine_unavailable")
	}
	engineID, workspace, err := s.ValidateTarget(def)
	if err != nil {
		return dto.ConfigPreview{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return dto.ConfigPreview{}, ErrClosed
	}
	key := s.previewKey(engineID, workspace, def)
	cached, found := s.previews[key]
	if !refresh || (found && time.Since(cached.at) < configPreviewTTL) {
		s.mu.Unlock()
		if found {
			return clonePreview(cached.result), nil
		}
		return dto.ConfigPreview{EngineID: engineID, ConfigOptions: []dto.ExternalConfigOption{}}, nil
	}
	pending := s.previewPending
	if pending != nil && pending.key != key {
		s.mu.Unlock()
		return dto.ConfigPreview{}, ErrBusy
	}
	if pending == nil {
		stats := s.host.Stats()
		if stats.Active >= s.host.options.MaxActive || stats.Queued > 0 || len(s.preparations) > 0 {
			s.mu.Unlock()
			return dto.ConfigPreview{}, ErrBusy
		}
		pending = &previewOperation{key: key, done: make(chan struct{})}
		s.previewPending = pending
		epoch := s.previewEpoch
		s.operations.Add(1)
		go func() {
			defer s.operations.Done()
			probeCtx, cancel := context.WithTimeout(s.host.ctx, s.host.options.RequestTimeout)
			defer cancel()
			scope := fmt.Sprintf("acp-preview-%x", sha256.Sum256([]byte(key)))
			prepared, probeErr := s.prepareSession(probeCtx, scope, &def, true)
			s.mu.Lock()
			defer s.mu.Unlock()
			pending.err = probeErr
			pending.result = dto.ConfigPreview{EngineID: engineID, ConfigOptions: prepared.ConfigOptions}
			if probeErr == nil && s.previewEpoch == epoch && !s.closed {
				s.cachePreview(key, pending.result)
			}
			s.previewPending = nil
			close(pending.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return dto.ConfigPreview{}, ctx.Err()
	case <-pending.done:
		return clonePreview(pending.result), pending.err
	}
}

func clonePreview(p dto.ConfigPreview) dto.ConfigPreview {
	return dto.ConfigPreview{EngineID: p.EngineID, ConfigOptions: copyPrepared(dto.PreparedSession{ConfigOptions: p.ConfigOptions}).ConfigOptions}
}

// Called under s.mu. Discovery and a real new session share the same defaults;
// per-Chat selections and restored sessions must never populate this cache.
func (s *Sessions) previewKey(engineID, workspace string, def catalog.AgentDefinition) string {
	definition, _ := json.Marshal(def)
	directory := s.host.registry.Directory()
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%x", engineID, workspace, directory.CatalogGeneration, directory.Revision, s.previewEpoch, sha256.Sum256(definition))
}

func (s *Sessions) cachePreview(key string, result dto.ConfigPreview) {
	if s.previews == nil {
		s.previews = map[string]configPreview{}
	}
	// TTL controls refresh, not display. Keep stale defaults available while
	// discovery waits for spare capacity; evict only the oldest bounded entry.
	if _, exists := s.previews[key]; !exists && len(s.previews) >= 128 {
		var oldestKey string
		var oldest time.Time
		for k, entry := range s.previews {
			if oldest.IsZero() || entry.at.Before(oldest) {
				oldestKey, oldest = k, entry.at
			}
		}
		delete(s.previews, oldestKey)
	}
	s.previews[key] = configPreview{result: clonePreview(result), at: time.Now()}
}
