package acp

import "time"

// ACP has no portable acknowledgement that background work has drained. Once
// execution/unknown tools were used we pin the session; end_turn alone is not proof of quiescence.
func idleReclaimable(e *session) bool {
	return e.childDefinition == nil && e.readyState && e.process != nil && !e.backgroundPossible && len(e.pendingTools) == 0 && e.pendingInteractions == 0 &&
		(e.binding.ContextState == "prepared" || (e.binding.ContextState == "persisted" && e.process.worker.reusable))
}

// Select the least recently used safe session. Operation locks protect prompts,
// configuration changes and approvals. Network cleanup runs outside the map lock.
func (s *Sessions) releaseIdleSession(before time.Time) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	var chosen *session
	var chatID string
	var oldest time.Time
	for id, e := range s.entries {
		if !e.op.TryLock() {
			continue
		}
		e.mu.Lock()
		eligible := idleReclaimable(e) && (before.IsZero() || e.lastUsed.Before(before))
		used := e.lastUsed
		e.mu.Unlock()
		if eligible && (chosen == nil || used.Before(oldest)) {
			if chosen != nil {
				chosen.op.Unlock()
			}
			chosen, chatID, oldest = e, id, used
		} else {
			e.op.Unlock()
		}
	}
	if chosen == nil {
		s.mu.Unlock()
		return ErrBusy
	}
	s.operations.Add(1)
	s.mu.Unlock()
	defer s.release(chatID, chosen)
	chosen.mu.Lock()
	p := chosen.process
	chosen.readyState = false
	chosen.mu.Unlock()
	err := p.close(true)
	chosen.mu.Lock()
	chosen.process = nil
	chosen.mu.Unlock()
	return err
}

func (s *Sessions) reapIdle() {
	defer close(s.reaperDone)
	interval := s.host.options.IdleTimeout / 2
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.host.ctx.Done():
			return
		case now := <-ticker.C:
			for s.releaseIdleSession(now.Add(-s.host.options.IdleTimeout)) == nil {
			}
			s.closeIdleWorkers(now.Add(-s.host.options.IdleTimeout))
		}
	}
}

func (s *Sessions) closeIdleWorkers(before time.Time) bool {
	s.workersMu.Lock()
	var selected []*engineWorker
	for _, w := range s.workers {
		// A worker establishing a new session is never an idle candidate.
		select {
		case w.setup <- struct{}{}:
		default:
			continue
		}
		w.mu.Lock()
		eligible := w.process != nil && len(w.handles) == 0 && !w.closing && (before.IsZero() || w.idleSince.Before(before))
		if eligible {
			w.closing = true
			selected = append(selected, w)
		}
		w.mu.Unlock()
		<-w.setup
	}
	s.workersMu.Unlock()
	for _, w := range selected {
		w.process.Close()
		s.forgetWorker(w)
	}
	return len(selected) > 0
}

// Only standard foreground tool kinds can become idle after a terminal update.
// Execute/other and unknown extensions may own background processes after end_turn.
// Called with the session mutex held; tracking stays bounded for malformed peers.
func (e *session) trackTool(id, kind, status string, created bool) {
	if e.backgroundPossible {
		return
	}
	safe := e.pendingTools[id]
	switch kind {
	case "read", "edit", "delete", "move", "search", "fetch", "think":
		safe = true
	case "":
		if created {
			safe = false
		}
	default:
		safe = false
	}
	if !safe || id == "" || len(e.pendingTools) >= 1024 {
		e.backgroundPossible = true
		e.pendingTools = nil
		return
	}
	if status == "completed" || status == "failed" {
		delete(e.pendingTools, id)
		return
	}
	if e.pendingTools == nil {
		e.pendingTools = map[string]bool{}
	}
	e.pendingTools[id] = true
}
