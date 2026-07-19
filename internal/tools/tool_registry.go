package tools

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func LoadRuntimeToolDefinitions(root string) ([]api.ToolDetailResponse, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	files, err := runtimeToolYAMLFiles(root)
	if err != nil {
		return nil, err
	}

	toolFiles := make([]runtimeToolFile, 0, len(files))
	for _, path := range files {
		name := strings.ToLower(filepath.Base(path))
		if name == "service.yml" || name == "service.yaml" {
			return nil, deprecatedExternalToolConfigError(path)
		}
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
		if isDeprecatedExternalToolConfig(path, rootNode) {
			return nil, deprecatedExternalToolConfigError(path)
		}
		toolFiles = append(toolFiles, runtimeToolFile{path: path, root: rootNode})
	}

	out := make([]api.ToolDetailResponse, 0, len(toolFiles))
	for _, file := range toolFiles {
		options := toolDefinitionParseOptions{sourceType: "agent-local", sourceCategory: "external"}
		def, err := parseToolDefinition(file.root, options)
		if err != nil {
			log.Printf("[tools] skip invalid tool file %s: %v", file.path, err)
			continue
		}
		out = append(out, def)
	}
	return out, nil
}

type runtimeToolFile struct {
	path string
	root map[string]any
}

func runtimeToolYAMLFiles(root string) ([]string, error) {
	files := make([]string, 0, 16)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry == nil {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || !catalog.ShouldLoadRuntimeName(name)) {
				return filepath.SkipDir
			}
			return nil
		}
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
	return files, nil
}

func isDeprecatedExternalToolConfig(path string, root map[string]any) bool {
	name := strings.ToLower(filepath.Base(path))
	if name == "service.yml" || name == "service.yaml" {
		return true
	}
	_, hasExternal := root["external"]
	return strings.EqualFold(strings.TrimSpace(AnyStringNode(root["kind"])), "external-service") ||
		strings.EqualFold(strings.TrimSpace(AnyStringNode(root["type"])), "external") ||
		hasExternal
}

func deprecatedExternalToolConfigError(path string) error {
	return fmt.Errorf("deprecated external stdio tool config %s: move the subprocess to registries/mcp-servers with transport: stdio", path)
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
		merged.Parameters = CloneMap(overlay.Parameters)
	}
	if len(overlay.OutputSchema) > 0 {
		merged.OutputSchema = CloneMap(overlay.OutputSchema)
	}
	merged.Meta = CloneMap(runtime.Meta)
	for key, value := range overlay.Meta {
		if isToolSourceMetaKey(key) {
			continue
		}
		merged.Meta[key] = value
	}
	return merged
}

func isToolSourceMetaKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "sourcecategory", "sourcetype", "sourcekey":
		return true
	default:
		return false
	}
}

func cloneToolDefinition(def api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           def.Key,
		Name:          def.Name,
		Label:         def.Label,
		Description:   def.Description,
		AfterCallHint: def.AfterCallHint,
		Parameters:    CloneMap(def.Parameters),
		OutputSchema:  CloneMap(def.OutputSchema),
		Meta:          CloneMap(def.Meta),
	}
}
