package tools

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/resources"
)

var requiredBuiltinToolNames = []string{
	"artifact_publish",
	"bash",
	"datetime",
	"desktop_action",
	"desktop_cdp",
	"file_edit",
	"file_glob",
	"file_grep",
	"file_read",
	"file_write",
	"memory_read",
	"memory_forget",
	"memory_promote",
	"memory_search",
	"memory_timeline",
	"memory_update",
	"memory_write",
	"plan_add_tasks",
	"plan_get_tasks",
	"plan_update_task",
	"finalize_planning",
	"regex",
	"vision_recognize",
	"image_generate",
	"web_fetch",
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
			sourceCategory:   "platform",
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
