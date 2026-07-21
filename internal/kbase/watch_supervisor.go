package kbase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	runtimewatch "agent-platform/internal/watch"
)

type deltaAccumulator struct {
	mu        sync.Mutex
	paths     map[string]struct{}
	reconcile bool
}

func newDeltaAccumulator() *deltaAccumulator {
	return &deltaAccumulator{paths: map[string]struct{}{}}
}

func (a *deltaAccumulator) Add(path string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reconcile {
		return
	}
	a.paths[normalizeIndexedPath(path)] = struct{}{}
	if len(a.paths) > maxDeltaPaths {
		a.reconcile = true
		a.paths = map[string]struct{}{}
	}
}

func (a *deltaAccumulator) RequireReconcile() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.reconcile = true
	a.paths = map[string]struct{}{}
	a.mu.Unlock()
}

func (a *deltaAccumulator) Drain() ([]string, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	paths := make([]string, 0, len(a.paths))
	for path := range a.paths {
		paths = append(paths, path)
	}
	reconcile := a.reconcile
	a.paths = map[string]struct{}{}
	a.reconcile = false
	return compactChangedPaths(paths), reconcile
}

type watcherBinding struct {
	watcher    *runtimewatch.Watcher
	cancel     context.CancelFunc
	signature  string
	agentKey   string
	storageDir string
	changes    *deltaAccumulator
}

type watchSupervisor struct {
	debounce time.Duration
	resolver *capabilityResolver
	refresh  *refreshCoordinator

	mu          sync.Mutex
	reconcileMu sync.Mutex
	context     context.Context
	bindings    map[string]watcherBinding
}

func newWatchSupervisor(debounce time.Duration, resolver *capabilityResolver, refresh *refreshCoordinator) *watchSupervisor {
	return &watchSupervisor{debounce: debounce, resolver: resolver, refresh: refresh, bindings: map[string]watcherBinding{}}
}

func (s *watchSupervisor) Start(ctx context.Context) {
	s.mu.Lock()
	for key, binding := range s.bindings {
		if binding.cancel != nil {
			binding.cancel()
		}
		delete(s.bindings, key)
	}
	s.context = ctx
	s.mu.Unlock()
	s.ensure(ctx)
}

func (s *watchSupervisor) Stop() {
	s.mu.Lock()
	for key, binding := range s.bindings {
		if binding.cancel != nil {
			binding.cancel()
		}
		delete(s.bindings, key)
	}
	s.mu.Unlock()
}

func (s *watchSupervisor) Reconcile(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.context == nil && ctx != nil {
		s.context = ctx
	}
	watchContext := s.context
	s.mu.Unlock()
	if watchContext != nil {
		ctx = watchContext
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.ensure(ctx)
}

func (s *watchSupervisor) ensure(ctx context.Context) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	desired := map[string]AgentSpec{}
	for _, spec := range s.resolver.Specs() {
		spec.Key = strings.TrimSpace(spec.Key)
		spec.Config.Source.Root = strings.TrimSpace(spec.Config.Source.Root)
		if spec.Key != "" && spec.Config.Source.Root != "" {
			desired[spec.Key] = spec
		}
	}

	s.mu.Lock()
	var released []watcherBinding
	for key, binding := range s.bindings {
		spec, ok := desired[key]
		if ok && binding.signature == watcherSignature(spec) {
			delete(desired, key)
			continue
		}
		if binding.cancel != nil {
			binding.cancel()
		}
		if !ok || storageLockKey(binding.storageDir) != storageLockKey(s.resolver.StorageDirForSpec(spec)) {
			released = append(released, binding)
		}
		delete(s.bindings, key)
	}
	s.mu.Unlock()
	for _, binding := range released {
		s.refresh.DropStorage(binding.agentKey, binding.storageDir)
	}
	for _, spec := range desired {
		s.startWatcher(ctx, spec)
	}
}

func (s *watchSupervisor) startWatcher(ctx context.Context, spec AgentSpec) {
	agentKey := strings.TrimSpace(spec.Key)
	workspace := strings.TrimSpace(spec.Config.Source.Root)
	debounce := s.debounce
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	watchCtx, cancel := context.WithCancel(ctx)
	changes := newDeltaAccumulator()
	matchers := compileMatchers(append(DefaultExcludePatterns(), spec.Config.Exclude...))
	watcher, err := runtimewatch.Start(watchCtx, runtimewatch.Spec{
		LogPrefix: "[kbase]",
		Roots: []runtimewatch.Root{{Path: workspace, Label: agentKey, Recursive: true,
			ShouldTraverse: func(path string) bool { return !shouldSkipDirName(filepath.Base(path)) }}},
		Debounce: debounce,
		Ignore: func(path string) bool {
			rel, err := filepath.Rel(workspace, path)
			if err != nil {
				return true
			}
			rel = filepath.ToSlash(rel)
			return matchesAny(matchers, rel) || strings.HasPrefix(filepath.Base(path), ".DS_Store")
		},
		OnEvent: func(event runtimewatch.Event) {
			rel, err := filepath.Rel(workspace, event.Path)
			if err == nil && !strings.HasPrefix(rel, "..") {
				changes.Add(filepath.ToSlash(rel))
			}
		},
		OnDebounce: func(context.Context) error {
			paths, reconcile := changes.Drain()
			if len(paths) > 0 || reconcile {
				s.refresh.QueueDelta(agentKey, s.resolver.StorageDirForSpec(spec), paths, reconcile)
			}
			return nil
		},
		OnError: func(error) {
			changes.RequireReconcile()
			paths, reconcile := changes.Drain()
			s.refresh.QueueDelta(agentKey, s.resolver.StorageDirForSpec(spec), paths, reconcile)
		},
	})
	if err != nil {
		cancel()
		log.Printf("[kbase] watcher disabled agent=%s workspace=%s: %v", agentKey, workspace, err)
		return
	}
	s.mu.Lock()
	if existing, ok := s.bindings[agentKey]; ok && existing.cancel != nil {
		existing.cancel()
	}
	s.bindings[agentKey] = watcherBinding{
		watcher: watcher, cancel: cancel, signature: watcherSignature(spec), changes: changes,
		agentKey: agentKey, storageDir: s.resolver.StorageDirForSpec(spec),
	}
	s.mu.Unlock()
}

func watcherSignature(spec AgentSpec) string {
	payload := struct {
		SourceRoot      string
		StorageLocation string
		Exclude         []string
	}{
		SourceRoot:      strings.TrimSpace(spec.Config.Source.Root),
		StorageLocation: strings.ToLower(strings.TrimSpace(spec.Config.Storage.Location)),
		Exclude:         append([]string(nil), spec.Config.Exclude...),
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
