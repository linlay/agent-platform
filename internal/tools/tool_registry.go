package tools

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

func LoadRuntimeToolDefinitions(root string) ([]api.ToolDetailResponse, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	out := make([]api.ToolDetailResponse, 0, len(names))
	for _, name := range names {
		if !catalog.ShouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		path := filepath.Join(root, name)
		tree, err := config.LoadYAMLTree(path)
		if err != nil {
			log.Printf("[tools] skip invalid tool file %s: %v", path, err)
			continue
		}
		rootNode, ok := tree.(map[string]any)
		if !ok {
			log.Printf("[tools] skip invalid tool file %s: object root required", path)
			continue
		}
		def, err := parseToolDefinition(rootNode, toolDefinitionParseOptions{sourceType: "agent-local"})
		if err != nil {
			log.Printf("[tools] skip invalid tool file %s: %v", path, err)
			continue
		}
		out = append(out, def)
	}
	return out, nil
}

func MergeToolDefinitions(base []api.ToolDetailResponse, runtime []api.ToolDetailResponse, mcp []api.ToolDetailResponse) []api.ToolDetailResponse {
	merged := make([]api.ToolDetailResponse, 0, len(base)+len(runtime)+len(mcp))
	byName := map[string]int{}
	for _, def := range base {
		key := normalizeToolName(def)
		byName[key] = len(merged)
		merged = append(merged, cloneToolDefinition(def))
	}
	for _, def := range runtime {
		key := normalizeToolName(def)
		if key == "" {
			continue
		}
		index, exists := byName[key]
		runtimeKind, _ := def.Meta["kind"].(string)
		if strings.EqualFold(runtimeKind, "backend") {
			if !exists {
				log.Printf("[tools] skip backend tool %q because no Go implementation is registered", def.Name)
				continue
			}
			merged[index] = mergeBackendToolDefinition(merged[index], def)
			continue
		}
		if exists {
			continue
		}
		byName[key] = len(merged)
		merged = append(merged, cloneToolDefinition(def))
	}
	for _, def := range mcp {
		key := normalizeToolName(def)
		if key == "" {
			continue
		}
		if _, exists := byName[key]; exists {
			log.Printf("[tools] mcp tool %q conflicts with local tool; local definition wins", def.Name)
			continue
		}
		byName[key] = len(merged)
		merged = append(merged, cloneToolDefinition(def))
	}
	return merged
}

func normalizeToolName(def api.ToolDetailResponse) string {
	name := strings.TrimSpace(def.Name)
	if name == "" {
		name = strings.TrimSpace(def.Key)
	}
	return strings.ToLower(name)
}

func mergeBackendToolDefinition(runtime api.ToolDetailResponse, overlay api.ToolDetailResponse) api.ToolDetailResponse {
	merged := cloneToolDefinition(runtime)
	if strings.TrimSpace(overlay.Key) != "" {
		merged.Key = overlay.Key
	}
	if strings.TrimSpace(overlay.Name) != "" {
		merged.Name = overlay.Name
	}
	if strings.TrimSpace(overlay.Label) != "" {
		merged.Label = overlay.Label
	}
	if strings.TrimSpace(merged.Description) == "" && strings.TrimSpace(overlay.Description) != "" {
		merged.Description = overlay.Description
	}
	if strings.TrimSpace(overlay.AfterCallHint) != "" {
		merged.AfterCallHint = overlay.AfterCallHint
	}
	if len(overlay.Parameters) > 0 {
		merged.Parameters = cloneAnyMap(overlay.Parameters)
	}
	merged.Meta = cloneAnyMap(runtime.Meta)
	for key, value := range overlay.Meta {
		merged.Meta[key] = value
	}
	return merged
}

func cloneToolDefinition(def api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           def.Key,
		Name:          def.Name,
		Label:         def.Label,
		Description:   def.Description,
		AfterCallHint: def.AfterCallHint,
		Parameters:    cloneAnyMap(def.Parameters),
		Meta:          cloneAnyMap(def.Meta),
	}
}
