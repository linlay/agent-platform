package hitl

import (
	"sort"
	"strings"

	"agent-platform-runner-go/internal/api"
)

type SkillChecker struct {
	rules []FlatRule
	byCmd map[string][]FlatRule
	tools map[string]api.ToolDetailResponse
}

func NewSkillChecker(skillHookDirs []string) (*SkillChecker, error) {
	var combined []FlatRule
	for _, dir := range skillHookDirs {
		rules, err := loadRulesFromDir(strings.TrimSpace(dir))
		if err != nil {
			return nil, err
		}
		combined = append(combined, rules...)
	}
	deduped := make([]FlatRule, 0, len(combined))
	byKey := make(map[string]int, len(combined))
	for _, rule := range combined {
		key := rule.Command + "\x00" + strings.ToLower(strings.TrimSpace(rule.Match))
		if idx, ok := byKey[key]; ok {
			if rule.Level > deduped[idx].Level {
				deduped[idx] = rule
			}
			continue
		}
		byKey[key] = len(deduped)
		deduped = append(deduped, rule)
	}
	byCmd, tools := buildIndexes(deduped)
	return &SkillChecker{
		rules: deduped,
		byCmd: byCmd,
		tools: tools,
	}, nil
}

func (c *SkillChecker) Check(command string, chatLevel int) InterceptResult {
	if c == nil {
		return InterceptResult{}
	}
	return checkRules(c.byCmd, command, chatLevel)
}

func (c *SkillChecker) Tool(name string) (api.ToolDetailResponse, bool) {
	if c == nil {
		return api.ToolDetailResponse{}, false
	}
	def, ok := c.tools[strings.ToLower(strings.TrimSpace(name))]
	return def, ok
}

func (c *SkillChecker) Tools() []api.ToolDetailResponse {
	if c == nil {
		return nil
	}
	return toolList(c.tools)
}

func buildIndexes(rules []FlatRule) (map[string][]FlatRule, map[string]api.ToolDetailResponse) {
	byCmd := make(map[string][]FlatRule, len(rules))
	tools := make(map[string]api.ToolDetailResponse, len(rules))
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
	return byCmd, tools
}

func checkRules(byCmd map[string][]FlatRule, command string, chatLevel int) InterceptResult {
	result, _ := betterInterceptResult(
		evaluateParsedCommand(byCmd, command, command, ParseCommandComponents(command), chatLevel, true),
		InterceptResult{},
	)
	for _, segment := range splitShellLikeSegments(command) {
		parsed := parseCommandTokens(splitShellLikeTokens(segment))
		result, _ = betterInterceptResult(
			result,
			evaluateParsedCommand(byCmd, command, segment, parsed, chatLevel, false),
		)
	}
	return result
}

func evaluateParsedCommand(
	byCmd map[string][]FlatRule,
	originalCommand string,
	matchedCommand string,
	parsed CommandComponents,
	chatLevel int,
	matchedWhole bool,
) InterceptResult {
	base := strings.ToLower(strings.TrimSpace(parsed.BaseCommand))
	if base == "" {
		return InterceptResult{}
	}
	candidates := append([]FlatRule(nil), byCmd[base]...)
	best := InterceptResult{}
	for _, rule := range candidates {
		if !matchesRule(matchedCommand, parsed, rule) {
			continue
		}
		candidate := InterceptResult{
			Intercepted:     chatLevel < rule.Level,
			Rule:            rule,
			ParsedCommand:   parsed,
			OriginalCommand: originalCommand,
			MatchedCommand:  matchedCommand,
			MatchedWhole:    matchedWhole,
		}
		best, _ = betterInterceptResult(best, candidate)
	}
	return best
}

func betterInterceptResult(current InterceptResult, candidate InterceptResult) (InterceptResult, bool) {
	if strings.TrimSpace(candidate.Rule.Command) == "" {
		return current, false
	}
	if strings.TrimSpace(current.Rule.Command) == "" {
		return candidate, true
	}
	if candidate.Rule.Level != current.Rule.Level {
		if candidate.Rule.Level > current.Rule.Level {
			return candidate, true
		}
		return current, false
	}
	if len(candidate.Rule.MatchTokens) != len(current.Rule.MatchTokens) {
		if len(candidate.Rule.MatchTokens) > len(current.Rule.MatchTokens) {
			return candidate, true
		}
		return current, false
	}
	if candidate.Intercepted != current.Intercepted {
		if candidate.Intercepted {
			return candidate, true
		}
		return current, false
	}
	if candidate.Rule.SourcePath != current.Rule.SourcePath {
		if candidate.Rule.SourcePath < current.Rule.SourcePath {
			return candidate, true
		}
		return current, false
	}
	if candidate.Rule.Order < current.Rule.Order {
		return candidate, true
	}
	return current, false
}

func toolList(tools map[string]api.ToolDetailResponse) []api.ToolDetailResponse {
	if len(tools) == 0 {
		return nil
	}
	keys := make([]string, 0, len(tools))
	for key := range tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]api.ToolDetailResponse, 0, len(keys))
	for _, key := range keys {
		out = append(out, tools[key])
	}
	return out
}
