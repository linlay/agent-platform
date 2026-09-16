package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"errors"
	"time"
)

type cliStatusCheck struct {
	done       chan struct{}
	cancel     context.CancelFunc
	waiters    int
	generation uint64
	session    *login
	result     Session
	err        error
}

// Only concurrent reads for this Manager's owner/id share a live subprocess.
// Completed results are never cached: the next read checks the actual CLI again.
func (m *Manager) sharedCLIStatus(ctx context.Context, id string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	if m.loggingOut[id] || m.disconnecting[id] {
		m.mu.Unlock()
		return Session{}, connector.ErrBusy
	}
	if session := m.sessions[id]; session != nil && session.Status != "authorized" {
		result := session.Session
		m.mu.Unlock()
		return result, nil
	}
	if m.cliStatusChecks == nil {
		m.cliStatusChecks = map[string]*cliStatusCheck{}
	}
	job := m.cliStatusChecks[id]
	if job == nil {
		probeCtx, cancel := context.WithTimeout(m.ctx, 22*time.Second)
		job = &cliStatusCheck{done: make(chan struct{}), cancel: cancel, generation: m.epochs[id], session: m.sessions[id]}
		m.cliStatusChecks[id] = job
		go func(job *cliStatusCheck) {
			defer cancel()
			job.result, job.err = m.probeCLIStatus(probeCtx, id)
			m.mu.Lock()
			if m.epochs[id] != job.generation || m.sessions[id] != job.session {
				job.result = Session{}
				job.err = connector.ErrBusy
			}
			if m.cliStatusChecks[id] == job {
				delete(m.cliStatusChecks, id)
			}
			close(job.done)
			m.mu.Unlock()
		}(job)
	}
	job.waiters++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		job.waiters--
		if job.waiters == 0 && m.cliStatusChecks[id] == job {
			delete(m.cliStatusChecks, id)
			job.cancel()
		}
		m.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return Session{}, ctx.Err()
	case <-job.done:
		return job.result, job.err
	}
}

func (m *Manager) probeCLIStatus(ctx context.Context, id string) (Session, error) {
	// PreparationStatus has a brief read/verification lock. Wait for such readers,
	// while keeping the same OS mutation exclusion used by import/prepare/auth.
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	release, err := acquireStatusOperation(lockCtx, m.sources.ExternalRoot, id)
	if err != nil {
		return Session{}, err
	}
	defer release()
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	result := Session{ConnectorID: id, AuthBrowser: pkg.AuthorizationBrowser(), Status: "unauthorized"}
	authorized, err := m.cliStatus(ctx, pkg)
	if err != nil {
		result.Status = "setup_required"
		result.Message = err.Error()
	} else if authorized {
		result.Status = "authorized"
	}
	return result, nil
}
func acquireStatusOperation(ctx context.Context, root, id string) (func(), error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, connector.ErrBusy
			}
			return nil, err
		}
		release, err := connector.AcquireOperation(root, id)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, connector.ErrBusy) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, connector.ErrBusy
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (m *Manager) cancelCLIStatus(id string) error {
	m.mu.Lock()
	job := m.cliStatusChecks[id]
	if job != nil {
		job.cancel()
	}
	m.mu.Unlock()
	if job == nil {
		return nil
	}
	select {
	case <-job.done:
		return nil
	case <-time.After(5 * time.Second):
		return connector.ErrBusy
	}
}
