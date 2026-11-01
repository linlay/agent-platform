package resources

import (
	"path/filepath"
	"strings"

	"agent-platform/internal/pathutil"
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
	if isDevNullRedirectTarget(target) {
		return RedirectAccessNeutral
	}
	switch op {
	case "<&", ">&":
		if isFileDescriptorRedirectTarget(target) {
			return RedirectAccessNeutral
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

func isDevNullRedirectTarget(target string) bool {
	return filepath.ToSlash(filepath.Clean(pathutil.ExpandHome(strings.TrimSpace(target)))) == "/dev/null"
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
