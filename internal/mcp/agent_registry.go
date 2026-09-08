package mcp

import (
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
)

type AgentConnectorSource interface {
	ConnectorRuntimes() []connector.AgentRuntime
}

// NewAgentRegistry validates source definitions synchronously. Transports are
// created only for Agent-mounted instances, after the catalog is assembled.
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
	packages, err := r.sources.LoadAll()
	if err != nil {
		return nil, err
	}
	if err := ValidateConnectorPackages(packages); err != nil {
		return nil, err
	}
	sourceDirs := map[string]string{}
	for _, pkg := range packages {
		sourceDirs[pkg.ID] = pkg.Dir
	}
	r.mu.RLock()
	agents := r.agents
	r.mu.RUnlock()
	servers := map[string]ServerDefinition{}
	if agents == nil {
		return servers, nil
	}
	for _, mount := range agents.ConnectorRuntimes() {
		if mount.AgentKey == "" || filepath.Base(mount.Dir) != mount.ID {
			return nil, fmt.Errorf("invalid Agent connector mount")
		}
		pkg, err := connector.Load(filepath.Dir(mount.Dir), mount.ID)
		if err != nil {
			return nil, err
		}
		pkg.StateRoot = r.sources.PersistentRoot()
		if len(pkg.MCP) == 0 {
			continue
		}
		digest, err := connector.RuntimeFingerprint(pkg.Dir)
		if err != nil {
			return nil, err
		}
		for name := range pkg.MCP {
			server, err := connectorServer(pkg, name)
			if err != nil {
				return nil, err
			}
			if !server.Enabled() {
				continue
			}
			server.SourceKey = server.Key
			server.AgentKey = mount.AgentKey
			server.RuntimeDigest = digest
			server.Key = connector.AgentServerKey(mount.AgentKey, server.SourceKey)
			if server.Transport == TransportStdio {
				// Explicit paths into the original package must also use the Agent
				// copy. Truly external commands retained by migration stay explicit.
				server.Command = rebasePackagePath(server.Command, sourceDirs[pkg.ID], pkg.Dir)
				server.WorkingDir = rebasePackagePath(server.WorkingDir, sourceDirs[pkg.ID], pkg.Dir)
			}
			if _, exists := servers[server.Key]; exists {
				return nil, fmt.Errorf("duplicate Agent MCP instance")
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
