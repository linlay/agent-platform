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

const builtinToolCatalogPath = "builtin_tool_catalog.yml"

var requiredBuiltinToolNames = []string{
	"agent_delegate",
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
	if err := applyBuiltinToolCatalogVisibility(defs, builtinToolCatalogPath); err != nil {
		return nil, err
	}
	return defs, nil
}

func applyBuiltinToolCatalogVisibility(defs []api.ToolDetailResponse, path string) error {
	data, err := resources.ToolFS.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read embedded builtin tool catalog %s: %w", path, err)
	}
	return applyBuiltinToolCatalogVisibilityData(defs, data)
}

func applyBuiltinToolCatalogVisibilityData(defs []api.ToolDetailResponse, data []byte) error {
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		return fmt.Errorf("parse embedded builtin tool catalog: %w", err)
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return fmt.Errorf("embedded builtin tool catalog must have object root")
	}
	rawNames, ok := root["visibleBuiltinTools"]
	if !ok {
		return fmt.Errorf("embedded builtin tool catalog requires visibleBuiltinTools")
	}
	names, ok := rawNames.([]any)
	if !ok {
		return fmt.Errorf("embedded builtin tool catalog visibleBuiltinTools must be a list")
	}

	known := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		name := normalizeToolName(def)
		if name == "" {
			return fmt.Errorf("embedded builtin tool catalog cannot apply visibility to unnamed tool")
		}
		known[name] = struct{}{}
	}
	visible := make(map[string]struct{}, len(names))
	for index, rawName := range names {
		name, ok := rawName.(string)
		if !ok {
			return fmt.Errorf("embedded builtin tool catalog visibleBuiltinTools[%d] must be a string", index)
		}
		normalized := strings.ToLower(strings.TrimSpace(name))
		if normalized == "" {
			return fmt.Errorf("embedded builtin tool catalog visibleBuiltinTools[%d] must not be empty", index)
		}
		if _, exists := visible[normalized]; exists {
			return fmt.Errorf("embedded builtin tool catalog visibleBuiltinTools contains duplicate tool %q", strings.TrimSpace(name))
		}
		if _, exists := known[normalized]; !exists {
			return fmt.Errorf("embedded builtin tool catalog references unknown tool %q", strings.TrimSpace(name))
		}
		visible[normalized] = struct{}{}
	}

	for index := range defs {
		if defs[index].Meta == nil {
			defs[index].Meta = map[string]any{}
		}
		_, allowed := visible[normalizeToolName(defs[index])]
		defs[index].Meta["catalogVisible"] = allowed
	}
	return nil
}
