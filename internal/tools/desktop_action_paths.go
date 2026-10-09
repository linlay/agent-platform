package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// Only these WebApp input fields have filesystem semantics. Other Action data
// (including nested application configuration) must remain untouched.
var desktopActionPathFields = map[string][]string{
	"desktop.webapp.package.init":     {"projectPath"},
	"desktop.webapp.package.validate": {"projectPath", "archivePath"},
	"desktop.webapp.package.build":    {"projectPath", "outputPath"},
	"desktop.webapp.install":          {"archivePath"},
}

type desktopActionPathError struct {
	field string
	input string
	err   error
}

func resolveDesktopActionPaths(session QuerySession, action string, args map[string]any) (map[string]any, *desktopActionPathError) {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	for _, field := range desktopActionPathFields[action] {
		raw, ok := args[field].(string)
		// Legacy relative paths and type/required validation stay with Desktop.
		if !ok || !strings.HasPrefix(strings.TrimSpace(raw), "@") {
			continue
		}
		var resolved string
		var err error
		if action == "desktop.webapp.install" {
			// Installation consumes one Desktop-host ZIP, including a Chat ZIP
			// outside the project Workspace. Tooling keeps its Workspace boundary.
			_, candidate, resolveErr := resolveDesktopActionAliasTarget(session, raw)
			resolved, err = candidate.Host, resolveErr
		} else {
			resolved, err = resolveDesktopActionAlias(session, raw)
		}
		if err != nil {
			return nil, &desktopActionPathError{field: field, input: raw, err: err}
		}
		out[field] = resolved
	}
	return out, nil
}

func resolveDesktopActionAlias(session QuerySession, raw string) (string, error) {
	workspace := accesspolicy.SessionWorkspaceRoot(session)
	if workspace == "" {
		return "", fmt.Errorf("workspace_unavailable: a trusted Session Workspace is required; @chat does not replace it")
	}
	_, candidate, err := resolveDesktopActionAliasTarget(session, raw)
	if err != nil {
		return "", err
	}
	root, err := pathutil.Canonicalize(workspace)
	if err != nil {
		return "", err
	}
	if !pathutil.WithinRoot(candidate, root) {
		return "", fmt.Errorf("resolved path %q is outside the current Workspace %q", candidate.Host, root.Host)
	}
	// filepath.Rel uses the host's volume/share rules on Windows and POSIX rules
	// on macOS. Never strip a drive letter or rebase a cross-volume target.
	relative, err := filepath.Rel(root.Host, candidate.Host)
	if err != nil {
		return "", fmt.Errorf("cannot express target relative to Workspace: %w", err)
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("target is outside the current Workspace")
	}
	return filepath.ToSlash(relative), nil
}

func resolveDesktopActionAliasTarget(session QuerySession, raw string) (string, pathutil.Canonical, error) {
	var empty pathutil.Canonical
	value := strings.TrimSpace(raw)
	if len(value) > 2048 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", empty, fmt.Errorf("alias path exceeds 2048 bytes or contains control characters")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	alias, suffix, _ := strings.Cut(value, "/")
	alias = strings.ToLower(alias)
	if alias != "@chat" && alias != "@workspace" && alias != "@runtime" {
		return "", empty, fmt.Errorf("only @chat, @workspace and @runtime are supported for this path")
	}
	if strings.HasPrefix(suffix, "/") || strings.Contains(suffix, ":") {
		return "", empty, fmt.Errorf("alias suffix must be a relative path without a drive or URI")
	}
	for _, segment := range strings.Split(suffix, "/") {
		if segment == ".." {
			return "", empty, fmt.Errorf("parent traversal is not allowed in this path")
		}
	}
	aliasRoot, err := accesspolicy.ResolveSessionPath(session, alias)
	if err != nil {
		return "", empty, err
	}
	target, err := accesspolicy.ResolveSessionPath(session, alias+"/"+suffix)
	if err != nil {
		return "", empty, err
	}
	base, err := pathutil.Canonicalize(aliasRoot)
	if err != nil {
		return "", empty, err
	}
	candidate, err := pathutil.Canonicalize(target)
	if err != nil {
		return "", empty, err
	}
	if !pathutil.WithinRoot(candidate, base) {
		return "", empty, fmt.Errorf("resolved path %q escapes %s", candidate.Host, alias)
	}
	if alias == "@runtime" && !withinDesktopActionRoots(session, candidate) {
		// @runtime is only another spelling: it reaches what @chat and
		// @workspace already reach, never the rest of the runtime root.
		return "", empty, fmt.Errorf("@runtime path must be inside the current Chat or Workspace")
	}
	return alias, candidate, nil
}

func withinDesktopActionRoots(session QuerySession, candidate pathutil.Canonical) bool {
	for _, raw := range []string{accesspolicy.SessionChatDir(session), accesspolicy.SessionWorkspaceRoot(session)} {
		if raw == "" {
			continue
		}
		if root, err := pathutil.Canonicalize(raw); err == nil && pathutil.WithinRoot(candidate, root) {
			return true
		}
	}
	return false
}
