package automation

import (
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"

	"github.com/robfig/cron/v3"
)

var (
	cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	// chatId 不做结构性校验——platform 只是透传。真正的格式由对端 channel bridge
	// 定义并在自己那头解析（wecom#..., feishu#..., wxmp#..., 等等）。
	// 这里仅用宽口径允许列表过滤 YAML 注入 / 控制字符 / 空白等危险字符。
	chatIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_\-.:@#/~+]+$`)
	chatIDMaxLength = 256
)

type TeamLookup interface {
	ResolveTeam(teamID string) (catalog.TeamSnapshot, bool)
}

type Registry struct {
	root  string
	teams TeamLookup
}

func NewRegistry(root string, teams TeamLookup) *Registry {
	return &Registry{root: root, teams: teams}
}

func (r *Registry) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

func (r *Registry) Load() ([]Definition, error) {
	paths, err := collectAutomationPaths(r.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	defs := make([]Definition, 0, len(paths))
	loadedByID := make(map[string]string, len(paths))
	for _, path := range paths {
		def, err := r.parseDefinition(path)
		if err != nil {
			log.Printf("[automation] skip invalid automation file %s: %v", path, err)
			continue
		}
		if existing, ok := loadedByID[def.ID]; ok {
			log.Printf("[automation] skip duplicate automation id=%s from %s (already loaded from %s)", def.ID, path, existing)
			continue
		}
		loadedByID[def.ID] = path
		defs = append(defs, def)
	}
	return defs, nil
}

func collectAutomationPaths(root string) ([]string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil
	}

	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}

		name := strings.TrimSpace(d.Name())
		if d.IsDir() {
			if !shouldTraverseAutomationDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isAutomationRuntimeFile(path) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(paths)
	return paths, nil
}

func (r *Registry) parseDefinition(path string) (Definition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return Definition{}, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return Definition{}, fmt.Errorf("automation file must be a map")
	}

	id := strings.TrimSpace(catalog.LogicalRuntimeBaseName(filepath.Base(path)))
	if id == "" {
		return Definition{}, fmt.Errorf("automation id is required")
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
	if _, err := parseCronAutomation(cronExpr); err != nil {
		return Definition{}, fmt.Errorf("invalid cron %q: go automation supports only traditional 5-field cron (minute hour day-of-month month day-of-week): %w", cronExpr, err)
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
		SourceFile:    path,
	}, nil
}

func (r *Registry) validateTeam(agentKey string, teamID string) error {
	if teamID == "" || r == nil || r.teams == nil {
		return nil
	}
	team, ok := r.teams.ResolveTeam(teamID)
	if !ok {
		return fmt.Errorf("team %q not found", teamID)
	}
	if !team.DefaultAgentValid {
		return fmt.Errorf("team %q has invalid default agent %q (%s)", teamID, team.DefaultAgentKey, team.DefaultAgentState)
	}
	if team.HasAgent(agentKey) {
		return nil
	}
	if team.DeclaresAgent(agentKey) {
		return fmt.Errorf("agentKey %q is unavailable in team %q", agentKey, teamID)
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
	if chatID != "" {
		if len(chatID) > chatIDMaxLength {
			return Query{}, fmt.Errorf("invalid query.chatId: length %d exceeds %d", len(chatID), chatIDMaxLength)
		}
		if !chatIDPattern.MatchString(chatID) {
			return Query{}, fmt.Errorf("invalid query.chatId %q", chatID)
		}
	}
	role, err := optionalStringNode(node, "role")
	if err != nil {
		return Query{}, err
	}
	role, err = normalizeAutomationQueryRole(role)
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

	return Query{
		RequestID:  requestID,
		ChatID:     chatID,
		Role:       role,
		Message:    message,
		References: references,
		Params:     params,
		Scene:      scene,
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
		if _, ok := node["sandboxPath"]; ok {
			return nil, fmt.Errorf("query.references.sandboxPath has been removed; use path")
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
			ID:        stringNode(node["id"]),
			Type:      stringNode(node["type"]),
			Name:      stringNode(node["name"]),
			Path:      stringNode(node["path"]),
			MimeType:  stringNode(node["mimeType"]),
			SizeBytes: sizeBytes,
			URL:       stringNode(node["url"]),
			SHA256:    stringNode(node["sha256"]),
			Meta:      meta,
		})
	}
	return references, nil
}

func (r *Registry) Persist(def Definition) error {
	if err := r.Validate(def); err != nil {
		return err
	}
	path, err := r.automationPath(def)
	if err != nil {
		return err
	}
	data := renderDefinition(def)
	return writeFileAtomic(path, data, 0o644)
}

func (r *Registry) Validate(def Definition) error {
	if strings.TrimSpace(def.ID) == "" {
		return fmt.Errorf("automation id is required")
	}
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(def.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if strings.TrimSpace(def.Cron) == "" {
		return fmt.Errorf("cron is required")
	}
	if _, err := parseCronAutomation(def.Cron); err != nil {
		return fmt.Errorf("invalid cron %q: go automation supports only traditional 5-field cron (minute hour day-of-month month day-of-week): %w", def.Cron, err)
	}
	if def.RemainingRuns != nil && *def.RemainingRuns <= 0 {
		return fmt.Errorf("remainingRuns must be a positive integer")
	}
	if strings.TrimSpace(def.AgentKey) == "" {
		return fmt.Errorf("agentKey is required")
	}
	if err := r.validateTeam(strings.TrimSpace(def.AgentKey), strings.TrimSpace(def.TeamID)); err != nil {
		return err
	}
	if strings.TrimSpace(def.Environment.ZoneID) != "" {
		if _, err := time.LoadLocation(strings.TrimSpace(def.Environment.ZoneID)); err != nil {
			return fmt.Errorf("invalid environment.zoneId %q", def.Environment.ZoneID)
		}
	}
	if strings.TrimSpace(def.Query.Message) == "" {
		return fmt.Errorf("query.message is required")
	}
	if _, err := normalizeAutomationQueryRole(def.Query.Role); err != nil {
		return err
	}
	chatID := strings.TrimSpace(def.Query.ChatID)
	if chatID != "" {
		if len(chatID) > chatIDMaxLength {
			return fmt.Errorf("invalid query.chatId: length %d exceeds %d", len(chatID), chatIDMaxLength)
		}
		if !chatIDPattern.MatchString(chatID) {
			return fmt.Errorf("invalid query.chatId %q", chatID)
		}
	}
	return nil
}

func normalizeAutomationQueryRole(role string) (string, error) {
	if strings.TrimSpace(role) == "" {
		return api.QueryRoleAutomation, nil
	}
	normalized, ok := api.NormalizeQueryRole(role)
	if !ok {
		return "", fmt.Errorf("invalid query.role: %s", api.QueryRoleValidationMessage)
	}
	return normalized, nil
}

func (r *Registry) Delete(def Definition) error {
	path, err := r.automationPath(def)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (r *Registry) automationPath(def Definition) (string, error) {
	path := strings.TrimSpace(def.SourceFile)
	if path == "" {
		if strings.TrimSpace(r.root) == "" || strings.TrimSpace(def.ID) == "" {
			return "", fmt.Errorf("automation path is required")
		}
		path = filepath.Join(r.root, def.ID+".yml")
	}
	if strings.TrimSpace(r.root) != "" && !insideDir(r.root, path) {
		return "", fmt.Errorf("automation path %q is outside root %q", path, r.root)
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
	return contracts.CloneMap(node), nil
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

func parseCronAutomation(spec string) (cron.Schedule, error) {
	return cronParser.Parse(strings.TrimSpace(spec))
}

func shouldTraverseAutomationDir(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	return catalog.ShouldLoadRuntimeName(name)
}

func isAutomationRuntimeFile(path string) bool {
	name := strings.TrimSpace(filepath.Base(path))
	if name == "" || strings.HasSuffix(name, ".tmp") {
		return false
	}
	if !catalog.ShouldLoadRuntimeName(name) {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yml", ".yaml":
		return true
	default:
		return false
	}
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
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
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
