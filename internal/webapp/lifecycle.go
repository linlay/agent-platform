package webapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type activeApp struct {
	state, generation string
	ctx               context.Context
	cancel            context.CancelFunc
	leases            sync.WaitGroup
	root              context.Context
	stop              context.CancelFunc
	done              chan struct{}
	clean             bool
	process           Process
	lastError         string
	restartCount      int
	exitCode          *int
}
type StateRequest struct {
	ExpectedRevision      string `json:"expectedRevision"`
	ExpectedStateRevision string `json:"expectedStateRevision"`
}

// Scope is an internal lease, never a public response or a request DTO.
type Scope struct {
	Entry              Entry
	Manifest           Manifest
	Policy             Policy
	UserConfig         map[string]any
	CodeRoot, DataRoot string
	BackendAddress     string
	Generation         string
	Context            context.Context
	Check              func() error
	Release            func()
}

func (s *Service) control(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.controls[id]; l != nil {
		return l
	}
	l := &sync.Mutex{}
	s.controls[id] = l
	return l
}
func (s *Service) Start(ctx context.Context, id string, request StateRequest) (Entry, error) {
	if !idPattern.MatchString(id) {
		return Entry{}, fail("webapp_not_found", 404)
	}
	control := s.control(id)
	control.Lock()
	defer control.Unlock()
	return s.start(ctx, id, request)
}
func (s *Service) start(ctx context.Context, id string, request StateRequest) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return Entry{}, err
	}
	r := s.records[id]
	if r == nil {
		return Entry{}, fail("webapp_not_found", 404)
	}
	if request.ExpectedRevision != r.Revision {
		return Entry{}, fail("webapp_revision_conflict", 409)
	}
	if a := s.active[id]; a != nil {
		if a.state == "running" || a.state == "starting" || a.state == "backoff" {
			return s.entry(r), nil
		}
		if !a.clean {
			return Entry{}, fail("webapp_transition_in_progress", 409)
		}
		delete(s.active, id)
	}
	if request.ExpectedStateRevision != s.desired[id].Revision {
		return Entry{}, fail("webapp_revision_conflict", 409)
	}
	if r.Manifest.Backend != nil && (s.runner == nil || !s.runner.Ready()) {
		return Entry{}, fail("webapp_backend_unavailable", 503)
	}
	running := 0
	for _, a := range s.active {
		if a.state != "failed" && a.state != "blocked" {
			running++
		}
	}
	if running >= s.config.MaxRunning {
		return Entry{}, fail("webapp_max_running", 409)
	}
	if err := verifyInstalledCode(filepath.Join(s.root, id, "app"), *r); err != nil {
		return Entry{}, err
	}
	values, _, err := s.readUserConfig(id)
	if err != nil {
		return Entry{}, err
	}
	if err := validateUserValues(r.Manifest, values); err != nil {
		return Entry{}, err
	}
	if ctx.Err() != nil {
		return Entry{}, ctx.Err()
	}
	desired := Desired{SchemaVersion: 1, State: "running", Revision: randomID()}
	if err := writeJSON(filepath.Join(s.root, id, "desired.json"), desired); err != nil {
		return Entry{}, err
	}
	root, stop := context.WithCancel(context.Background())
	appCtx, cancel := context.WithCancel(root)
	a := &activeApp{state: "running", generation: randomID(), ctx: appCtx, cancel: cancel, root: root, stop: stop, done: make(chan struct{}), clean: true}
	s.active[id] = a
	if r.Manifest.Backend != nil {
		a.state = "starting"
		a.clean = false
		go s.runBackend(id, a, *r, s.policies[id])
	} else {
		close(a.done)
	}
	s.desired[id] = desired
	return s.entry(r), nil
}
func (s *Service) Stop(ctx context.Context, id string, request StateRequest) (Entry, error) {
	return s.stop(ctx, id, request, false)
}
func (s *Service) stop(ctx context.Context, id string, request StateRequest, preserve bool) (Entry, error) {
	if !idPattern.MatchString(id) {
		return Entry{}, fail("webapp_not_found", 404)
	}
	control := s.control(id)
	control.Lock()
	defer control.Unlock()
	return s.stopLocked(ctx, id, request, preserve)
}
func (s *Service) stopLocked(ctx context.Context, id string, request StateRequest, preserve bool) (Entry, error) {
	s.mu.Lock()
	r := s.records[id]
	if r == nil {
		s.mu.Unlock()
		return Entry{}, fail("webapp_not_found", 404)
	}
	if !preserve {
		if err := s.mutationError(); err != nil {
			s.mu.Unlock()
			return Entry{}, err
		}
		if r.Revision != request.ExpectedRevision {
			s.mu.Unlock()
			return Entry{}, fail("webapp_revision_conflict", 409)
		}
	}
	a := s.active[id]
	if !preserve && (s.desired[id].State != "stopped" || a != nil) {
		if request.ExpectedStateRevision != s.desired[id].Revision {
			s.mu.Unlock()
			return Entry{}, fail("webapp_revision_conflict", 409)
		}
		d := Desired{SchemaVersion: 1, State: "stopped", Revision: randomID()}
		if err := writeJSON(filepath.Join(s.root, id, "desired.json"), d); err != nil {
			s.mu.Unlock()
			return Entry{}, err
		}
		s.desired[id] = d
	}
	if a == nil {
		e := s.entry(r)
		s.mu.Unlock()
		return e, nil
	}
	a.state = "stopping"
	a.cancel()
	a.stop()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { <-a.done; a.leases.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		s.mu.Lock()
		a.state = "failed"
		s.mu.Unlock()
		return Entry{}, fail("webapp_stop_incomplete", 503)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !a.clean {
		if checker, ok := s.runner.(interface{ ConfirmStopped(string) bool }); ok && checker.ConfirmStopped(id) {
			a.clean = true
		}
	}
	if !a.clean {
		a.state = "failed"
		return Entry{}, fail("webapp_stop_incomplete", 503)
	}
	if s.active[id] == a {
		delete(s.active, id)
	}
	return s.entry(r), nil
}
func (s *Service) Acquire(id string) (*Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return nil, err
	}
	r := s.records[id]
	if r == nil {
		return nil, fail("webapp_not_found", 404)
	}
	a := s.active[id]
	if a == nil || a.state != "running" || a.ctx.Err() != nil {
		return nil, fail("webapp_not_running", 503)
	}
	values, _, err := s.readUserConfig(id)
	if err != nil {
		return nil, err
	}
	if err := validateUserValues(r.Manifest, values); err != nil {
		return nil, err
	}
	policy := s.policies[id]
	a.leases.Add(1)
	var once sync.Once
	address := ""
	if a.process != nil {
		address = a.process.Address()
	}
	return &Scope{BackendAddress: address, Entry: s.entry(r), Manifest: r.Manifest, Policy: policy, UserConfig: values, CodeRoot: filepath.Join(s.root, id, "app"), DataRoot: filepath.Join(s.dataRoot, id), Generation: a.generation, Context: a.ctx, Release: func() { once.Do(a.leases.Done) }, Check: func() error { return s.Check(id, a.generation, policy.Revision) }}, nil
}
func (s *Service) Check(id, generation, policyRevision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return err
	}
	a := s.active[id]
	if a == nil || a.state != "running" || a.generation != generation || a.ctx.Err() != nil || s.policies[id].Revision != policyRevision {
		return fail("app_grant_required", 403)
	}
	return nil
}
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	ids := []string{}
	for id := range s.active {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var errorsMu sync.Mutex
	var failures []error
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.stop(ctx, id, StateRequest{}, true)
			if err != nil {
				errorsMu.Lock()
				failures = append(failures, err)
				errorsMu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	s.closed = true
	return s.lock.Close()
}

// RestoreDesired never adopts recorded PIDs or endpoints; every generation
// is created through the same locked runner and health path as a fresh start.
func (s *Service) RestoreDesired(ctx context.Context) error {
	s.mu.Lock()
	requests := map[string]StateRequest{}
	for id, r := range s.records {
		if s.desired[id].State == "running" {
			requests[id] = StateRequest{ExpectedRevision: r.Revision, ExpectedStateRevision: s.desired[id].Revision}
		}
	}
	s.mu.Unlock()
	ids := make([]string, 0, len(requests))
	for id := range requests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		req := requests[id]
		if _, err := s.Start(ctx, id, req); err != nil {
			code, _ := ErrorCode(err)
			s.mu.Lock()
			root, stop := context.WithCancel(context.Background())
			stop()
			done := make(chan struct{})
			close(done)
			s.active[id] = &activeApp{state: "blocked", generation: randomID(), ctx: root, cancel: stop, root: root, stop: stop, done: done, clean: true, lastError: code}
			s.mu.Unlock()
		}
	}
	return nil
}

// CodeFile opens a file relative to the immutable frontend root with the Go
// root boundary, so later filesystem changes cannot turn a path into escape.
func (scope *Scope) CodeFile(name string) (*os.File, error) {
	if !safeRelative(name) {
		return nil, fail("webapp_path_invalid", 400)
	}
	root, err := os.OpenRoot(filepath.Join(scope.CodeRoot, scope.Manifest.Frontend.Root))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(filepath.FromSlash(name))
}

// RestoreStatic is retained as an internal compatibility name for fixtures.
func (s *Service) RestoreStatic(ctx context.Context) error { return s.RestoreDesired(ctx) }
func (s *Service) Restart(ctx context.Context, id string, req StateRequest) (Entry, error) {
	if !idPattern.MatchString(id) {
		return Entry{}, fail("webapp_not_found", 404)
	}
	l := s.control(id)
	l.Lock()
	defer l.Unlock()
	e, err := s.stopLocked(ctx, id, req, false)
	if err != nil {
		return Entry{}, err
	}
	return s.start(ctx, id, StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: e.StateRevision})
}
func (s *Service) runBackend(id string, a *activeApp, r Record, policy Policy) {
	defer close(a.done)
	var attempts []time.Time
	for {
		if a.root.Err() != nil {
			s.mu.Lock()
			a.clean = true
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		generation := a.generation
		appCtx := a.ctx
		s.mu.Unlock()
		spec := RuntimeSpec{ID: id, Generation: generation, CodeRoot: filepath.Join(s.root, id, "app"), DataRoot: filepath.Join(s.dataRoot, id), ManagementRoot: filepath.Join(s.root, id), Manifest: r.Manifest, Policy: policy, Context: appCtx, Check: func() error { return s.instanceCheck(id, generation, policy.Revision) }}
		spec.OnUnavailable = func(reason string) {
			s.mu.Lock()
			if s.active[id] == a && a.generation == generation && a.state != "stopping" {
				a.state = "unhealthy"
				a.lastError = reason
				a.cancel()
			}
			s.mu.Unlock()
		}

		p, err := s.runner.Start(appCtx, spec)
		clean := true
		reason := ""
		code := 0
		started := time.Now()
		if err == nil {
			s.mu.Lock()
			a.process = p
			if a.root.Err() == nil {
				a.state = "running"
				a.lastError = ""
			}
			s.mu.Unlock()
			select {
			case <-p.Done():
			case <-a.root.Done():
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			stopErr := p.Stop(stopCtx)
			cancel()
			result := p.Exit()
			clean = result.Clean && stopErr == nil
			reason = result.Reason
			code = result.Code
		} else {
			reason, _ = ErrorCode(err)
			if reason == "webapp_stop_incomplete" || reason == "webapp_previous_generation_alive" {
				clean = false
			}
		}
		s.mu.Lock()
		a.cancel()
		a.process = nil
		a.state = "backoff"
		a.exitCode = &code
		a.lastError = reason
		s.mu.Unlock()
		a.leases.Wait()
		s.mu.Lock()
		a.clean = clean
		if a.root.Err() != nil {
			s.mu.Unlock()
			return
		}
		if !clean || reason == "webapp_isolation_required" || reason == "webapp_backend_unavailable" {
			a.state = "blocked"
			s.mu.Unlock()
			return
		}
		now := time.Now()
		if now.Sub(started) >= time.Duration(s.config.Backend.Restart.StableResetSeconds)*time.Second {
			attempts = nil
		}
		cutoff := now.Add(-time.Duration(s.config.Backend.Restart.WindowSeconds) * time.Second)
		kept := attempts[:0]
		for _, t := range attempts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		attempts = kept
		if len(attempts) >= s.config.Backend.Restart.MaxAttempts {
			a.state = "failed"
			a.lastError = "webapp_restart_exhausted"
			s.mu.Unlock()
			return
		}
		attempts = append(attempts, now)
		a.restartCount++
		a.state = "backoff"
		s.mu.Unlock()
		delay := time.Second << min(len(attempts)-1, 5)
		timer := time.NewTimer(delay)
		select {
		case <-a.root.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.mu.Lock()
		if a.root.Err() != nil {
			s.mu.Unlock()
			return
		}
		a.generation = randomID()
		a.ctx, a.cancel = context.WithCancel(a.root)
		a.state = "starting"
		a.clean = false
		s.mu.Unlock()
	}
}
