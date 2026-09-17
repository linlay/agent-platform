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
	"desktop.webapp.install":          {"workspaceArchivePath"},
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
		resolved, err := resolveDesktopActionAlias(session, raw)
		if err != nil {
			return nil, &desktopActionPathError{field: field, input: raw, err: err}
		}
		out[field] = resolved
	}
	return out, nil
}

func resolveDesktopActionAlias(session QuerySession, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if len(value) > 2048 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("alias path exceeds 2048 bytes or contains control characters")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	alias, suffix, _ := strings.Cut(value, "/")
	alias = strings.ToLower(alias)
	if alias != "@chat" && alias != "@workspace" {
		return "", fmt.Errorf("only @chat and @workspace are supported for WebApp paths")
	}
	if strings.HasPrefix(suffix, "/") || strings.Contains(suffix, ":") {
		return "", fmt.Errorf("alias suffix must be a relative path without a drive or URI")
	}
	for _, segment := range strings.Split(suffix, "/") {
		if segment == ".." {
			return "", fmt.Errorf("parent traversal is not allowed in WebApp paths")
		}
	}
	workspace := accesspolicy.SessionWorkspaceRoot(session)
	if workspace == "" {
		return "", fmt.Errorf("workspace_unavailable: a trusted Session Workspace is required; @chat does not replace it")
	}
	aliasRoot, err := accesspolicy.ResolveSessionPath(session, alias)
	if err != nil {
		return "", err
	}
	target, err := accesspolicy.ResolveSessionPath(session, alias+"/"+suffix)
	if err != nil {
		return "", err
	}
	root, err := pathutil.Canonicalize(workspace)
	if err != nil {
		return "", err
	}
	base, err := pathutil.Canonicalize(aliasRoot)
	if err != nil {
		return "", err
	}
	candidate, err := pathutil.Canonicalize(target)
	if err != nil {
		return "", err
	}
	if !pathutil.WithinRoot(candidate, base) {
		return "", fmt.Errorf("resolved path %q escapes %s", candidate.Host, alias)
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
