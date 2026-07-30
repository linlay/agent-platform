package hitl

import (
	"strings"
	"sync"
)

type Registry struct {
	root string

	mu      sync.RWMutex
	version int64
	rules   []FlatRule
	byCmd   map[string][]FlatRule
}

func NewRegistry(root string) (*Registry, error) {
	registry := &Registry{
		root:  root,
		byCmd: map[string][]FlatRule{},
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
	byCmd := buildIndexes(rules)

	r.mu.Lock()
	r.rules = append([]FlatRule(nil), rules...)
	r.byCmd = byCmd
	r.version++
	r.mu.Unlock()
	return nil
}

func (r *Registry) Check(command string, chatLevel int) InterceptResult {
	r.mu.RLock()
	byCmd := r.byCmd
	r.mu.RUnlock()
	return checkRules(byCmd, command, chatLevel)
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
	var (
		hit       bool
		remaining []string
	)
	if len(rule.MatchTokens) > 0 && rule.MatchTokens[0] == "|" {
		hit, remaining = matchesPipelineTokensWithRemaining(command, rule.MatchTokens[1:])
	} else {
		hit = matchesTokens(parsed.Tokens, rule.MatchTokens)
		if hit {
			remaining = parsed.Tokens[len(rule.MatchTokens):]
		}
	}
	if !hit {
		return false
	}
	return !hasPassThroughFlag(remaining, rule.PassThroughFlags)
}

func matchesPipelineTokensWithRemaining(command string, matchTokens []string) (bool, []string) {
	if len(matchTokens) == 0 {
		return false, nil
	}
	segments := splitShellLikePipelineSegments(command)
	if len(segments) < 2 {
		return false, nil
	}
	parsed := parseCommandTokens(splitShellLikeTokens(segments[1]))
	if strings.TrimSpace(parsed.BaseCommand) == "" {
		return false, nil
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
	if !matchesTokens(commandTokens, matchTokens) {
		return false, nil
	}
	return true, commandTokens[len(matchTokens):]
}

func hasPassThroughFlag(argTokens []string, flags []string) bool {
	if len(flags) == 0 || len(argTokens) == 0 {
		return false
	}
	flagSet := make(map[string]struct{}, len(flags))
	for _, flag := range flags {
		flag = strings.ToLower(strings.TrimSpace(flag))
		if flag == "" {
			continue
		}
		flagSet[flag] = struct{}{}
	}
	for _, token := range argTokens {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			continue
		}
		if _, ok := flagSet[token]; ok {
			return true
		}
	}
	return false
}
