package hitl

import (
	"strings"
	"sync"

	"agent-platform-runner-go/internal/api"
)

type Registry struct {
	root string

	mu      sync.RWMutex
	version int64
	rules   []FlatRule
	byCmd   map[string][]FlatRule
	tools   map[string]api.ToolDetailResponse
}

func NewRegistry(root string) (*Registry, error) {
	registry := &Registry{
		root:  root,
		byCmd: map[string][]FlatRule{},
		tools: map[string]api.ToolDetailResponse{},
	}
	if err := registry.Reload(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Reload() error {
	rules, err := loadRulesFromDir(r.root)
	if err != nil {
		return err
	}
	byCmd, tools := buildIndexes(rules)

	r.mu.Lock()
	r.rules = append([]FlatRule(nil), rules...)
	r.byCmd = byCmd
	r.tools = tools
	r.version++
	r.mu.Unlock()
	return nil
}

func (r *Registry) Version() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

func (r *Registry) Rules() []FlatRule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]FlatRule(nil), r.rules...)
}

func (r *Registry) Tool(name string) (api.ToolDetailResponse, bool) {
	if r == nil {
		return api.ToolDetailResponse{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.tools[strings.ToLower(strings.TrimSpace(name))]
	return def, ok
}

func (r *Registry) Tools() []api.ToolDetailResponse {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return toolList(r.tools)
}

func (r *Registry) Check(command string, chatLevel int) InterceptResult {
	r.mu.RLock()
	byCmd := r.byCmd
	r.mu.RUnlock()
	return checkRules(byCmd, command, chatLevel)
}

func syntheticToolName(viewportKey string) string {
	return "_hitl_" + strings.TrimSpace(viewportKey) + "_"
}

func matchesTokens(commandTokens []string, matchTokens []string) bool {
	if len(matchTokens) == 0 {
		return true
	}
	if len(commandTokens) < len(matchTokens) {
		return false
	}
	for idx := range matchTokens {
		if strings.ToLower(strings.TrimSpace(commandTokens[idx])) != matchTokens[idx] {
			return false
		}
	}
	return true
}

func matchesRule(command string, parsed CommandComponents, rule FlatRule) bool {
	if len(rule.MatchTokens) > 0 && rule.MatchTokens[0] == "|" {
		return matchesPipelineTokens(command, rule.MatchTokens[1:])
	}
	return matchesTokens(parsed.Tokens, rule.MatchTokens)
}

func matchesPipelineTokens(command string, matchTokens []string) bool {
	if len(matchTokens) == 0 {
		return false
	}
	segments := splitShellLikePipelineSegments(command)
	if len(segments) < 2 {
		return false
	}
	parsed := parseCommandTokens(splitShellLikeTokens(segments[1]))
	if strings.TrimSpace(parsed.BaseCommand) == "" {
		return false
	}
	commandTokens := make([]string, 0, len(parsed.Tokens)+1)
	commandTokens = append(commandTokens, strings.ToLower(strings.TrimSpace(parsed.BaseCommand)))
	for _, token := range parsed.Tokens {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			continue
		}
		commandTokens = append(commandTokens, token)
	}
	return matchesTokens(commandTokens, matchTokens)
}

func buildSyntheticToolDefinition(rule FlatRule) api.ToolDetailResponse {
	name := syntheticToolName(rule.ViewportKey)
	return api.ToolDetailResponse{
		Key:         name,
		Name:        name,
		Label:       "Bash HITL Viewport Metadata",
		Description: "Synthetic metadata entry for Bash HITL viewport lookup. Intercepted bash commands emit awaiting events directly and no longer synthesize approval tool calls.",
		Parameters: map[string]any{
			"type": "object",
		},
		Meta: map[string]any{
			"kind":          "frontend",
			"sourceType":    "hitl",
			"sourceKey":     rule.FileKey,
			"clientVisible": true,
			"viewportType":  rule.ViewportType,
			"viewportKey":   rule.ViewportKey,
		},
	}
}
