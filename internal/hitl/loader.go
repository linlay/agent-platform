package hitl

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

func loadRulesFromDir(root string) ([]FlatRule, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
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

	var rules []FlatRule
	seen := map[string]bool{}
	viewportTypes := map[string]string{}
	order := 0
	for _, path := range files {
		file, enabled, parseErr := parseRuleFile(path)
		if parseErr != nil {
			return nil, parseErr
		}
		if !enabled {
			continue
		}
		for _, block := range file.Commands {
			command := strings.ToLower(strings.TrimSpace(block.Command))
			for _, sub := range block.Subcommands {
				match := strings.TrimSpace(sub.Match)
				key := command + "\x00" + strings.ToLower(match)
				if seen[key] {
					continue
				}
				matchTokens := splitMatchTokens(match)
				if err := validateSubcommandRule(command, sub, matchTokens); err != nil {
					return nil, fmt.Errorf("%s: %w", path, err)
				}
				viewportType, viewportKey := normalizeViewport(sub)
				viewportTypeKey := strings.ToLower(strings.TrimSpace(viewportKey))
				if existing, ok := viewportTypes[viewportTypeKey]; ok && existing != viewportType {
					return nil, fmt.Errorf("%s: viewportKey %q is associated with multiple viewportType values", path, viewportKey)
				}
				viewportTypes[viewportTypeKey] = viewportType
				seen[key] = true
				ruleKey := buildRuleKey(file.Key, command, match, sub.Level, viewportType, viewportKey)
				rules = append(rules, FlatRule{
					RuleKey:      ruleKey,
					FileKey:      file.Key,
					SourcePath:   path,
					Order:        order,
					Command:      command,
					Match:        match,
					MatchTokens:  matchTokens,
					Level:        sub.Level,
					Title:        strings.TrimSpace(sub.Title),
					ViewportType: viewportType,
					ViewportKey:  viewportKey,
				})
				order++
			}
		}
	}
	return rules, nil
}

func buildRuleKey(fileKey string, command string, match string, level int, viewportType string, viewportKey string) string {
	return fmt.Sprintf(
		"%s::%s::%s::%d::%s::%s",
		strings.TrimSpace(fileKey),
		strings.TrimSpace(command),
		strings.TrimSpace(match),
		level,
		strings.TrimSpace(viewportType),
		strings.TrimSpace(viewportKey),
	)
}

func parseRuleFile(path string) (RuleFile, bool, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return RuleFile{}, false, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return RuleFile{}, false, fmt.Errorf("rule file must be an object")
	}
	enabled := true
	if raw, exists := root["enabled"]; exists {
		if value, ok := raw.(bool); ok {
			enabled = value
		}
	}
	file := RuleFile{
		Key: strings.TrimSpace(stringValue(root["key"])),
	}
	if file.Key == "" {
		base := filepath.Base(path)
		file.Key = strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	}
	for _, rawCommand := range listMaps(root["commands"]) {
		block := CommandBlock{
			Command: strings.TrimSpace(stringValue(rawCommand["command"])),
		}
		for _, rawSub := range listMaps(rawCommand["subcommands"]) {
			block.Subcommands = append(block.Subcommands, SubcommandRule{
				Match:        strings.TrimSpace(stringValue(rawSub["match"])),
				Level:        intValue(rawSub["level"]),
				Title:        strings.TrimSpace(stringValue(rawSub["title"])),
				ViewportType: strings.TrimSpace(firstString(rawSub, "viewportType", "toolType")),
				ViewportKey:  strings.TrimSpace(stringValue(rawSub["viewportKey"])),
			})
		}
		file.Commands = append(file.Commands, block)
	}
	return file, enabled, nil
}

func validateSubcommandRule(command string, sub SubcommandRule, matchTokens []string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("command is required")
	}
	if sub.Level <= 0 {
		return fmt.Errorf("level must be greater than 0")
	}
	viewportType, viewportKey := normalizeViewport(sub)
	switch viewportType {
	case "builtin", "html":
	default:
		return fmt.Errorf("viewportType must be one of builtin,html")
	}
	if strings.TrimSpace(viewportKey) == "" {
		return fmt.Errorf("viewportKey is required")
	}
	if strings.TrimSpace(sub.Match) != "" && len(matchTokens) == 0 {
		return fmt.Errorf("match is invalid")
	}
	return nil
}

func normalizeViewport(sub SubcommandRule) (string, string) {
	viewportType := strings.ToLower(strings.TrimSpace(sub.ViewportType))
	viewportKey := strings.TrimSpace(sub.ViewportKey)
	if viewportType == "" && viewportKey == "" {
		return "builtin", "confirm_dialog"
	}
	if viewportType == "" {
		viewportType = "builtin"
	}
	if viewportKey == "" {
		viewportKey = "confirm_dialog"
	}
	return viewportType, viewportKey
}

func listMaps(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		mapped, ok := item.(map[string]any)
		if ok {
			out = append(out, mapped)
		}
	}
	return out
}

func firstString(root map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := strings.TrimSpace(stringValue(root[key])); text != "" {
			return text
		}
	}
	return ""
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
