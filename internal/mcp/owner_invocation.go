package mcp

import (
	"agent-platform/internal/connector"
	"context"
	"fmt"
	"sync"
	"time"
)

type ownerInvocation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *Client) beginOwnerInvocation(ctx context.Context, key string) (context.Context, func(), error) {
	if c == nil || c.owner != "" || c.registry == nil {
		return ctx, func() {}, nil
	}
	owner := connector.OwnerFromContext(ctx)
	if owner == "" {
		return ctx, func() {}, nil
	}
	server, ok := c.registry.Server(key)
	if !ok || server.ConnectorPackage == nil || server.ConnectorPackage.Builtin {
		return ctx, func() {}, nil
	}
	pkg := *server.ConnectorPackage
	pkg.Owner = owner
	c.mu.Lock()
	state, err := pkg.ReadConnection()
	if err != nil {
		c.mu.Unlock()
		return ctx, nil, err
	}
	if !state.Bound || !state.Enabled {
		c.mu.Unlock()
		c.dropOwnerServer(owner, key)
		return ctx, nil, fmt.Errorf("connector_disabled")
	}
	scoped, cancel := context.WithCancel(ctx)
	job := &ownerInvocation{cancel: cancel, done: make(chan struct{})}
	id := owner + "\x00" + pkg.ID
	if c.activeOwnerCalls == nil {
		c.activeOwnerCalls = map[string]map[*ownerInvocation]struct{}{}
	}
	if c.activeOwnerCalls[id] == nil {
		c.activeOwnerCalls[id] = map[*ownerInvocation]struct{}{}
	}
	c.activeOwnerCalls[id][job] = struct{}{}
	c.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() { cancel(); c.mu.Lock(); delete(c.activeOwnerCalls[id], job); c.mu.Unlock(); close(job.done) })
	}
	return scoped, release, nil
}
func (c *Client) stopOwnerInvocations(owner, id string) error {
	c.mu.Lock()
	var jobs []*ownerInvocation
	for job := range c.activeOwnerCalls[owner+"\x00"+id] {
		jobs = append(jobs, job)
		job.cancel()
	}
	c.mu.Unlock()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for _, job := range jobs {
		select {
		case <-job.done:
		case <-timer.C:
			return fmt.Errorf("MCP connector operations are still stopping")
		}
	}
	return nil
}
