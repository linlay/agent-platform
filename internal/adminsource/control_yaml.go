package adminsource

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"agent-platform/internal/config"
)

func redactAgentEnvironment(content string) (string, []string, error) {
	tree, err := parseDefinition(content)
	if err != nil {
		return "", nil, err
	}
	env := node(node(tree, "runtimeConfig"), "env")
	spans, err := config.YAMLSourceMap(content, "runtimeConfig", "env")
	if err != nil {
		return "", nil, err
	}
	if len(spans) != len(env) {
		return "", nil, fmt.Errorf("cannot safely locate agent environment")
	}
	replacements := map[string]config.YAMLSourceValue{}
	paths := []string{}
	for key := range env {
		if _, ok := spans[key]; !ok {
			return "", nil, fmt.Errorf("cannot safely locate agent environment key")
		}
		replacements[key] = config.YAMLSourceValue{Head: `"[REDACTED]"`}
		paths = append(paths, "runtimeConfig.env."+key)
	}
	result, err := config.ReplaceYAMLSourceValues(content, spans, replacements)
	if err != nil {
		return "", nil, err
	}
	safe, err := parseDefinition(result)
	if err != nil {
		return "", nil, err
	}
	for key := range env {
		env[key] = "[REDACTED]"
	}
	if !reflect.DeepEqual(tree, safe) {
		return "", nil, fmt.Errorf("environment redaction changed unrelated configuration")
	}
	sort.Strings(paths)
	return result, paths, nil
}

func preserveAgentEnvironment(before, candidate string, paths []string) (string, error) {
	old, err := parseDefinition(before)
	if err != nil {
		return "", err
	}
	next, err := parseDefinition(candidate)
	if err != nil {
		return "", err
	}
	oldEnv := node(node(old, "runtimeConfig"), "env")
	env := node(node(next, "runtimeConfig"), "env")
	if env == nil {
		return "", fmt.Errorf("candidate must contain runtimeConfig.env")
	}
	originals, err := config.YAMLSourceMap(before, "runtimeConfig", "env")
	if err != nil {
		return "", err
	}
	replacements := map[string]config.YAMLSourceValue{}
	for _, path := range paths {
		key, ok := strings.CutPrefix(path, "runtimeConfig.env.")
		if !ok || key == "" {
			return "", fmt.Errorf("invalid preservePaths entry")
		}
		if _, seen := replacements[key]; seen {
			return "", fmt.Errorf("duplicate preservePaths entry")
		}
		value, ok := oldEnv[key]
		if !ok {
			return "", fmt.Errorf("preserved key does not exist")
		}
		if current, exists := env[key]; exists && current != "[REDACTED]" {
			return "", fmt.Errorf("preserved key also has a candidate value")
		}
		raw, ok := originals[key]
		if !ok {
			return "", fmt.Errorf("cannot locate preserved environment key")
		}
		replacements[key] = raw
		env[key] = value
	}
	// Missing keys are inserted as placeholders, then use the same raw-value edit.
	keys := []string{}
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		spans, e := config.YAMLSourceMap(candidate, "runtimeConfig", "env")
		if e != nil {
			return "", e
		}
		if _, exists := spans[key]; exists {
			continue
		}
		candidate, e = insertEnvironmentPlaceholder(candidate, key, spans)
		if e != nil {
			return "", e
		}
	}
	spans, err := config.YAMLSourceMap(candidate, "runtimeConfig", "env")
	if err != nil {
		return "", err
	}
	result, err := config.ReplaceYAMLSourceValues(candidate, spans, replacements)
	if err != nil {
		return "", err
	}
	actual, err := parseDefinition(result)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(next, actual) {
		return "", fmt.Errorf("preserving environment changed unrelated configuration")
	}
	return result, nil
}

func insertEnvironmentPlaceholder(content, key string, members map[string]config.YAMLSourceValue) (string, error) {
	parents, err := config.YAMLSourceMap(content, "runtimeConfig")
	if err != nil {
		return "", err
	}
	env, ok := parents["env"]
	if !ok {
		return "", fmt.Errorf("candidate must contain runtimeConfig.env")
	}
	if strings.HasPrefix(env.Head, "{") {
		at := env.Start + len(env.Head) - 1
		prefix := ""
		if len(members) > 0 {
			prefix = ", "
		}
		return content[:at] + prefix + key + `: "[REDACTED]"` + content[at:], nil
	}
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	indent := env.Indent + 2
	for _, member := range members {
		indent = member.Indent
		break
	}
	addition := newline + strings.Repeat(" ", indent) + key + `: "[REDACTED]"`
	return content[:env.End] + addition + content[env.End:], nil
}
