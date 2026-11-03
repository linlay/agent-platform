package resources

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

type RedirectAccessKind int

const (
	RedirectAccessNeutral RedirectAccessKind = iota
	RedirectAccessRead
	RedirectAccessWrite
	RedirectAccessUnknown
)

func ClassifyRedirectAccess(redirect types.Redirect) RedirectAccessKind {
	if redirect.IsHeredoc {
		return RedirectAccessNeutral
	}
	op := strings.TrimSpace(redirect.Op)
	target := strings.TrimSpace(redirect.Target)
	if op == "" {
		return RedirectAccessUnknown
	}
	if op == "<<<" {
		return RedirectAccessNeutral
	}
	switch op {
	case "<&", ">&":
		if isFileDescriptorRedirectTarget(target) {
			return RedirectAccessNeutral
		}
		if op == ">&" {
			return RedirectAccessWrite
		}
		return RedirectAccessUnknown
	case "<":
		return RedirectAccessRead
	case ">", ">>", ">|", ">>|", "&>", "&>|", "&>>", "&>>|":
		return RedirectAccessWrite
	case "<>":
		return RedirectAccessWrite
	default:
		if strings.Contains(op, ">") {
			return RedirectAccessWrite
		}
		if strings.Contains(op, "<") {
			return RedirectAccessRead
		}
		return RedirectAccessUnknown
	}
}

func isFileDescriptorRedirectTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "-" {
		return true
	}
	if target == "" {
		return false
	}
	for _, r := range target {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RedirectEffects reports resources opened by the shell, independently of the
// called program's argument grammar. File-descriptor duplication is neutral.
func RedirectEffects(redirects []types.Redirect) types.Effects {
	var effects types.Effects
	for _, redirect := range redirects {
		access := ClassifyRedirectAccess(redirect)
		if access == RedirectAccessNeutral {
			continue
		}
		if access == RedirectAccessUnknown || redirect.Target == "" {
			effects.Unknowns = append(effects.Unknowns, types.Unknown{Kind: types.UnknownSyntax, Arg: -1, Rule: "shell:redirection"})
			if redirect.Target == "" {
				continue
			}
		}
		kind := types.Read
		role := types.Source
		if access == RedirectAccessWrite || access == RedirectAccessUnknown {
			kind = types.Output
			role = types.Destination
		}
		word := types.ValueSource(types.CommandInput{Args: []string{redirect.Target}, Words: []types.SourceWord{{WordSpan: redirect.TargetWord, Arg: -1, Origin: types.WordOriginSyntax}}}, types.ArgumentRef(0, redirect.Target), false)
		if strings.TrimSpace(redirect.Op) == "<>" {
			effects.Ops = append(effects.Ops, types.Op{Kind: types.Read, Targets: []types.Target{{Path: redirect.Target, Role: types.Source, Word: word}}, Origin: types.ShellRedirect, Arg: -1, Rule: "shell:<>:read"})
		}
		effects.Ops = append(effects.Ops, types.Op{Kind: kind, Targets: []types.Target{{Path: redirect.Target, Role: role, Word: word}}, Origin: types.ShellRedirect, Arg: -1, Rule: "shell:" + redirect.Op})
	}
	return effects
}
