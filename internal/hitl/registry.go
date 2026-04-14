package hitl

import (
	"sort"
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
	byCmd := make(map[string][]FlatRule, len(rules))
	tools := map[string]api.ToolDetailResponse{}
	for _, rule := range rules {
		byCmd[rule.Command] = append(byCmd[rule.Command], rule)
		toolName := syntheticToolName(rule.ViewportKey)
		if _, exists := tools[toolName]; !exists {
			tools[toolName] = buildSyntheticToolDefinition(rule)
		}
	}
	for command := range byCmd {
		sort.SliceStable(byCmd[command], func(i int, j int) bool {
			left := byCmd[command][i]
			right := byCmd[command][j]
			if len(left.MatchTokens) != len(right.MatchTokens) {
				return len(left.MatchTokens) > len(right.MatchTokens)
			}
			if left.SourcePath != right.SourcePath {
				return left.SourcePath < right.SourcePath
			}
			return left.Order < right.Order
		})
	}

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

func (r *Registry) Check(command string, chatLevel int) InterceptResult {
	parsed := ParseCommandComponents(command)
	base := strings.ToLower(strings.TrimSpace(parsed.BaseCommand))
	if base == "" {
		return InterceptResult{}
	}

	r.mu.RLock()
	candidates := append([]FlatRule(nil), r.byCmd[base]...)
	r.mu.RUnlock()

	for _, rule := range candidates {
		if !matchesTokens(parsed.Tokens, rule.MatchTokens) {
			continue
		}
		if chatLevel >= rule.Level {
			return InterceptResult{
				Intercepted:     false,
				Rule:            rule,
				ParsedCommand:   parsed,
				OriginalCommand: command,
			}
		}
		return InterceptResult{
			Intercepted:     true,
			Rule:            rule,
			ParsedCommand:   parsed,
			OriginalCommand: command,
		}
	}
	return InterceptResult{}
}

func syntheticToolName(viewportKey string) string {
	return "_hitl_" + strings.TrimSpace(viewportKey) + "_"
}

func matchesTokens(commandTokens []string, matchTokens []string) bool {
	if len(matchTokens) == 0 || len(commandTokens) < len(matchTokens) {
		return false
	}
	for idx := range matchTokens {
		if strings.ToLower(strings.TrimSpace(commandTokens[idx])) != matchTokens[idx] {
			return false
		}
	}
	return true
}

func buildSyntheticToolDefinition(rule FlatRule) api.ToolDetailResponse {
	name := syntheticToolName(rule.ViewportKey)
	return api.ToolDetailResponse{
		Key:         name,
		Name:        name,
		Label:       "Bash HITL Approval",
		Description: "Synthetic HITL approval tool for intercepted bash commands.",
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
