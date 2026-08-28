package mcp

import (
	"context"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type ToolSync struct {
	registry *Registry
	client   *Client

	refreshMu        sync.Mutex
	mu               sync.RWMutex
	toolsByName      map[string]api.ToolDetailResponse
	aliasToCanonical map[string]string
	snapshots        map[string]serverToolSnapshot
	statuses         map[string]api.MCPServerToolSyncStatus
	registryVersion  int64
}

const (
	ToolSyncStatusPending     = "pending"
	ToolSyncStatusSyncing     = "syncing"
	ToolSyncStatusReady       = "ready"
	ToolSyncStatusUnavailable = "unavailable"
)

type ToolSyncResult struct {
	Tools   []api.ToolDetailResponse
	Changed bool
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
		statuses:         map[string]api.MCPServerToolSyncStatus{},
	}
}

func (s *ToolSync) Load(ctx context.Context) ([]api.ToolDetailResponse, error) {
	result, err := s.refreshTools(ctx, nil)
	return result.Tools, err
}

func (s *ToolSync) RefreshServer(ctx context.Context, serverKey string) ([]api.ToolDetailResponse, error) {
	result, err := s.refreshTools(ctx, map[string]struct{}{normalizeKey(serverKey): {}})
	return result.Tools, err
}

func (s *ToolSync) RefreshServers(ctx context.Context, serverKeys []string) ([]api.ToolDetailResponse, error) {
	result, err := s.RefreshServersWithResult(ctx, serverKeys)
	return result.Tools, err
}

func (s *ToolSync) RefreshServersWithResult(ctx context.Context, serverKeys []string) (ToolSyncResult, error) {
	targets := map[string]struct{}{}
	for _, key := range serverKeys {
		if normalized := normalizeKey(key); normalized != "" {
			targets[normalized] = struct{}{}
		}
	}
	return s.refreshTools(ctx, targets)
}

func (s *ToolSync) ServerStatus(serverKey string) (api.MCPServerToolSyncStatus, bool) {
	if s == nil {
		return api.MCPServerToolSyncStatus{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.statuses[normalizeKey(serverKey)]
	return cloneServerSyncStatus(status), ok
}

func (s *ToolSync) SyncedRegistryVersion() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.registryVersion
}

func (s *ToolSync) Definitions() []api.ToolDetailResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSortedToolDefinitions(s.toolsByName)
}

// ToolNamesForServers returns the current synchronized tools owned by the
// server keys explicitly selected by an Agent's toolConfig.mcp-servers.
func (s *ToolSync) ToolNamesForServers(serverKeys []string) []string {
	if s == nil || len(serverKeys) == 0 {
		return nil
	}
	selected := make(map[string]struct{}, len(serverKeys))
	for _, serverKey := range serverKeys {
		if key := normalizeKey(serverKey); key != "" {
			selected[key] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.registryVersion != s.registry.Version() {
		return nil
	}
	result := make([]string, 0)
	for _, tool := range cloneSortedToolDefinitions(s.toolsByName) {
		serverKey := normalizeKey(contracts.AnyStringNode(tool.Meta["serverKey"]))
		if serverKey == "" {
			serverKey = normalizeKey(contracts.AnyStringNode(tool.Meta["sourceKey"]))
		}
		if _, ok := selected[serverKey]; ok {
			result = append(result, tool.Name)
		}
	}
	return result
}

func (s *ToolSync) Tool(name string) (api.ToolDetailResponse, bool) {
	if s == nil {
		return api.ToolDetailResponse{}, false
	}
	normalized := normalizeKey(name)
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
	normalized := normalizeKey(name)
	if normalized == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	canonical, ok := s.aliasToCanonical[normalized]
	return canonical, ok
}

func (s *ToolSync) refreshTools(ctx context.Context, targets map[string]struct{}) (ToolSyncResult, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	servers := s.registry.Servers()
	activeKeys := make(map[string]struct{}, len(servers))
	s.mu.RLock()
	previousTools := cloneToolMap(s.toolsByName)
	previousStatuses := comparableServerStatuses(s.statuses)
	nextSnapshots := make(map[string]serverToolSnapshot, len(s.snapshots))
	for key, snapshot := range s.snapshots {
		nextSnapshots[key] = cloneSnapshot(snapshot)
	}
	nextStatuses := make(map[string]api.MCPServerToolSyncStatus, len(s.statuses))
	for key, status := range s.statuses {
		nextStatuses[key] = cloneServerSyncStatus(status)
	}
	s.mu.RUnlock()

	for _, server := range servers {
		serverKey := normalizeKey(server.Key)
		activeKeys[serverKey] = struct{}{}
		if len(targets) > 0 {
			if _, ok := targets[serverKey]; !ok {
				continue
			}
		}
		attemptedAt := time.Now().UnixMilli()
		status := nextStatuses[serverKey]
		if status.Status == "" {
			status.Status = ToolSyncStatusPending
		}
		status.Status = ToolSyncStatusSyncing
		status.LastSyncAttemptAt = attemptedAt
		status.Diagnostic = nil
		nextStatuses[serverKey] = status
		s.setServerStatus(serverKey, status)

		snapshot, err := s.syncServer(ctx, server)
		if err != nil {
			log.Printf("[mcp] failed to sync server %q: %v", server.Key, err)
			status.Status = ToolSyncStatusUnavailable
			status.Diagnostic = &api.AdminRegistryListDiagnostic{
				Severity: "error",
				Code:     "mcp_sync_failed",
				Message:  sanitizeSyncError(server, err),
			}
			nextStatuses[serverKey] = status
			continue
		}
		nextSnapshots[serverKey] = snapshot
		status.Status = ToolSyncStatusReady
		status.LastSyncSuccessAt = time.Now().UnixMilli()
		status.Diagnostic = nil
		nextStatuses[serverKey] = status
	}
	for key := range nextSnapshots {
		if _, ok := activeKeys[key]; !ok {
			delete(nextSnapshots, key)
		}
	}
	for key := range nextStatuses {
		if _, ok := activeKeys[key]; !ok {
			delete(nextStatuses, key)
		}
	}

	toolsByName, aliasToCanonical := mergeSnapshots(servers, nextSnapshots)
	changed := !reflect.DeepEqual(previousTools, toolsByName) ||
		!reflect.DeepEqual(previousStatuses, comparableServerStatuses(nextStatuses))
	s.mu.Lock()
	s.snapshots = nextSnapshots
	s.toolsByName = toolsByName
	s.aliasToCanonical = aliasToCanonical
	s.statuses = nextStatuses
	s.registryVersion = s.registry.Version()
	s.mu.Unlock()
	return ToolSyncResult{Tools: cloneSortedToolDefinitions(toolsByName), Changed: changed}, nil
}

func (s *ToolSync) setServerStatus(serverKey string, status api.MCPServerToolSyncStatus) {
	s.mu.Lock()
	if s.statuses == nil {
		s.statuses = map[string]api.MCPServerToolSyncStatus{}
	}
	s.statuses[serverKey] = cloneServerSyncStatus(status)
	s.mu.Unlock()
}

type comparableServerStatus struct {
	Status  string
	Code    string
	Message string
}

func comparableServerStatuses(statuses map[string]api.MCPServerToolSyncStatus) map[string]comparableServerStatus {
	out := make(map[string]comparableServerStatus, len(statuses))
	for key, status := range statuses {
		item := comparableServerStatus{Status: status.Status}
		if status.Diagnostic != nil {
			item.Code = status.Diagnostic.Code
			item.Message = status.Diagnostic.Message
		}
		out[key] = item
	}
	return out
}

func sanitizeSyncError(server ServerDefinition, err error) string {
	message := strings.TrimSpace(err.Error())
	secrets := make([]string, 0, len(server.Headers)+len(server.Env)+1)
	secrets = append(secrets, server.AuthToken)
	for _, value := range server.Headers {
		secrets = append(secrets, value)
	}
	for _, value := range server.Env {
		secrets = append(secrets, value)
	}
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		return "MCP tool synchronization failed"
	}
	return message
}

func cloneServerSyncStatus(status api.MCPServerToolSyncStatus) api.MCPServerToolSyncStatus {
	cloned := status
	if status.Diagnostic != nil {
		diagnostic := *status.Diagnostic
		cloned.Diagnostic = &diagnostic
	}
	return cloned
}

func cloneToolMap(tools map[string]api.ToolDetailResponse) map[string]api.ToolDetailResponse {
	out := make(map[string]api.ToolDetailResponse, len(tools))
	for key, tool := range tools {
		out[key] = cloneTool(tool)
	}
	return out
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
		normalizedName := normalizeKey(tool.Name)
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
	toolName := normalizeKey(tool.Name)
	toolKey := normalizeKey(tool.Key)
	for i := range overrides {
		override := &overrides[i]
		if normalizeKey(override.Name) == toolName && toolName != "" {
			return override
		}
		if normalizeKey(override.Key) == toolKey && toolKey != "" {
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
		merged.Parameters = contracts.CloneMap(override.Parameters)
	}
	if strings.TrimSpace(override.ViewportType) != "" {
		merged.ViewportType = strings.TrimSpace(override.ViewportType)
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
		snapshot, ok := snapshots[normalizeKey(server.Key)]
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
		if normalizeKey(target) == canonical {
			registerAlias(alias, canonical, aliasToCanonical)
		}
	}
}

func registerAlias(alias string, canonical string, aliasToCanonical map[string]string) {
	normalizedAlias := normalizeKey(alias)
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

func cloneTool(tool api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           tool.Key,
		Name:          tool.Name,
		Label:         tool.Label,
		Description:   tool.Description,
		AfterCallHint: tool.AfterCallHint,
		Parameters:    contracts.CloneMap(tool.Parameters),
		OutputSchema:  contracts.CloneMap(tool.OutputSchema),
		Meta:          contracts.CloneMap(tool.Meta),
	}
}
