package mcp

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"

	"agent-platform-runner-go/internal/api"
)

type ToolSync struct {
	registry *Registry
	client   *Client

	mu               sync.RWMutex
	toolsByName      map[string]api.ToolDetailResponse
	aliasToCanonical map[string]string
	snapshots        map[string]serverToolSnapshot
}

type serverToolSnapshot struct {
	toolsByName      map[string]api.ToolDetailResponse
	aliasToCanonical map[string]string
}

func NewToolSync(registry *Registry, client *Client) *ToolSync {
	return &ToolSync{
		registry:         registry,
		client:           client,
		toolsByName:      map[string]api.ToolDetailResponse{},
		aliasToCanonical: map[string]string{},
		snapshots:        map[string]serverToolSnapshot{},
	}
}

func (s *ToolSync) Load(ctx context.Context) ([]api.ToolDetailResponse, error) {
	return s.refreshTools(ctx, nil)
}

func (s *ToolSync) RefreshServer(ctx context.Context, serverKey string) ([]api.ToolDetailResponse, error) {
	return s.refreshTools(ctx, map[string]struct{}{normalizeLookup(serverKey): {}})
}

func (s *ToolSync) RefreshServers(ctx context.Context, serverKeys []string) ([]api.ToolDetailResponse, error) {
	targets := map[string]struct{}{}
	for _, key := range serverKeys {
		if normalized := normalizeLookup(key); normalized != "" {
			targets[normalized] = struct{}{}
		}
	}
	return s.refreshTools(ctx, targets)
}

func (s *ToolSync) Definitions() []api.ToolDetailResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSortedToolDefinitions(s.toolsByName)
}

func (s *ToolSync) Tool(name string) (api.ToolDetailResponse, bool) {
	if s == nil {
		return api.ToolDetailResponse{}, false
	}
	normalized := normalizeLookup(name)
	if normalized == "" {
		return api.ToolDetailResponse{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tool, ok := s.toolsByName[normalized]; ok {
		return cloneTool(tool), true
	}
	if canonical := s.aliasToCanonical[normalized]; canonical != "" {
		tool, ok := s.toolsByName[canonical]
		if ok {
			return cloneTool(tool), true
		}
	}
	return api.ToolDetailResponse{}, false
}

func (s *ToolSync) ResolveAlias(name string) (string, bool) {
	normalized := normalizeLookup(name)
	if normalized == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	canonical, ok := s.aliasToCanonical[normalized]
	return canonical, ok
}

func (s *ToolSync) refreshTools(ctx context.Context, targets map[string]struct{}) ([]api.ToolDetailResponse, error) {
	servers := s.registry.Servers()
	activeKeys := make(map[string]struct{}, len(servers))
	s.mu.RLock()
	nextSnapshots := make(map[string]serverToolSnapshot, len(s.snapshots))
	for key, snapshot := range s.snapshots {
		nextSnapshots[key] = cloneSnapshot(snapshot)
	}
	s.mu.RUnlock()

	for _, server := range servers {
		serverKey := normalizeLookup(server.Key)
		activeKeys[serverKey] = struct{}{}
		if len(targets) > 0 {
			if _, ok := targets[serverKey]; !ok {
				continue
			}
		}
		snapshot, err := s.syncServer(ctx, server)
		if err != nil {
			log.Printf("[mcp] failed to sync server %q: %v", server.Key, err)
			continue
		}
		nextSnapshots[serverKey] = snapshot
	}
	for key := range nextSnapshots {
		if _, ok := activeKeys[key]; !ok {
			delete(nextSnapshots, key)
		}
	}

	toolsByName, aliasToCanonical := mergeSnapshots(servers, nextSnapshots)
	s.mu.Lock()
	s.snapshots = nextSnapshots
	s.toolsByName = toolsByName
	s.aliasToCanonical = aliasToCanonical
	s.mu.Unlock()
	return cloneSortedToolDefinitions(toolsByName), nil
}

func (s *ToolSync) syncServer(ctx context.Context, server ServerDefinition) (serverToolSnapshot, error) {
	if err := s.client.Initialize(ctx, server.Key); err != nil {
		return serverToolSnapshot{}, err
	}
	discovered, err := s.client.ListTools(ctx, server.Key)
	if err != nil {
		return serverToolSnapshot{}, err
	}
	toolsByName := map[string]api.ToolDetailResponse{}
	aliasToCanonical := map[string]string{}
	for _, tool := range discovered {
		normalizedName := normalizeLookup(tool.Name)
		if normalizedName == "" {
			continue
		}
		if _, exists := toolsByName[normalizedName]; exists {
			log.Printf("[mcp] duplicate MCP tool %q from server %q, keep first", tool.Name, server.Key)
			continue
		}
		tool = applyServerToolOverride(tool, findServerToolOverride(server.Tools, tool))
		def := tool.ToAPITool(server.Key)
		toolsByName[normalizedName] = def
		registerAliases(server, normalizedName, tool.Aliases, aliasToCanonical)
	}
	return serverToolSnapshot{toolsByName: toolsByName, aliasToCanonical: aliasToCanonical}, nil
}

func findServerToolOverride(overrides []ToolDefinition, tool ToolDefinition) *ToolDefinition {
	toolName := normalizeLookup(tool.Name)
	toolKey := normalizeLookup(tool.Key)
	for i := range overrides {
		override := &overrides[i]
		if normalizeLookup(override.Name) == toolName && toolName != "" {
			return override
		}
		if normalizeLookup(override.Key) == toolKey && toolKey != "" {
			return override
		}
	}
	return nil
}

func applyServerToolOverride(base ToolDefinition, override *ToolDefinition) ToolDefinition {
	if override == nil {
		return base
	}
	merged := base
	if strings.TrimSpace(override.Key) != "" {
		merged.Key = strings.TrimSpace(override.Key)
	}
	if strings.TrimSpace(override.Label) != "" {
		merged.Label = strings.TrimSpace(override.Label)
	}
	if strings.TrimSpace(override.Description) != "" {
		merged.Description = strings.TrimSpace(override.Description)
	}
	if strings.TrimSpace(override.AfterCallHint) != "" {
		merged.AfterCallHint = strings.TrimSpace(override.AfterCallHint)
	}
	if len(override.Parameters) > 0 {
		merged.Parameters = cloneMap(override.Parameters)
	}
	if override.ToolAction {
		merged.ToolAction = true
	}
	if strings.TrimSpace(override.ToolType) != "" {
		merged.ToolType = strings.TrimSpace(override.ToolType)
	}
	if strings.TrimSpace(override.ViewportKey) != "" {
		merged.ViewportKey = strings.TrimSpace(override.ViewportKey)
	}
	if len(override.Meta) > 0 {
		if merged.Meta == nil {
			merged.Meta = map[string]any{}
		}
		for key, value := range override.Meta {
			merged.Meta[key] = value
		}
	}
	return merged
}

func mergeSnapshots(servers []ServerDefinition, snapshots map[string]serverToolSnapshot) (map[string]api.ToolDetailResponse, map[string]string) {
	toolsByName := map[string]api.ToolDetailResponse{}
	aliasToCanonical := map[string]string{}
	conflicts := map[string]struct{}{}
	for _, server := range servers {
		snapshot, ok := snapshots[normalizeLookup(server.Key)]
		if !ok {
			continue
		}
		keys := make([]string, 0, len(snapshot.toolsByName))
		for key := range snapshot.toolsByName {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, blocked := conflicts[key]; blocked {
				continue
			}
			tool := snapshot.toolsByName[key]
			if _, exists := toolsByName[key]; exists {
				delete(toolsByName, key)
				conflicts[key] = struct{}{}
				log.Printf("[mcp] duplicate MCP tool %q across servers, both skipped", key)
				continue
			}
			toolsByName[key] = cloneTool(tool)
		}
		aliasKeys := make([]string, 0, len(snapshot.aliasToCanonical))
		for alias := range snapshot.aliasToCanonical {
			aliasKeys = append(aliasKeys, alias)
		}
		sort.Strings(aliasKeys)
		for _, alias := range aliasKeys {
			canonical := snapshot.aliasToCanonical[alias]
			if canonical == "" || alias == canonical {
				continue
			}
			if existing, exists := aliasToCanonical[alias]; exists && existing != canonical {
				log.Printf("[mcp] duplicate MCP alias %q for %q and %q, keep first", alias, existing, canonical)
				continue
			}
			aliasToCanonical[alias] = canonical
		}
	}
	return toolsByName, aliasToCanonical
}

func registerAliases(server ServerDefinition, canonical string, aliases []string, aliasToCanonical map[string]string) {
	for _, alias := range aliases {
		registerAlias(alias, canonical, aliasToCanonical)
	}
	for alias, target := range server.AliasMap {
		if normalizeLookup(target) == canonical {
			registerAlias(alias, canonical, aliasToCanonical)
		}
	}
}

func registerAlias(alias string, canonical string, aliasToCanonical map[string]string) {
	normalizedAlias := normalizeLookup(alias)
	if normalizedAlias == "" || normalizedAlias == canonical {
		return
	}
	if existing, exists := aliasToCanonical[normalizedAlias]; exists && existing != canonical {
		log.Printf("[mcp] duplicate MCP alias %q for %q and %q, keep first", normalizedAlias, existing, canonical)
		return
	}
	aliasToCanonical[normalizedAlias] = canonical
}

func cloneSnapshot(snapshot serverToolSnapshot) serverToolSnapshot {
	tools := make(map[string]api.ToolDetailResponse, len(snapshot.toolsByName))
	for key, tool := range snapshot.toolsByName {
		tools[key] = cloneTool(tool)
	}
	aliases := make(map[string]string, len(snapshot.aliasToCanonical))
	for key, value := range snapshot.aliasToCanonical {
		aliases[key] = value
	}
	return serverToolSnapshot{toolsByName: tools, aliasToCanonical: aliases}
}

func cloneSortedToolDefinitions(tools map[string]api.ToolDetailResponse) []api.ToolDetailResponse {
	keys := make([]string, 0, len(tools))
	for key := range tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]api.ToolDetailResponse, 0, len(keys))
	for _, key := range keys {
		out = append(out, cloneTool(tools[key]))
	}
	return out
}

func normalizeLookup(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func cloneTool(tool api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           tool.Key,
		Name:          tool.Name,
		Label:         tool.Label,
		Description:   tool.Description,
		AfterCallHint: tool.AfterCallHint,
		Parameters:    cloneMap(tool.Parameters),
		Meta:          cloneMap(tool.Meta),
	}
}
