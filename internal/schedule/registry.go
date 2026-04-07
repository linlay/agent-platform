package schedule

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
		ID:          id,
		Name:        name,
		Description: description,
		Enabled:     boolNode(root["enabled"], true),
		Cron:        cronExpr,
		AgentKey:    agentKey,
		TeamID:      teamID,
		Environment: Environment{ZoneID: zoneID},
		Query:       query,
		SourceFile:  path,
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
