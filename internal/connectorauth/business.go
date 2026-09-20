package connectorauth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type businessInvocation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *Manager) WithDisconnectHandler(handler func(context.Context, string) error) *Manager {
	m.disconnectHandler = handler
	return m
}
func (m *Manager) beginBusiness(ctx context.Context, id string) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disconnecting[id] {
		return ctx, nil, fmt.Errorf("connector disconnect is in progress")
	}
	pkg, err := m.sources.Load(id)
	if err != nil {
		return ctx, nil, err
	}
	state, err := pkg.ReadConnection()
	if err != nil {
		return ctx, nil, err
	}
	if !state.Bound || !state.Enabled {
		return ctx, nil, fmt.Errorf("connector_disabled")
	}
	child, cancel := context.WithCancel(ctx)
	job := &businessInvocation{cancel: cancel, done: make(chan struct{})}
	if m.business == nil {
		m.business = map[string]map[*businessInvocation]struct{}{}
	}
	if m.business[id] == nil {
		m.business[id] = map[*businessInvocation]struct{}{}
	}
	m.business[id][job] = struct{}{}
	var once sync.Once
	release := func() {
		once.Do(func() { cancel(); m.mu.Lock(); delete(m.business[id], job); m.mu.Unlock(); close(job.done) })
	}
	return child, release, nil
}
func (m *Manager) cancelBusiness(id string) error {
	m.mu.Lock()
	var jobs []*businessInvocation
	for job := range m.business[id] {
		jobs = append(jobs, job)
		job.cancel()
	}
	m.mu.Unlock()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for _, job := range jobs {
		select {
		case <-job.done:
		case <-timer.C:
			return fmt.Errorf("connector operations are still stopping")
		}
	}
	return nil
}

// BeginBusiness tracks operations so disconnect cancels and waits for completion.
func (m *Manager) BeginBusiness(ctx context.Context, id string) (context.Context, func(), error) {
	return m.beginBusiness(ctx, id)
}
