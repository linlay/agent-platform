package tools

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/resources"
)

var requiredBuiltinToolNames = []string{
	"artifact_publish",
	"bash",
	"datetime",
	"_memory_read_",
	"_memory_forget_",
	"_memory_promote_",
	"_memory_search_",
	"_memory_timeline_",
	"_memory_update_",
	"_memory_write_",
	"plan_add_tasks",
	"plan_get_tasks",
	"plan_update_task",
	"bash_sandbox",
	"ask_user_question",
	"agent_invoke",
}

func LoadEmbeddedToolDefinitions() ([]api.ToolDetailResponse, error) {
	pattern := "tools"
	entries, err := fs.ReadDir(resources.ToolFS, pattern)
	if err != nil {
		return nil, fmt.Errorf("read embedded tool definitions: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".yml") || strings.HasSuffix(strings.ToLower(entry.Name()), ".yaml") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	defs := make([]api.ToolDetailResponse, 0, len(names))
	seen := map[string]string{}
	for _, name := range names {
		path := filepath.ToSlash(filepath.Join(pattern, name))
		data, err := resources.ToolFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read embedded tool %s: %w", path, err)
		}
		tree, err := config.LoadYAMLTreeBytes(data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded tool %s: %w", path, err)
		}
		root, ok := tree.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("embedded tool %s must have object root", path)
		}
		def, err := parseToolDefinition(root, toolDefinitionParseOptions{
			sourceType:       "local",
			defaultSourceKey: strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml"),
		})
		if err != nil {
			return nil, fmt.Errorf("parse embedded tool %s: %w", path, err)
		}
		normalized := normalizeToolName(def)
		if previous, exists := seen[normalized]; exists {
			return nil, fmt.Errorf("duplicate embedded tool %q in %s and %s", def.Name, previous, path)
		}
		seen[normalized] = path
		defs = append(defs, def)
	}
	for _, name := range requiredBuiltinToolNames {
		if _, ok := seen[strings.ToLower(strings.TrimSpace(name))]; !ok {
			return nil, fmt.Errorf("missing embedded builtin tool definition: %s", name)
		}
	}
	return defs, nil
}
