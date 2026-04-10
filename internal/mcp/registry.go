package mcp

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

const defaultReadTimeoutMs = 15000

type Registry struct {
	root string

	mu      sync.RWMutex
	version int64
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
	servers, err := loadServersFromDir(r.root)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.servers = servers
	r.version++
	r.mu.Unlock()
	return nil
}

func (r *Registry) Version() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

func (r *Registry) Server(key string) (ServerDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	server, ok := r.servers[normalizeKey(key)]
	return server, ok
}

func (r *Registry) Servers() []ServerDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.servers))
	for key := range r.servers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ServerDefinition, 0, len(keys))
	for _, key := range keys {
		out = append(out, r.servers[key])
	}
	return out
}

func loadServersFromDir(root string) (map[string]ServerDefinition, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return map[string]ServerDefinition{}, nil
	} else if err != nil {
		return nil, err
	}

	files := make([]string, 0, 16)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d == nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !catalog.ShouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	servers := map[string]ServerDefinition{}
	for _, path := range files {
		server, err := parseServerFile(path)
		if err != nil {
			continue
		}
		if server.Key == "" || !server.Enabled() {
			continue
		}
		if _, exists := servers[server.Key]; exists {
			continue
		}
		servers[server.Key] = server
	}
	return servers, nil
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
	if !firstBool(root["enabled"], true) {
		return ServerDefinition{}, nil
	}
	serverKey := normalizeKey(firstString(root["serverKey"], root["server-key"], root["key"]))
	if serverKey == "" {
		base := filepath.Base(path)
		serverKey = normalizeKey(strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml"))
	}
	baseURL := strings.TrimSpace(firstString(root["baseUrl"], root["base-url"], root["url"]))
	if baseURL == "" {
		return ServerDefinition{}, fmt.Errorf("empty baseUrl")
	}
	endpointPath := normalizeEndpointPath(firstString(root["endpointPath"], root["endpoint-path"], root["path"]))
	server := ServerDefinition{
		Key:              serverKey,
		Name:             fallbackString(firstString(root["name"]), serverKey),
		BaseURL:          baseURL,
		EndpointPath:     endpointPath,
		ToolPrefix:       strings.TrimSpace(firstString(root["toolPrefix"], root["tool-prefix"])),
		AuthToken:        strings.TrimSpace(firstString(root["authToken"], root["auth-token"])),
		Headers:          normalizeStringMap(anyMapNode(root["headers"])),
		AliasMap:         normalizeAliasMap(anyMapNode(root["aliasMap"])),
		ConnectTimeoutMs: firstInt(root["connectTimeoutMs"], root["connect-timeout-ms"], 3000),
		ReadTimeoutMs:    firstInt(root["readTimeoutMs"], root["read-timeout-ms"], defaultReadTimeoutMs),
		Retry:            firstInt(root["retry"], nil, 1),
	}
	for _, item := range listMaps(root["tools"]) {
		tool, err := parseToolDefinition(item)
		if err != nil {
			continue
		}
		server.Tools = append(server.Tools, tool)
	}
	return server, nil
}

func parseToolDefinition(root map[string]any) (ToolDefinition, error) {
	name := strings.TrimSpace(firstString(root["name"]))
	if name == "" {
		return ToolDefinition{}, fmt.Errorf("tool name is required")
	}
	parameters := anyMapNode(root["inputSchema"])
	if len(parameters) == 0 {
		parameters = anyMapNode(root["parameters"])
	}
	aliases := normalizeAliases(root["aliases"])
	return ToolDefinition{
		Key:           strings.TrimSpace(firstString(root["key"])),
		Name:          name,
		Label:         strings.TrimSpace(firstString(root["label"])),
		Description:   strings.TrimSpace(firstString(root["description"])),
		AfterCallHint: strings.TrimSpace(firstString(root["afterCallHint"])),
		Parameters:    cloneMap(parameters),
		ToolAction:    firstBool(root["toolAction"], false),
		ToolType:      strings.TrimSpace(firstString(root["toolType"])),
		ViewportKey:   strings.TrimSpace(firstString(root["viewportKey"])),
		Aliases:       aliases,
		Meta:          cloneMap(anyMapNode(root["meta"])),
	}, nil
}

func (s ServerDefinition) Enabled() bool {
	return strings.TrimSpace(s.Key) != "" && strings.TrimSpace(s.BaseURL) != ""
}

func normalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeEndpointPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/mcp"
	}
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

func normalizeStringMap(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		normalizedKey := strings.TrimSpace(key)
		normalizedValue := strings.TrimSpace(anyString(value))
		if normalizedKey == "" || normalizedValue == "" {
			continue
		}
		out[normalizedKey] = normalizedValue
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAliasMap(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		alias := normalizeKey(key)
		target := normalizeKey(anyString(value))
		if alias == "" || target == "" {
			continue
		}
		out[alias] = target
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAliases(value any) []string {
	raw, _ := value.([]any)
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		alias := normalizeKey(anyString(item))
		if alias != "" {
			out = append(out, alias)
		}
	}
	if len(out) == 0 {
		return nil
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

func firstString(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(anyString(value)); text != "" {
			return text
		}
	}
	return ""
}

func firstInt(primary any, secondary any, fallback int) int {
	for _, value := range []any{primary, secondary} {
		if value == nil {
			continue
		}
		switch v := value.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		case string:
			text := strings.TrimSpace(v)
			if text == "" {
				continue
			}
			if parsed, err := strconv.Atoi(text); err == nil {
				return parsed
			}
		}
	}
	return fallback
}

func firstBool(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		text := strings.ToLower(strings.TrimSpace(v))
		switch text {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return fallback
}

func anyMapNode(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func anyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.Itoa(int(v))
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}
