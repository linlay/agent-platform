package hitl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/bashast"
)

type WorkspaceCheckInput struct {
	Command       string
	Cwd           string
	WorkspaceRoot string
	ChatLevel     int
}

func CheckWorkspaceAccess(input WorkspaceCheckInput) InterceptResult {
	root, ok := normalizeWorkspacePath(input.WorkspaceRoot, "")
	if !ok {
		return InterceptResult{}
	}
	cwd := strings.TrimSpace(input.Cwd)
	if cwd == "" {
		cwd = root
	}
	resolvedCwd, ok := normalizeWorkspacePath(cwd, root)
	if !ok {
		return InterceptResult{}
	}
	if !pathWithinRoot(resolvedCwd, root) {
		return workspaceIntercept(input.Command, fmt.Sprintf("cwd %s", resolvedCwd), root, 1, input.ChatLevel)
	}

	result := bashast.ParseForSecurity(strings.TrimSpace(input.Command))
	if result.Kind != bashast.Simple {
		return InterceptResult{}
	}
	for _, cmd := range result.Commands {
		if path := firstOutsidePath(cmd, resolvedCwd, root); path != "" {
			return workspaceIntercept(input.Command, fmt.Sprintf("path %s", path), root, 1, input.ChatLevel)
		}
	}
	return InterceptResult{}
}

func firstOutsidePath(cmd bashast.SimpleCommand, cwd string, root string) string {
	for _, redirect := range cmd.Redirects {
		if redirect.IsHeredoc {
			continue
		}
		if outside, ok := outsideWorkspacePath(redirect.Target, cwd, root); ok {
			return outside
		}
	}
	if len(cmd.Argv) <= 1 {
		return ""
	}
	for _, arg := range cmd.Argv[1:] {
		for _, candidate := range pathCandidates(arg) {
			if outside, ok := outsideWorkspacePath(candidate, cwd, root); ok {
				return outside
			}
		}
	}
	return ""
}

func pathCandidates(arg string) []string {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.Contains(arg, "://") || strings.HasPrefix(arg, "git@") {
		return nil
	}
	if strings.HasPrefix(arg, "-") {
		if _, value, ok := strings.Cut(arg, "="); ok {
			arg = strings.TrimSpace(value)
		} else {
			return nil
		}
	}
	if strings.HasPrefix(arg, "~") ||
		filepath.IsAbs(arg) ||
		strings.HasPrefix(arg, "../") ||
		arg == ".." ||
		strings.HasPrefix(arg, "./") ||
		strings.Contains(arg, string(filepath.Separator)) {
		return []string{arg}
	}
	return nil
}

func outsideWorkspacePath(raw string, cwd string, root string) (string, bool) {
	path, ok := normalizeWorkspacePath(raw, cwd)
	if !ok {
		return "", false
	}
	if pathWithinRoot(path, root) {
		return "", false
	}
	return path, true
}

func normalizeWorkspacePath(raw string, base string) (string, bool) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", false
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		if strings.TrimSpace(base) == "" {
			return "", false
		}
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		path = filepath.Clean(evaluated)
	}
	return path, true
}

func pathWithinRoot(path string, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func workspaceIntercept(command string, matched string, root string, level int, chatLevel int) InterceptResult {
	if chatLevel >= level {
		return InterceptResult{}
	}
	hash := sha256.Sum256([]byte(root + "\x00" + matched))
	ruleKey := "workspace::" + hex.EncodeToString(hash[:8])
	return InterceptResult{
		Intercepted: true,
		Rule: FlatRule{
			RuleKey:      ruleKey,
			Command:      "workspace",
			Match:        matched,
			Level:        level,
			Title:        "Workspace access approval",
			ViewportType: "builtin",
			ViewportKey:  "approval",
		},
		OriginalCommand: strings.TrimSpace(command),
		MatchedCommand:  matched,
		MatchedWhole:    true,
	}
}
