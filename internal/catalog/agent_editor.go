package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	configpkg "agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type EditableAgentSource struct {
	Kind     string
	Path     string
	AgentDir string
}

type EditableAgentFiles struct {
	Key          string
	Definition   map[string]any
	SoulPrompt   string
	AgentsPrompt string
	Source       EditableAgentSource
}

func (r *FileRegistry) EditableAgent(key string) (EditableAgentFiles, bool, error) {
	if r == nil {
		return EditableAgentFiles{}, false, fmt.Errorf("agent registry is not configured")
	}
	return r.findEditableAgent(key)
}

func (r *FileRegistry) CreateEditableAgent(key string, definition map[string]any, soulPrompt *string, agentsPrompt *string) (EditableAgentFiles, error) {
	if err := validateEditableAgentKey(key); err != nil {
		return EditableAgentFiles{}, err
	}
	if _, ok, err := r.findEditableAgent(key); err != nil {
		return EditableAgentFiles{}, err
	} else if ok {
		return EditableAgentFiles{}, fmt.Errorf("agent already exists")
	}
	if err := validateEditableDefinition(key, definition); err != nil {
		return EditableAgentFiles{}, err
	}
	definition = normalizeEditableDefinition(definition)
	agentDir := filepath.Join(r.cfg.Paths.AgentsDir, key)
	source := EditableAgentSource{
		Kind:     "directory",
		Path:     filepath.Join(agentDir, "agent.yml"),
		AgentDir: agentDir,
	}
	if err := persistEditableAgent(source, definition, soulPrompt, agentsPrompt, false); err != nil {
		return EditableAgentFiles{}, err
	}
	return editableFilesFromSource(key, definition, source, stringPtrValue(soulPrompt), stringPtrValue(agentsPrompt)), nil
}

func (r *FileRegistry) UpdateEditableAgent(key string, definition map[string]any, soulPrompt *string, agentsPrompt *string) (EditableAgentFiles, error) {
	if err := validateEditableAgentKey(key); err != nil {
		return EditableAgentFiles{}, err
	}
	existing, ok, err := r.findEditableAgent(key)
	if err != nil {
		return EditableAgentFiles{}, err
	}
	if !ok {
		return EditableAgentFiles{}, fmt.Errorf("agent not found")
	}
	if err := validateEditableDefinition(key, definition); err != nil {
		return EditableAgentFiles{}, err
	}
	definition = normalizeEditableDefinition(definition)
	if soulPrompt == nil {
		soulPrompt = &existing.SoulPrompt
	}
	if agentsPrompt == nil {
		agentsPrompt = &existing.AgentsPrompt
	}
	if err := persistEditableAgent(existing.Source, definition, soulPrompt, agentsPrompt, true); err != nil {
		return EditableAgentFiles{}, err
	}
	return editableFilesFromSource(key, definition, existing.Source, *soulPrompt, *agentsPrompt), nil
}

func (r *FileRegistry) DeleteEditableAgent(key string) error {
	if err := validateEditableAgentKey(key); err != nil {
		return err
	}
	existing, ok, err := r.findEditableAgent(key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("agent not found")
	}
	switch existing.Source.Kind {
	case "directory":
		return os.RemoveAll(existing.Source.AgentDir)
	case "file":
		if err := os.Remove(existing.Source.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported agent source kind %q", existing.Source.Kind)
	}
}

func (r *FileRegistry) findEditableAgent(key string) (EditableAgentFiles, bool, error) {
	if err := validateEditableAgentKey(key); err != nil {
		return EditableAgentFiles{}, false, err
	}
	root := strings.TrimSpace(r.cfg.Paths.AgentsDir)
	if root == "" {
		return EditableAgentFiles{}, false, fmt.Errorf("agents directory is not configured")
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return EditableAgentFiles{}, false, nil
	}
	if err != nil {
		return EditableAgentFiles{}, false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(strings.TrimSpace(name), ".") || !ShouldLoadRuntimeName(name) {
			continue
		}
		if entry.IsDir() {
			source, ok, err := editableDirectorySource(root, name)
			if err != nil || !ok {
				return EditableAgentFiles{}, false, err
			}
			files, match, err := readEditableAgentSource(key, source)
			if err != nil {
				return EditableAgentFiles{}, false, err
			}
			if match {
				return files, true, nil
			}
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".yml") && !strings.HasSuffix(strings.ToLower(name), ".yaml") {
			continue
		}
		source := EditableAgentSource{Kind: "file", Path: filepath.Join(root, name)}
		files, match, err := readEditableAgentSource(key, source)
		if err != nil {
			return EditableAgentFiles{}, false, err
		}
		if match {
			return files, true, nil
		}
	}
	return EditableAgentFiles{}, false, nil
}

func editableDirectorySource(root string, name string) (EditableAgentSource, bool, error) {
	agentDir := filepath.Join(root, name)
	configPath := resolveDirectoryAgentConfig(agentDir)
	if configPath == "" {
		return EditableAgentSource{}, false, nil
	}
	return EditableAgentSource{Kind: "directory", Path: configPath, AgentDir: agentDir}, true, nil
}

func readEditableAgentSource(key string, source EditableAgentSource) (EditableAgentFiles, bool, error) {
	tree, err := configpkg.LoadYAMLTree(source.Path)
	if err != nil {
		return EditableAgentFiles{}, false, err
	}
	definition, ok := tree.(map[string]any)
	if !ok {
		return EditableAgentFiles{}, false, fmt.Errorf("agent file must be a map")
	}
	if strings.TrimSpace(stringNode(definition["key"])) != key {
		return EditableAgentFiles{}, false, nil
	}
	files := EditableAgentFiles{
		Key:        key,
		Definition: contracts.CloneMap(definition),
		Source:     source,
	}
	if source.Kind == "directory" {
		files.SoulPrompt = readOptionalMarkdown(filepath.Join(source.AgentDir, "SOUL.md"))
		files.AgentsPrompt = readOptionalMarkdown(filepath.Join(source.AgentDir, "AGENTS.md"))
	}
	return files, true, nil
}

func validateEditableAgentKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("agent key is required")
	}
	if key == "." || key == ".." || strings.HasPrefix(key, ".") {
		return fmt.Errorf("invalid agent key")
	}
	if strings.ContainsAny(key, `/\`) {
		return fmt.Errorf("invalid agent key")
	}
	clean := filepath.Clean(key)
	if clean != key || filepath.IsAbs(key) {
		return fmt.Errorf("invalid agent key")
	}
	return nil
}

func validateEditableDefinition(key string, definition map[string]any) error {
	if len(definition) == 0 {
		return fmt.Errorf("definition is required")
	}
	if strings.TrimSpace(stringNode(definition["key"])) != strings.TrimSpace(key) {
		return fmt.Errorf("definition.key must match key")
	}
	data := renderYAMLMap(normalizeEditableDefinition(definition))
	path, err := writeValidationAgentFile(data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()
	def, err := parseAgentFile(path)
	if err != nil {
		return err
	}
	if strings.EqualFold(def.Mode, "PROXY") {
		if def.ProxyConfig == nil || strings.TrimSpace(def.ProxyConfig.BaseURL) == "" {
			return fmt.Errorf("proxyConfig.baseUrl is required for PROXY mode")
		}
	}
	return nil
}

func normalizeEditableDefinition(definition map[string]any) map[string]any {
	if definition == nil {
		return nil
	}
	normalized := contracts.CloneMap(definition)
	mode := NormalizeAgentModeForRuntime(stringNode(normalized["mode"]))
	switch mode {
	case "ONESHOT":
		normalized["mode"] = "REACT"
	default:
		normalized["mode"] = AgentModeForAPI(stringNode(normalized["mode"]))
	}
	if mode == AgentModeCoder {
		delete(normalized, "workspace")
		normalizeEditableCoderVisibility(normalized)
	}
	return normalized
}

func normalizeEditableCoderVisibility(definition map[string]any) {
	visibility := mapNode(definition["visibility"])
	scopes := normalizeEditableVisibilityScopes(listStrings(visibility["scopes"]))
	for _, scope := range scopes {
		if scope == "invoke" || scope == "internal" {
			definition["visibility"] = map[string]any{"scopes": scopes}
			return
		}
	}
	definition["visibility"] = map[string]any{"scopes": []any{"nav"}}
}

func normalizeEditableVisibilityScopes(rawScopes []string) []string {
	out := make([]string, 0, len(rawScopes))
	seen := map[string]struct{}{}
	for _, raw := range rawScopes {
		scope := normalizeAgentVisibilityScope(raw)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func writeValidationAgentFile(data []byte) (string, error) {
	file, err := os.CreateTemp("", "agent-definition-*.yml")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func persistEditableAgent(source EditableAgentSource, definition map[string]any, soulPrompt *string, agentsPrompt *string, allowStandalone bool) error {
	if source.Kind == "file" && !allowStandalone {
		return fmt.Errorf("new agents must use directory source")
	}
	if source.Path == "" {
		return fmt.Errorf("agent path is required")
	}
	if err := writeFileAtomic(source.Path, renderYAMLMap(definition), 0o644); err != nil {
		return err
	}
	if source.Kind != "directory" {
		return nil
	}
	if err := writeOptionalPrompt(filepath.Join(source.AgentDir, "SOUL.md"), soulPrompt); err != nil {
		return err
	}
	return writeOptionalPrompt(filepath.Join(source.AgentDir, "AGENTS.md"), agentsPrompt)
}

func writeOptionalPrompt(path string, value *string) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeFileAtomic(path, []byte(strings.TrimRight(*value, "\n")+"\n"), 0o644)
}

func editableFilesFromSource(key string, definition map[string]any, source EditableAgentSource, soulPrompt string, agentsPrompt string) EditableAgentFiles {
	return EditableAgentFiles{
		Key:          key,
		Definition:   contracts.CloneMap(definition),
		SoulPrompt:   strings.TrimSpace(soulPrompt),
		AgentsPrompt: strings.TrimSpace(agentsPrompt),
		Source:       source,
	}
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func renderYAMLMap(node map[string]any) []byte {
	var b strings.Builder
	writeYAMLMap(&b, 0, node)
	return []byte(b.String())
}

func writeYAMLKeyValue(b *strings.Builder, indent int, key string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			writeYAMLLine(b, indent, key+": {}")
			return
		}
		writeYAMLLine(b, indent, key+":")
		writeYAMLMap(b, indent+2, typed)
	case []any:
		if len(typed) == 0 {
			writeYAMLLine(b, indent, key+": []")
			return
		}
		writeYAMLLine(b, indent, key+":")
		writeYAMLList(b, indent+2, typed)
	case []string:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		writeYAMLKeyValue(b, indent, key, items)
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		writeYAMLKeyValue(b, indent, key, items)
	default:
		writeYAMLLine(b, indent, key+": "+formatYAMLScalar(typed, indent))
	}
}

func writeYAMLMap(b *strings.Builder, indent int, node map[string]any) {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if indent == 0 {
		keys = prioritizeYAMLKeys(keys, []string{"key", "name", "mode"})
	}
	for _, key := range keys {
		writeYAMLKeyValue(b, indent, key, node[key])
	}
}

func prioritizeYAMLKeys(keys []string, priority []string) []string {
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(priority))
	for _, wanted := range priority {
		for _, key := range keys {
			if key == wanted {
				out = append(out, key)
				seen[key] = struct{}{}
				break
			}
		}
	}
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, key)
	}
	return out
}

func writeYAMLList(b *strings.Builder, indent int, items []any) {
	for _, item := range items {
		switch typed := item.(type) {
		case map[string]any:
			if len(typed) == 0 {
				writeYAMLLine(b, indent, "- {}")
				continue
			}
			writeYAMLLine(b, indent, "-")
			writeYAMLMap(b, indent+2, typed)
		case []any:
			if len(typed) == 0 {
				writeYAMLLine(b, indent, "- []")
				continue
			}
			writeYAMLLine(b, indent, "-")
			writeYAMLList(b, indent+2, typed)
		default:
			writeYAMLLine(b, indent, "- "+formatYAMLScalar(typed, indent))
		}
	}
}

func writeYAMLLine(b *strings.Builder, indent int, line string) {
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(line)
	b.WriteByte('\n')
}

func formatYAMLScalar(value any, indent int) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return quoteOrBlockYAMLString(typed, indent)
	default:
		return quoteOrBlockYAMLString(fmt.Sprint(typed), indent)
	}
}

func quoteOrBlockYAMLString(value string, indent int) string {
	sanitized := strings.ReplaceAll(value, "\r\n", "\n")
	if strings.Contains(sanitized, "\n") {
		lines := strings.Split(strings.TrimRight(sanitized, "\n"), "\n")
		var b strings.Builder
		b.WriteString("|")
		for _, line := range lines {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent+2))
			b.WriteString(line)
		}
		return b.String()
	}
	if canUsePlainYAMLScalar(sanitized) {
		return sanitized
	}
	if !strings.Contains(sanitized, "'") {
		return "'" + sanitized + "'"
	}
	if !strings.Contains(sanitized, `"`) {
		return `"` + sanitized + `"`
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(sanitized, `\`, `\\`), `"`, `\"`) + `"`
}

func canUsePlainYAMLScalar(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	lower := strings.ToLower(value)
	switch lower {
	case "true", "false", "null", "~", "[]", "{}":
		return false
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return false
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil && strings.Contains(value, ".") {
		return false
	}
	if strings.Contains(value, ": ") || strings.ContainsAny(value, "#\t") {
		return false
	}
	switch value[0] {
	case '-', '?', ':', '[', ']', '{', '}', ',', '&', '*', '!', '|', '>', '@', '`':
		return false
	}
	return true
}
