package schedule

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"

	"github.com/robfig/cron/v3"
)

var (
	cronParser  = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type TeamLookup interface {
	TeamDefinition(teamID string) (catalog.TeamDefinition, bool)
}

type Registry struct {
	root  string
	teams TeamLookup
}

func NewRegistry(root string, teams TeamLookup) *Registry {
	return &Registry{root: root, teams: teams}
}

func (r *Registry) Load() ([]Definition, error) {
	entries, err := os.ReadDir(r.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	byName := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
		byName[entry.Name()] = entry
	}
	sort.Strings(names)

	defs := make([]Definition, 0, len(names))
	for _, name := range names {
		entry := byName[name]
		if entry.IsDir() || !catalog.ShouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		path := filepath.Join(r.root, name)
		def, err := r.parseDefinition(path)
		if err != nil {
			log.Printf("[schedule] skip invalid schedule file %s: %v", path, err)
			continue
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func (r *Registry) parseDefinition(path string) (Definition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return Definition{}, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return Definition{}, fmt.Errorf("schedule file must be a map")
	}

	id := strings.TrimSpace(catalog.LogicalRuntimeBaseName(filepath.Base(path)))
	if id == "" {
		return Definition{}, fmt.Errorf("schedule id is required")
	}

	name := stringNode(root["name"])
	if name == "" {
		return Definition{}, fmt.Errorf("name is required")
	}
	description := stringNode(root["description"])
	if description == "" {
		return Definition{}, fmt.Errorf("description is required")
	}
	cronExpr := stringNode(root["cron"])
	if cronExpr == "" {
		return Definition{}, fmt.Errorf("cron is required")
	}
	if _, err := parseCronSchedule(cronExpr); err != nil {
		return Definition{}, fmt.Errorf("invalid cron %q: %w", cronExpr, err)
	}
	remainingRuns, err := positiveIntPtrNode(root["remainingRuns"], "remainingRuns")
	if err != nil {
		return Definition{}, err
	}

	agentKey := stringNode(root["agentKey"])
	if agentKey == "" {
		return Definition{}, fmt.Errorf("agentKey is required")
	}
	teamID := stringNode(root["teamId"])
	if err := r.validateTeam(agentKey, teamID); err != nil {
		return Definition{}, err
	}

	environmentNode, err := mapNode(root["environment"], true)
	if err != nil {
		return Definition{}, fmt.Errorf("invalid environment: %w", err)
	}
	zoneID := stringNode(environmentNode["zoneId"])
	if zoneID != "" {
		if _, err := time.LoadLocation(zoneID); err != nil {
			return Definition{}, fmt.Errorf("invalid environment.zoneId %q", zoneID)
		}
	}

	queryNode, err := mapNode(root["query"], false)
	if err != nil {
		return Definition{}, fmt.Errorf("invalid query: %w", err)
	}
	query, err := parseQuery(queryNode)
	if err != nil {
		return Definition{}, err
	}

	return Definition{
		ID:            id,
		Name:          name,
		Description:   description,
		Enabled:       boolNode(root["enabled"], true),
		Cron:          cronExpr,
		RemainingRuns: remainingRuns,
		AgentKey:      agentKey,
		TeamID:        teamID,
		Environment:   Environment{ZoneID: zoneID},
		Query:         query,
		PushURL:       stringNode(root["pushUrl"]),
		PushTargetID:  stringNode(root["pushTargetId"]),
		SourceFile:    path,
	}, nil
}

func (r *Registry) validateTeam(agentKey string, teamID string) error {
	if teamID == "" || r == nil || r.teams == nil {
		return nil
	}
	team, ok := r.teams.TeamDefinition(teamID)
	if !ok {
		return fmt.Errorf("team %q not found", teamID)
	}
	for _, key := range team.AgentKeys {
		if key == agentKey {
			return nil
		}
	}
	return fmt.Errorf("agentKey %q is not in team %q", agentKey, teamID)
}

func parseQuery(node map[string]any) (Query, error) {
	message := stringNode(node["message"])
	if message == "" {
		return Query{}, fmt.Errorf("query.message is required")
	}

	requestID, err := optionalStringNode(node, "requestId")
	if err != nil {
		return Query{}, err
	}
	chatID, err := optionalStringNode(node, "chatId")
	if err != nil {
		return Query{}, err
	}
	if chatID != "" && !uuidPattern.MatchString(chatID) {
		return Query{}, fmt.Errorf("invalid query.chatId %q", chatID)
	}
	role, err := optionalStringNode(node, "role")
	if err != nil {
		return Query{}, err
	}
	references, err := parseReferences(node["references"])
	if err != nil {
		return Query{}, err
	}
	params, err := paramsNode(node["params"])
	if err != nil {
		return Query{}, err
	}
	scene, err := sceneNode(node["scene"])
	if err != nil {
		return Query{}, err
	}
	hidden, err := boolPtrNode(node["hidden"])
	if err != nil {
		return Query{}, err
	}

	return Query{
		RequestID:  requestID,
		ChatID:     chatID,
		Role:       role,
		Message:    message,
		References: references,
		Params:     params,
		Scene:      scene,
		Hidden:     hidden,
	}, nil
}

func parseReferences(value any) ([]api.Reference, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid query.references")
	}
	references := make([]api.Reference, 0, len(items))
	for _, item := range items {
		node, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid query.references")
		}
		meta, err := paramsNode(node["meta"])
		if err != nil {
			return nil, fmt.Errorf("invalid query.references.meta")
		}
		sizeBytes, err := int64PtrNode(node["sizeBytes"])
		if err != nil {
			return nil, fmt.Errorf("invalid query.references.sizeBytes")
		}
		references = append(references, api.Reference{
			ID:          stringNode(node["id"]),
			Type:        stringNode(node["type"]),
			Name:        stringNode(node["name"]),
			MimeType:    stringNode(node["mimeType"]),
			SizeBytes:   sizeBytes,
			URL:         stringNode(node["url"]),
			SHA256:      stringNode(node["sha256"]),
			SandboxPath: stringNode(node["sandboxPath"]),
			Meta:        meta,
		})
	}
	return references, nil
}

func (r *Registry) Persist(def Definition) error {
	path, err := r.schedulePath(def)
	if err != nil {
		return err
	}
	data := renderDefinition(def)
	return writeFileAtomic(path, data, 0o644)
}

func (r *Registry) Delete(def Definition) error {
	path, err := r.schedulePath(def)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (r *Registry) schedulePath(def Definition) (string, error) {
	path := strings.TrimSpace(def.SourceFile)
	if path == "" {
		if strings.TrimSpace(r.root) == "" || strings.TrimSpace(def.ID) == "" {
			return "", fmt.Errorf("schedule path is required")
		}
		path = filepath.Join(r.root, def.ID+".yml")
	}
	if strings.TrimSpace(r.root) != "" && !insideDir(r.root, path) {
		return "", fmt.Errorf("schedule path %q is outside root %q", path, r.root)
	}
	return path, nil
}

func paramsNode(value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	node, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object")
	}
	return cloneMap(node), nil
}

func sceneNode(value any) (*api.Scene, error) {
	if value == nil {
		return nil, nil
	}
	node, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid query.scene")
	}
	return &api.Scene{
		URL:   stringNode(node["url"]),
		Title: stringNode(node["title"]),
	}, nil
}

func parseCronSchedule(spec string) (cron.Schedule, error) {
	return cronParser.Parse(strings.TrimSpace(spec))
}

func mapNode(value any, allowNil bool) (map[string]any, error) {
	if value == nil {
		if allowNil {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("expected object")
	}
	node, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object")
	}
	return node, nil
}

func optionalStringNode(node map[string]any, key string) (string, error) {
	value, ok := node[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid query.%s", key)
	}
	return strings.TrimSpace(text), nil
}

func stringNode(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func boolNode(value any, fallback bool) bool {
	switch typed := value.(type) {
	case nil:
		return fallback
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		default:
			return fallback
		}
	default:
		return fallback
	}
}

func boolPtrNode(value any) (*bool, error) {
	if value == nil {
		return nil, nil
	}
	typed, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("invalid query.hidden")
	}
	return &typed, nil
}

func int64PtrNode(value any) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case int:
		result := int64(typed)
		return &result, nil
	case int64:
		result := typed
		return &result, nil
	case float64:
		result := int64(typed)
		return &result, nil
	default:
		return nil, fmt.Errorf("invalid integer")
	}
}

func positiveIntPtrNode(value any, field string) (*int, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case int:
		if typed <= 0 {
			return nil, fmt.Errorf("%s must be a positive integer", field)
		}
		result := typed
		return &result, nil
	case int64:
		if typed <= 0 || typed > math.MaxInt {
			return nil, fmt.Errorf("%s must be a positive integer", field)
		}
		result := int(typed)
		return &result, nil
	case float64:
		if typed != math.Trunc(typed) || typed <= 0 || typed > math.MaxInt {
			return nil, fmt.Errorf("%s must be a positive integer", field)
		}
		result := int(typed)
		return &result, nil
	default:
		return nil, fmt.Errorf("%s must be a positive integer", field)
	}
}

func renderDefinition(def Definition) []byte {
	var b strings.Builder
	writeYAMLKeyValue(&b, 0, "name", def.Name)
	writeYAMLKeyValue(&b, 0, "description", def.Description)
	writeYAMLKeyValue(&b, 0, "enabled", def.Enabled)
	writeYAMLKeyValue(&b, 0, "cron", def.Cron)
	if def.RemainingRuns != nil {
		writeYAMLKeyValue(&b, 0, "remainingRuns", *def.RemainingRuns)
	}
	writeYAMLKeyValue(&b, 0, "agentKey", def.AgentKey)
	if strings.TrimSpace(def.TeamID) != "" {
		writeYAMLKeyValue(&b, 0, "teamId", def.TeamID)
	}
	if strings.TrimSpace(def.Environment.ZoneID) != "" {
		writeYAMLKeyValue(&b, 0, "environment", map[string]any{
			"zoneId": def.Environment.ZoneID,
		})
	}

	writeYAMLLine(&b, 0, "query:")
	if strings.TrimSpace(def.Query.RequestID) != "" {
		writeYAMLKeyValue(&b, 2, "requestId", def.Query.RequestID)
	}
	if strings.TrimSpace(def.Query.ChatID) != "" {
		writeYAMLKeyValue(&b, 2, "chatId", def.Query.ChatID)
	}
	if strings.TrimSpace(def.Query.Role) != "" {
		writeYAMLKeyValue(&b, 2, "role", def.Query.Role)
	}
	writeYAMLKeyValue(&b, 2, "message", def.Query.Message)
	if def.Query.Hidden != nil {
		writeYAMLKeyValue(&b, 2, "hidden", *def.Query.Hidden)
	}
	if len(def.Query.Params) > 0 {
		writeYAMLKeyValue(&b, 2, "params", def.Query.Params)
	}
	if def.Query.Scene != nil {
		scene := map[string]any{}
		if strings.TrimSpace(def.Query.Scene.URL) != "" {
			scene["url"] = def.Query.Scene.URL
		}
		if strings.TrimSpace(def.Query.Scene.Title) != "" {
			scene["title"] = def.Query.Scene.Title
		}
		if len(scene) > 0 {
			writeYAMLKeyValue(&b, 2, "scene", scene)
		}
	}
	if len(def.Query.References) > 0 {
		items := make([]any, 0, len(def.Query.References))
		for _, ref := range def.Query.References {
			node := map[string]any{}
			if strings.TrimSpace(ref.ID) != "" {
				node["id"] = ref.ID
			}
			if strings.TrimSpace(ref.Type) != "" {
				node["type"] = ref.Type
			}
			if strings.TrimSpace(ref.Name) != "" {
				node["name"] = ref.Name
			}
			if strings.TrimSpace(ref.MimeType) != "" {
				node["mimeType"] = ref.MimeType
			}
			if ref.SizeBytes != nil {
				node["sizeBytes"] = *ref.SizeBytes
			}
			if strings.TrimSpace(ref.URL) != "" {
				node["url"] = ref.URL
			}
			if strings.TrimSpace(ref.SHA256) != "" {
				node["sha256"] = ref.SHA256
			}
			if strings.TrimSpace(ref.SandboxPath) != "" {
				node["sandboxPath"] = ref.SandboxPath
			}
			if len(ref.Meta) > 0 {
				node["meta"] = ref.Meta
			}
			items = append(items, node)
		}
		writeYAMLKeyValue(&b, 2, "references", items)
	}

	if strings.TrimSpace(def.PushURL) != "" {
		writeYAMLKeyValue(&b, 0, "pushUrl", def.PushURL)
	}
	if strings.TrimSpace(def.PushTargetID) != "" {
		writeYAMLKeyValue(&b, 0, "pushTargetId", def.PushTargetID)
	}

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
	default:
		writeYAMLLine(b, indent, key+": "+formatYAMLScalar(typed))
	}
}

func writeYAMLMap(b *strings.Builder, indent int, node map[string]any) {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeYAMLKeyValue(b, indent, key, node[key])
	}
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
			writeYAMLLine(b, indent, "- "+formatYAMLScalar(typed))
		}
	}
}

func writeYAMLLine(b *strings.Builder, indent int, line string) {
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(line)
	b.WriteByte('\n')
}

func formatYAMLScalar(value any) string {
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
		return quoteYAMLString(typed)
	default:
		return quoteYAMLString(fmt.Sprint(typed))
	}
}

func quoteYAMLString(value string) string {
	sanitized := strings.ReplaceAll(value, "\r\n", "\n")
	sanitized = strings.ReplaceAll(sanitized, "\n", "\\n")
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
	return !strings.ContainsAny(value, "\n\r")
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

func insideDir(parent string, child string) bool {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
