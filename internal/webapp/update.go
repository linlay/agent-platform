package webapp

import (
	"context"
	"time"
)

func (s *Service) Install(ctx context.Context, req InstallRequest) (Entry, error) {
	review, err := s.Preflight(ctx, req.UploadID)
	if err != nil {
		return Entry{}, err
	}
	if review.SHA256 != req.SHA256 {
		return Entry{}, fail("webapp_upload_changed", 409)
	}
	if review.ExpectedRevision != req.ExpectedRevision {
		return Entry{}, fail("webapp_revision_conflict", 409)
	}
	id := review.Manifest.ID
	s.mu.Lock()
	a := s.active[id]
	transition := a != nil && (a.state == "stopping" || a.state == "unhealthy")
	s.mu.Unlock()
	if transition {
		return Entry{}, fail("webapp_update_requires_stop", 409)
	}
	control := s.control(id)
	control.Lock()
	defer control.Unlock()
	s.mu.Lock()
	if err := s.mutationError(); err != nil {
		s.mu.Unlock()
		return Entry{}, err
	}
	old := s.records[id]
	if old != nil && old.Revision != req.ExpectedRevision || old == nil && req.ExpectedRevision != "" {
		s.mu.Unlock()
		return Entry{}, fail("webapp_revision_conflict", 409)
	}
	previous := s.desired[id]
	resume := old != nil && previous.State == "running"
	s.installing[id] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.installing, id); s.mu.Unlock() }()
	if resume {
		_, err := s.stopLocked(ctx, id, StateRequest{}, true)
		if err != nil {
			return Entry{}, err
		}
	}
	var validate func(Entry) error
	if resume {
		validate = func(e Entry) error {
			s.mu.Lock()
			state := s.desired[id]
			s.mu.Unlock()
			if _, err := s.start(ctx, id, StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: state.Revision}); err == nil {
				if err = s.waitReady(ctx, id); err == nil {
					return nil
				}
			}
			cleanup, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			if _, err := s.stopLocked(cleanup, id, StateRequest{}, true); err != nil {
				return fail("webapp_stop_incomplete", 503)
			}
			return fail("webapp_update_health_failed", 503)
		}
	}
	result, err := s.install(ctx, req, resume, validate)
	if err != nil && resume {
		s.mu.Lock()
		safe := !s.recoveryRequired && !s.closing && s.records[id] != nil && s.records[id].Revision == old.Revision
		state := s.desired[id]
		s.mu.Unlock()
		if safe {
			_, restoreErr := s.start(context.Background(), id, StateRequest{ExpectedRevision: old.Revision, ExpectedStateRevision: state.Revision})
			if restoreErr != nil {
				return Entry{}, fail("webapp_update_rollback_start_failed", 503)
			}
		}
	}
	if err == nil {
		s.mu.Lock()
		delete(s.installing, id)
		result = s.entry(s.records[id])
		s.mu.Unlock()
	}
	return result, err
}
func (s *Service) waitReady(ctx context.Context, id string) error {
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		s.mu.Lock()
		a := s.active[id]
		state := ""
		if a != nil {
			state = a.state
		}
		s.mu.Unlock()
		if state == "running" {
			return nil
		}
		if state != "starting" {
			return fail("webapp_update_health_failed", 503)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
