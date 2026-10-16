package mcp

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
)

type AgentConnectorSource interface {
	ConnectorRuntimes() []connector.AgentRuntime
}

// NewAgentRegistry validates source definitions synchronously, warning and
// skipping invalid connectors. Transports are created only for Agent mounts.
func NewAgentRegistry(sources connector.Sources) (*Registry, error) {
	r := &Registry{sources: sources, agentScoped: true, servers: map[string]ServerDefinition{}}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) BindAgents(source AgentConnectorSource) error {
	r.mu.Lock()
	r.agents = source
	r.mu.Unlock()
	return r.Reload()
}

func (r *Registry) loadAgentServers() (map[string]ServerDefinition, error) {
	if _, err := loadConnectorServers(r.sources); err != nil {
		return nil, err
	}
	r.mu.RLock()
	agents := r.agents
	r.mu.RUnlock()
	servers := map[string]ServerDefinition{}
	if agents == nil {
		return servers, nil
	}
	for _, mount := range agents.ConnectorRuntimes() {
		if mount.AgentKey == "" || !connector.ValidID(mount.ID) {
			return nil, fmt.Errorf("invalid Agent connector mount")
		}
		pkg, err := connector.LoadDirectory(mount.Dir, mount.ID)
		if err != nil {
			log.Printf("[mcp] warning: skipping invalid Agent connector: agent=%s connector=%s: %v", mount.AgentKey, mount.ID, err)
			continue
		}
		pkg.StateRoot = r.sources.PersistentRoot()
		if len(pkg.MCP) == 0 {
			continue
		}
		digest, err := connector.RuntimeFingerprint(pkg.Dir)
		if err != nil {
			log.Printf("[mcp] warning: skipping unavailable Agent connector: agent=%s connector=%s: %v", mount.AgentKey, mount.ID, err)
			continue
		}
		components, err := connectorPackageServers(pkg)
		if err != nil {
			log.Printf("[mcp] warning: skipping invalid Agent connector: agent=%s connector=%s: %v", mount.AgentKey, mount.ID, err)
			continue
		}
		// Derive the source directory even when its current definition is invalid
		// or deleted: frozen last-good mounts must still rebase absolute paths.
		sourceDir := ""
		if root := r.sources.Root(pkg.ID); root != "" {
			sourceDir = filepath.Join(root, pkg.ID)
		}
		for _, server := range components {
			server.SourceKey = server.Key
			server.AgentKey = mount.AgentKey
			server.RuntimeDigest = digest
			server.RuntimePackageDir = pkg.Dir
			server.Key = connector.AgentVersionServerKey(mount.AgentKey, server.SourceKey, mount.Digest)
			if server.Transport == TransportStdio {
				// Explicit paths into the original package must also use the Agent
				// copy. Truly external commands retained by migration stay explicit.
				server.Command = rebasePackagePath(server.Command, sourceDir, pkg.Dir)
				server.WorkingDir = rebasePackagePath(server.WorkingDir, sourceDir, pkg.Dir)
			}
			if _, exists := servers[server.Key]; exists {
				continue
			}
			servers[server.Key] = server
		}
	}
	return servers, nil
}

func rebasePackagePath(path, source, runtime string) string {
	if source == "" {
		return path
	}
	rel, err := filepath.Rel(source, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join(runtime, rel)
	}
	return path
}
