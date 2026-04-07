package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

type Registry struct {
	root string

	mu      sync.RWMutex
	servers map[string]ServerDefinition
}

func NewRegistry(root string) (*Registry, error) {
	registry := &Registry{
		root:    root,
		servers: map[string]ServerDefinition{},
	}
	if err := registry.Reload(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Reload() error {
	entries, err := os.ReadDir(r.root)
	if os.IsNotExist(err) {
		r.mu.Lock()
		r.servers = map[string]ServerDefinition{}
		r.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}

	servers := map[string]ServerDefinition{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !catalog.ShouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		path := filepath.Join(r.root, name)
		server, err := parseServerFile(path)
		if err != nil {
			continue
		}
		servers[server.Key] = server
	}
	r.mu.Lock()
	r.servers = servers
	r.mu.Unlock()
	return nil
}

func (r *Registry) Server(key string) (ServerDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	server, ok := r.servers[key]
	return server, ok
}

func (r *Registry) Servers() []ServerDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServerDefinition, 0, len(r.servers))
	for _, server := range r.servers {
		out = append(out, server)
	}
	return out
}

func parseServerFile(path string) (ServerDefinition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return ServerDefinition{}, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return ServerDefinition{}, fmt.Errorf("mcp server file must be a map")
	}
	server := ServerDefinition{
		Key:          stringNode(root["key"]),
		Name:         stringNode(root["name"]),
		BaseURL:      stringNode(root["baseUrl"]),
		EndpointPath: stringNode(root["endpointPath"]),
		AuthToken:    stringNode(root["authToken"]),
		Headers:      stringMapNode(root["headers"]),
		TimeoutMs:    intNode(root["timeoutMs"]),
		Retry:        intNode(root["retry"]),
	}
	for _, item := range listMaps(root["tools"]) {
		server.Tools = append(server.Tools, ToolDefinition{
			Key:         stringNode(item["key"]),
			Name:        stringNode(item["name"]),
			Description: stringNode(item["description"]),
			Parameters:  mapNode(item["parameters"]),
			Meta:        mapNode(item["meta"]),
		})
	}
	if server.Key == "" {
		base := filepath.Base(path)
		server.Key = strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	}
	if server.Name == "" {
		server.Name = server.Key
	}
	return server, nil
}

func stringNode(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func intNode(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func mapNode(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func stringMapNode(value any) map[string]string {
	raw, _ := value.(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func listMaps(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			out = append(out, mapped)
		}
	}
	return out
}
