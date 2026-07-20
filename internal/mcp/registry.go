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

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

const (
	defaultReadTimeout    = 15
	defaultStartupTimeout = 5
)

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
			return nil, fmt.Errorf("load MCP server %s: %w", path, err)
		}
		if server.Key == "" || !server.Enabled() {
			continue
		}
		if _, exists := servers[server.Key]; exists {
			return nil, fmt.Errorf("duplicate MCP server key %q in %s", server.Key, path)
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
	return parseServerTree(path, tree)
}

func parseServerTree(path string, tree any) (ServerDefinition, error) {
	root, ok := tree.(map[string]any)
	if !ok {
		return ServerDefinition{}, fmt.Errorf("mcp server file must be a map")
	}
	if !firstBool(root["enabled"], true) {
		return ServerDefinition{}, nil
	}
	serverKey := normalizeKey(contracts.FirstNonEmptyString(root["serverKey"], root["server-key"], root["key"]))
	if serverKey == "" {
		base := filepath.Base(path)
		serverKey = normalizeKey(strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml"))
	}
	transport := strings.ToLower(strings.TrimSpace(contracts.FirstNonEmptyString(root["transport"])))
	if transport == "" {
		transport = TransportStreamableHTTP
	}
	if transport != TransportStreamableHTTP && transport != TransportStdio {
		return ServerDefinition{}, fmt.Errorf("unsupported MCP transport %q", transport)
	}
	baseURL := strings.TrimSpace(contracts.FirstNonEmptyString(root["baseUrl"], root["base-url"], root["url"]))
	command := strings.TrimSpace(contracts.FirstNonEmptyString(root["command"]))
	args, err := normalizeStringSlice(root["args"])
	if err != nil {
		return ServerDefinition{}, fmt.Errorf("args: %w", err)
	}
	env := normalizeStringMap(contracts.AnyMapNode(root["env"]))
	workingDir := strings.TrimSpace(contracts.FirstNonEmptyString(root["workingDirectory"], root["working-directory"]))
	baseDir := filepath.Dir(path)
	if transport == TransportStreamableHTTP {
		if baseURL == "" {
			return ServerDefinition{}, fmt.Errorf("streamable-http MCP server requires baseUrl")
		}
		if hasAnyKey(root, "command", "args", "env", "workingDirectory", "working-directory") {
			return ServerDefinition{}, fmt.Errorf("streamable-http MCP server cannot declare stdio fields")
		}
	} else {
		if command == "" {
			return ServerDefinition{}, fmt.Errorf("stdio MCP server requires command")
		}
		if hasAnyKey(root, "baseUrl", "base-url", "url", "endpointPath", "endpoint-path", "path", "authToken", "auth-token", "headers") {
			return ServerDefinition{}, fmt.Errorf("stdio MCP server cannot declare HTTP fields")
		}
		if !filepath.IsAbs(command) {
			command = filepath.Clean(filepath.Join(baseDir, command))
		}
		if workingDir == "" {
			workingDir = baseDir
		} else if !filepath.IsAbs(workingDir) {
			workingDir = filepath.Clean(filepath.Join(baseDir, workingDir))
		}
	}
	endpointPath := ""
	if transport == TransportStreamableHTTP {
		endpointPath = normalizeEndpointPath(contracts.FirstNonEmptyString(root["endpointPath"], root["endpoint-path"], root["path"]))
	}
	server := ServerDefinition{
		Key:            serverKey,
		Name:           fallbackString(contracts.FirstNonEmptyString(root["name"]), serverKey),
		Transport:      transport,
		BaseURL:        baseURL,
		EndpointPath:   endpointPath,
		Command:        command,
		Args:           args,
		Env:            env,
		WorkingDir:     workingDir,
		ToolPrefix:     strings.TrimSpace(contracts.FirstNonEmptyString(root["toolPrefix"], root["tool-prefix"])),
		AuthToken:      strings.TrimSpace(contracts.FirstNonEmptyString(root["authToken"], root["auth-token"])),
		Headers:        normalizeStringMap(contracts.AnyMapNode(root["headers"])),
		AliasMap:       normalizeAliasMap(contracts.AnyMapNode(root["aliasMap"])),
		ConnectTimeout: firstInt(root["connect-timeout"], nil, 3),
		StartupTimeout: firstInt(root["startup-timeout"], root["startupTimeout"], defaultStartupTimeout),
		ReadTimeout:    firstInt(root["read-timeout"], nil, defaultReadTimeout),
		Retry:          firstInt(root["retry"], nil, 1),
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

// ValidateServerCandidate parses an MCP server YAML candidate without opening
// a network connection or writing it into the runtime registry.
func ValidateServerCandidate(resourceKey string, content []byte) error {
	resourceKey = normalizeKey(resourceKey)
	if resourceKey == "" {
		return fmt.Errorf("MCP server key is required")
	}
	if resourceKey == "." || resourceKey == ".." || strings.HasPrefix(resourceKey, ".") || strings.ContainsAny(resourceKey, `/\`) {
		return fmt.Errorf("invalid MCP server key")
	}
	tree, err := config.LoadYAMLTreeBytes(content)
	if err != nil {
		return err
	}
	server, err := parseServerTree(filepath.Join(".", resourceKey+".yml"), tree)
	if err != nil {
		return err
	}
	if server.Key != "" && server.Key != resourceKey {
		return fmt.Errorf("serverKey must match resourceKey")
	}
	return nil
}

func hasAnyKey(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}

func parseToolDefinition(root map[string]any) (ToolDefinition, error) {
	name := strings.TrimSpace(contracts.FirstNonEmptyString(root["name"]))
	if name == "" {
		return ToolDefinition{}, fmt.Errorf("tool name is required")
	}
	meta := contracts.CloneMap(contracts.AnyMapNode(root["meta"]))
	parameters := contracts.AnyMapNode(root["inputSchema"])
	if len(parameters) == 0 {
		parameters = contracts.AnyMapNode(root["parameters"])
	}
	aliases := normalizeAliases(root["aliases"])
	return ToolDefinition{
		Key:           strings.TrimSpace(contracts.FirstNonEmptyString(root["key"])),
		Name:          name,
		Label:         strings.TrimSpace(contracts.FirstNonEmptyString(root["label"])),
		Description:   strings.TrimSpace(contracts.FirstNonEmptyString(root["description"])),
		AfterCallHint: strings.TrimSpace(contracts.FirstNonEmptyString(root["afterCallHint"])),
		Parameters:    contracts.CloneMap(parameters),
		OutputSchema:  contracts.CloneMap(contracts.AnyMapNode(root["outputSchema"])),
		ToolAction:    firstBool(root["toolAction"], false),
		ViewportType:  strings.TrimSpace(contracts.FirstNonEmptyString(root["viewportType"])),
		ViewportKey:   strings.TrimSpace(contracts.FirstNonEmptyString(root["viewportKey"])),
		Aliases:       aliases,
		Meta:          meta,
	}, nil
}

func (s ServerDefinition) Enabled() bool {
	if strings.TrimSpace(s.Key) == "" {
		return false
	}
	if s.Transport == TransportStdio {
		return strings.TrimSpace(s.Command) != ""
	}
	return strings.TrimSpace(s.BaseURL) != ""
}

func normalizeStringSlice(value any) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("must contain only strings")
			}
			out = append(out, text)
		}
		return out, nil
	case string:
		return parseFlowStringList(typed)
	default:
		return nil, fmt.Errorf("must be a list of strings")
	}
}

func parseFlowStringList(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil, fmt.Errorf("must be a list of strings")
	}
	body := strings.TrimSpace(value[1 : len(value)-1])
	if body == "" {
		return []string{}, nil
	}
	var (
		items   []string
		start   int
		quote   byte
		escaped bool
	)
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 && ch == '\\' && quote == '"' {
			escaped = true
			continue
		}
		if ch == '\'' || ch == '"' {
			if quote == 0 {
				quote = ch
			} else if quote == ch {
				quote = 0
			}
			continue
		}
		if ch == ',' && quote == 0 {
			item, err := parseFlowStringItem(body[start:i])
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			start = i + 1
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("invalid quoted string in args")
	}
	item, err := parseFlowStringItem(body[start:])
	if err != nil {
		return nil, err
	}
	return append(items, item), nil
}

func parseFlowStringItem(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("args must not contain an empty YAML item")
	}
	if value[0] == '"' {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted string in args: %w", err)
		}
		return unquoted, nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", fmt.Errorf("invalid quoted string in args")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	return value, nil
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
		normalizedValue := strings.TrimSpace(contracts.StringValue(value))
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
		target := normalizeKey(contracts.StringValue(value))
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
		alias := normalizeKey(contracts.StringValue(item))
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

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}
