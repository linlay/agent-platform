package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
)

// Each authenticated owner gets independent sessions, environment, transports,
// and availability backoff. Shared catalog definitions never contain credentials.
func (c *Client) forConnectorOwner(ctx context.Context, key string) (*Client, error) {
	if c == nil || c.registry == nil || c.owner != "" {
		return nil, nil
	}
	base, ok := c.registry.Server(key)
	if !ok || base.ConnectorPackage == nil || base.ConnectorPackage.Builtin {
		return nil, nil
	}
	owner := connector.OwnerFromContext(ctx)
	if owner == "" {
		// Anonymous discovery can inspect public definitions, but never a configured
		// account, OAuth session, identity adapter or private stdio process.
		if base.ConnectorPackage.AuthMode != connector.AuthDelegated || base.ConnectorPackage.ManagedCLI() || base.AuthSource != "" {
			return nil, fmt.Errorf("%w: connector owner is required", contracts.ErrMCPCallFailed)
		}
		return nil, nil
	}
	pkg := *base.ConnectorPackage
	pkg.Owner = owner
	connection, err := pkg.ReadConnection()
	if err != nil || !connection.Bound || !connection.Enabled {
		c.dropOwnerServer(owner, key)
		return nil, fmt.Errorf("%w: connector connection is disabled or unbound", contracts.ErrMCPCallFailed)
	}
	server, err := connectorServer(pkg, base.ConnectorComponent)
	if err != nil {
		return nil, err
	}
	server.Key, server.SourceKey, server.AgentKey, server.RuntimeDigest = base.Key, base.SourceKey, base.AgentKey, base.RuntimeDigest
	// Preserve the Agent runtime's rebased paths, not the original source paths.
	server.Command, server.WorkingDir = base.Command, base.WorkingDir
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: client is closed", contracts.ErrMCPCallFailed)
	}
	if c.ownerClients == nil {
		c.ownerClients = map[string]*Client{}
	}
	child := c.ownerClients[owner]
	if child == nil {
		registry := &Registry{sources: c.registry.sources, servers: map[string]ServerDefinition{}}
		var gate *AvailabilityGate
		if c.gate != nil {
			gate = NewAvailabilityGate()
		}
		child = NewClientWithGate(registry, c.httpClient, gate)
		child.ownerBaseFingerprints = map[string]string{}
		child.owner, child.identityFile = owner, c.identityFile
		c.ownerClients[owner] = child
	}
	c.mu.Unlock()
	child.registry.mu.Lock()
	previous, exists := child.registry.servers[server.Key]
	changed := !exists || serverFingerprint(previous) != serverFingerprint(server)
	child.registry.servers[server.Key] = server
	child.ownerBaseFingerprints[server.Key] = serverFingerprint(base)
	child.registry.mu.Unlock()
	if changed {
		child.gate.MarkSuccess(server.Key)
		child.Reconcile()
	}
	return child, nil
}

func (c *Client) dropOwnerServer(owner, key string) {
	c.mu.Lock()
	child := c.ownerClients[owner]
	c.mu.Unlock()
	if child == nil {
		return
	}
	child.registry.mu.Lock()
	delete(child.registry.servers, normalizeKey(key))
	child.registry.mu.Unlock()
	child.Reconcile()
}

func (c *Client) reconcileOwnerClients() {
	if c == nil || c.registry == nil || c.owner != "" {
		return
	}
	c.mu.Lock()
	children := make([]*Client, 0, len(c.ownerClients))
	for _, child := range c.ownerClients {
		children = append(children, child)
	}
	c.mu.Unlock()
	for _, child := range children {
		for _, current := range child.registry.Servers() {
			base, exists := c.registry.Server(current.Key)
			var next ServerDefinition
			valid := false
			if exists && base.ConnectorPackage != nil {
				pkg := *base.ConnectorPackage
				pkg.Owner = child.owner
				state, err := pkg.ReadConnection()
				if err == nil && state.Bound && state.Enabled {
					next, err = connectorServer(pkg, base.ConnectorComponent)
					if err == nil {
						next.Key, next.SourceKey, next.AgentKey, next.RuntimeDigest = base.Key, base.SourceKey, base.AgentKey, base.RuntimeDigest
						next.Command, next.WorkingDir = base.Command, base.WorkingDir
						valid = true
					}
				}
			}
			child.registry.mu.Lock()
			if valid {
				child.registry.servers[current.Key] = next
				child.ownerBaseFingerprints[current.Key] = serverFingerprint(base)
			} else {
				delete(child.registry.servers, current.Key)
			}
			child.registry.mu.Unlock()
		}
		child.Reconcile()
	}
}

// DiscoverOwnerTools returns only request-local definitions. It never publishes
// one user's account-specific tools or descriptions into the global ToolSync.
// An unavailable connector is skipped so ordinary chat remains usable.
func (c *Client) DiscoverOwnerTools(ctx context.Context, serverKeys []string) ([]api.ToolDetailResponse, error) {
	if connector.OwnerFromContext(ctx) == "" {
		return nil, fmt.Errorf("connector owner is required")
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	syncer := &ToolSync{client: c}
	tools := map[string]api.ToolDetailResponse{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := make(chan struct{}, 4)
	for _, key := range serverKeys {
		server, ok := c.registry.Server(key)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(server ServerDefinition) {
			defer wg.Done()
			select {
			case workers <- struct{}{}:
			case <-discoveryCtx.Done():
				return
			}
			defer func() { <-workers }()
			snapshot, err := syncer.syncServer(discoveryCtx, server)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for name, definition := range snapshot.toolsByName {
				tools[name] = definition
			}
		}(server)
	}
	wg.Wait()
	return cloneSortedToolDefinitions(tools), nil
}

// DisconnectOwnerConnector closes this owner's live SDK sessions immediately.
// Connection state is already disabled by the lifecycle coordinator before this
// hook runs, so a concurrent new call cannot recreate the owner's connection.
func (c *Client) DisconnectOwnerConnector(ctx context.Context, owner, id string) error {
	if err := c.stopOwnerInvocations(owner, id); err != nil {
		return err
	}
	c.mu.Lock()
	child := c.ownerClients[owner]
	c.mu.Unlock()
	if child == nil {
		return nil
	}
	child.registry.mu.Lock()
	for key, server := range child.registry.servers {
		if server.ConnectorID == id {
			delete(child.registry.servers, key)
		}
	}
	child.registry.mu.Unlock()
	child.Reconcile()
	return nil
}
