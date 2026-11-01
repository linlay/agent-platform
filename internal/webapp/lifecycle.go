package webapp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type activeApp struct {
	state, generation string
	ctx               context.Context
	cancel            context.CancelFunc
	leases            sync.WaitGroup
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
		if a.state == "running" {
			return s.entry(r), nil
		}
		return Entry{}, fail("webapp_transition_in_progress", 409)
	}
	if request.ExpectedStateRevision != s.desired[id].Revision {
		return Entry{}, fail("webapp_revision_conflict", 409)
	}
	if r.Manifest.Backend != nil {
		return Entry{}, fail("webapp_backend_unavailable", 503)
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
	appCtx, cancel := context.WithCancel(context.Background())
	s.active[id] = &activeApp{state: "running", generation: randomID(), ctx: appCtx, cancel: cancel}
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
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { a.leases.Wait(); close(done) }()
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
	return &Scope{Entry: s.entry(r), Manifest: r.Manifest, Policy: policy, UserConfig: values, CodeRoot: filepath.Join(s.root, id, "app"), DataRoot: filepath.Join(s.dataRoot, id), Generation: a.generation, Context: a.ctx, Release: func() { once.Do(a.leases.Done) }, Check: func() error { return s.Check(id, a.generation, policy.Revision) }}, nil
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
	for _, id := range ids {
		wg.Add(1)
		go func(id string) { defer wg.Done(); _, _ = s.stop(ctx, id, StateRequest{}, true) }(id)
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.lock.Close()
}

// RestoreStatic publishes only previously running static apps. Node recovery
// is wired after its internal HTTP bridge and runner become available.
func (s *Service) RestoreStatic(ctx context.Context) error {
	s.mu.Lock()
	requests := map[string]StateRequest{}
	for id, r := range s.records {
		if r.Manifest.Backend == nil && s.desired[id].State == "running" {
			requests[id] = StateRequest{ExpectedRevision: r.Revision, ExpectedStateRevision: s.desired[id].Revision}
		}
	}
	s.mu.Unlock()
	for id, req := range requests {
		if _, err := s.Start(ctx, id, req); err != nil {
			return err
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
