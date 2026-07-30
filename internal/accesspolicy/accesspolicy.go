package accesspolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/rootpaths"
)

type AccessMode string

const (
	ReadAccess  AccessMode = "read"
	WriteAccess AccessMode = "write"
)

type Decision string

const (
	DecisionAllow            Decision = "allow"
	DecisionRequiresApproval Decision = "requires_approval"
	DecisionAutoApproved     Decision = "auto_approved"
	DecisionBlock            Decision = "block"
)

type Level struct {
	Name          string
	ReadRoots     []string
	WriteRoots    []string
	ReadonlyRoots []string
	Approvals     config.AccessPolicyApprovalConfig
}

type PathPlan struct {
	RawPath     string
	Path        string
	Root        string
	RuleKey     string
	Fingerprint string
	CommandText string
	Mode        AccessMode
	Decision    Decision
	Reason      string
	AccessLevel string
}

func (p PathPlan) Allowed() bool {
	return p.Decision == DecisionAllow || p.Decision == DecisionAutoApproved
}

func (p PathPlan) RequiresApproval() bool {
	return p.Decision == DecisionRequiresApproval
}

func (p PathPlan) AutoApproved() bool {
	return p.Decision == DecisionAutoApproved
}

func (p PathPlan) Blocked() bool {
	return p.Decision == DecisionBlock
}

func BuildPathPlan(cfg config.AccessPolicyConfig, session QuerySession, mode AccessMode, rawPath string) (PathPlan, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return PathPlan{}, fmt.Errorf("path is required")
	}
	accessLevel := sessionAccessLevel(session)
	level := EffectiveLevel(cfg, accessLevel)
	workspaceRoot := SessionWorkspaceRoot(session)
	candidate, err := ResolveSessionPath(session, rawPath)
	if err != nil {
		return PathPlan{}, err
	}
	realCandidate, err := pathutil.Canonicalize(candidate)
	if err != nil {
		return PathPlan{}, err
	}

	roots := level.ReadRoots
	action := level.Approvals.ReadOutsideRoots
	if mode == WriteAccess {
		roots = level.WriteRoots
		action = level.Approvals.WriteOutsideRoots
	}
	roots = appendSessionHostAccessRoots(roots, session, mode)
	root, ok := firstAllowedRoot(session, workspaceRoot, roots, realCandidate)
	if mode == WriteAccess && ok {
		if readonlyRoot, readonly := firstAllowedRoot(session, workspaceRoot, level.ReadonlyRoots, realCandidate); readonly {
			return buildPathPlan(mode, rawPath, realCandidate, readonlyRoot, accessLevel, DecisionBlock, "path is under a readonly root"), nil
		}
	}
	if ok {
		return buildPathPlan(mode, rawPath, realCandidate, root, accessLevel, DecisionAllow, ""), nil
	}
	root, err = pathutil.NearestExistingAncestor(realCandidate.Host)
	if err != nil || root.Host == "" {
		root, err = pathutil.Canonicalize(filepath.Dir(realCandidate.Host))
		if err != nil {
			return PathPlan{}, err
		}
	}
	return buildPathPlan(mode, rawPath, realCandidate, root, accessLevel, decisionForAction(action), outsideRootsReason(mode)), nil
}

func EffectiveLevel(cfg config.AccessPolicyConfig, accessLevel string) Level {
	normalized, ok := NormalizeAccessLevel(accessLevel)
	if !ok {
		normalized = AccessLevelDefault
	}
	raw := resolveLevelConfig(cfg, normalized, map[string]bool{})
	return Level{
		Name:          normalized,
		ReadRoots:     raw.ReadRoots,
		WriteRoots:    raw.WriteRoots,
		ReadonlyRoots: raw.ReadonlyRoots,
		Approvals:     raw.Approvals,
	}
}

func SessionWorkspaceRoot(session QuerySession) string {
	root := strings.TrimSpace(session.WorkspaceRoot)
	if root == "" {
		root = strings.TrimSpace(session.RuntimeContext.LocalPaths.WorkspaceDir)
	}
	if root == "" {
		return ""
	}
	root = filepath.Clean(pathutil.ExpandHome(root))
	if !filepath.IsAbs(root) {
		return ""
	}
	return root
}

func PathInSessionWorkspace(session QuerySession, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	roots, err := sessionRoots(session)
	if err != nil {
		return false
	}
	zone, _, err := roots.Classify(path)
	return err == nil && zone == rootpaths.ZoneWorkspace
}

func PathInSessionChat(session QuerySession, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	roots, err := sessionRoots(session)
	if err != nil {
		return false
	}
	zone, _, err := roots.Classify(path)
	return err == nil && zone == rootpaths.ZoneCurrentChat
}

func PathInSessionHostAccessRoot(session QuerySession, mode AccessMode, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	roots := session.RuntimeHostAccess.ReadRoots
	if mode == WriteAccess {
		roots = session.RuntimeHostAccess.WriteRoots
	}
	if len(roots) == 0 {
		return false
	}
	workspaceRoot := SessionWorkspaceRoot(session)
	candidate, err := ResolveSessionPath(session, path)
	if err != nil {
		return false
	}
	candidateCanonical, err := pathutil.Canonicalize(candidate)
	if err != nil {
		return false
	}
	_, ok := firstAllowedRoot(session, workspaceRoot, roots, candidateCanonical)
	return ok
}

func SessionChatDir(session QuerySession) string {
	if root := cleanAbs(session.ChatRoot); root != "" {
		return root
	}
	return cleanAbs(session.RuntimeContext.LocalPaths.ChatDir)
}

func SessionChatsRoot(session QuerySession) string {
	return cleanAbs(session.RuntimeContext.LocalPaths.ChatsDir)
}

func ResolveSessionPath(session QuerySession, rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if alias, suffix, ok := splitRootQualifiedPath(rawPath); ok {
		root := expandRootAlias(alias, session)
		if root == "" {
			if strings.EqualFold(alias, "@workspace") {
				return "", fmt.Errorf("workspace_unavailable: workspace is required")
			}
			return "", fmt.Errorf("path root %s is unavailable", alias)
		}
		if suffix == "" {
			resolved := filepath.Clean(root)
			if strings.EqualFold(alias, "@workspace") {
				return requireSessionWorkspacePath(session, resolved)
			}
			return resolved, nil
		}
		resolved := filepath.Clean(filepath.Join(root, filepath.FromSlash(suffix)))
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path escapes %s", alias)
		}
		if strings.EqualFold(alias, "@workspace") {
			return requireSessionWorkspacePath(session, resolved)
		}
		return resolved, nil
	}
	candidate := pathutil.ExpandHome(rawPath)
	if translated, ok, err := translateExecutionPath(session, candidate); ok || err != nil {
		return translated, err
	}
	if filepath.IsAbs(candidate) {
		return filepath.Clean(candidate), nil
	}
	workspaceRoot := SessionWorkspaceRoot(session)
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace_unavailable: relative paths require a workspace")
	}
	return requireSessionWorkspacePath(session, filepath.Clean(filepath.Join(workspaceRoot, candidate)))
}

func translateExecutionPath(session QuerySession, rawPath string) (string, bool, error) {
	if !session.AgentHasRuntimeSandbox {
		return "", false, nil
	}
	for _, roots := range []struct {
		execution string
		host      string
		workspace bool
	}{
		{execution: session.RuntimeContext.SandboxPaths.ChatDir, host: SessionChatDir(session)},
		{execution: session.RuntimeContext.SandboxPaths.WorkspaceDir, host: SessionWorkspaceRoot(session), workspace: true},
	} {
		executionRoot := filepath.ToSlash(strings.TrimRight(strings.TrimSpace(roots.execution), `/\`))
		hostRoot := strings.TrimSpace(roots.host)
		candidate := filepath.ToSlash(strings.TrimSpace(rawPath))
		if executionRoot == "" || hostRoot == "" {
			continue
		}
		if candidate != executionRoot && !strings.HasPrefix(candidate, executionRoot+"/") {
			continue
		}
		suffix := strings.TrimPrefix(candidate, executionRoot)
		suffix = strings.TrimLeft(suffix, "/")
		resolved := filepath.Clean(filepath.Join(hostRoot, filepath.FromSlash(suffix)))
		rel, err := filepath.Rel(hostRoot, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", true, fmt.Errorf("path escapes sandbox root %s", executionRoot)
		}
		if roots.workspace {
			resolved, err = requireSessionWorkspacePath(session, resolved)
			return resolved, true, err
		}
		return resolved, true, nil
	}
	return "", false, nil
}

func requireSessionWorkspacePath(session QuerySession, candidate string) (string, error) {
	roots, err := sessionRoots(session)
	if err != nil {
		return "", err
	}
	resolved, err := roots.RequireWorkspacePath(candidate)
	if err != nil {
		return "", err
	}
	return resolved.Host, nil
}

func sessionRoots(session QuerySession) (rootpaths.Roots, error) {
	return rootpaths.New(
		SessionWorkspaceRoot(session),
		SessionChatsRoot(session),
		SessionChatDir(session),
	)
}

func splitRootQualifiedPath(rawPath string) (string, string, bool) {
	normalized := filepath.ToSlash(strings.TrimSpace(rawPath))
	for _, alias := range []string{"@workspace", "@chat", "@agent", "@skills", "@skills-market", "@owner"} {
		if strings.EqualFold(normalized, alias) {
			return alias, "", true
		}
		prefix := alias + "/"
		if strings.HasPrefix(strings.ToLower(normalized), prefix) {
			return alias, normalized[len(prefix):], true
		}
	}
	return "", "", false
}

func pathInSessionRoot(root string, path string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	rootCanonical, err := pathutil.Canonicalize(root)
	if err != nil {
		return false
	}
	candidate := pathutil.ExpandHome(path)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootCanonical.Host, candidate)
	}
	candidateCanonical, err := pathutil.Canonicalize(candidate)
	return err == nil && pathutil.WithinRoot(candidateCanonical, rootCanonical)
}

func NormalizePath(path string) (string, error) {
	normalized, err := pathutil.Canonicalize(path)
	if err != nil {
		return "", err
	}
	return normalized.Host, nil
}

func resolveLevelConfig(cfg config.AccessPolicyConfig, name string, seen map[string]bool) config.AccessPolicyLevelConfig {
	current := defaultLevelConfig(name)
	if cfg.Levels != nil {
		if configured, ok := cfg.Levels[name]; ok {
			current = mergeLevelConfig(current, configured)
		}
	}
	if current.Inherit != "" && !seen[name] {
		seen[name] = true
		parent := resolveLevelConfig(cfg, current.Inherit, seen)
		current = mergeLevelConfig(parent, current)
	}
	return current
}

func defaultLevelConfig(name string) config.AccessPolicyLevelConfig {
	switch name {
	case AccessLevelAutoApprove:
		return config.AccessPolicyLevelConfig{
			Inherit: AccessLevelDefault,
			Approvals: config.AccessPolicyApprovalConfig{
				ReadOutsideRoots:      "auto",
				WriteOutsideRoots:     "hitl",
				BashComplexFilesystem: "auto",
				BashOpaqueCommand:     "auto",
				BashWriteInWriteRoots: "allow",
			},
		}
	case AccessLevelFullAccess:
		return config.AccessPolicyLevelConfig{
			ReadRoots:     []string{"/"},
			WriteRoots:    []string{"/"},
			ReadonlyRoots: []string{},
			Approvals: config.AccessPolicyApprovalConfig{
				ReadOutsideRoots:      "allow",
				WriteOutsideRoots:     "allow",
				BashComplexFilesystem: "allow",
				BashOpaqueCommand:     "allow",
				BashWriteInWriteRoots: "allow",
			},
		}
	default:
		return config.AccessPolicyLevelConfig{
			ReadRoots:     []string{"@workspace", "@chat", "@agent", "@skills"},
			WriteRoots:    []string{"@workspace", "@chat"},
			ReadonlyRoots: []string{"@agent", "@skills", "@skills-market"},
			Approvals: config.AccessPolicyApprovalConfig{
				ReadOutsideRoots:      "hitl",
				WriteOutsideRoots:     "hitl",
				BashComplexFilesystem: "hitl",
				BashOpaqueCommand:     "hitl",
				BashWriteInWriteRoots: "allow",
			},
		}
	}
}

func mergeLevelConfig(parent config.AccessPolicyLevelConfig, child config.AccessPolicyLevelConfig) config.AccessPolicyLevelConfig {
	out := parent
	if strings.TrimSpace(child.Inherit) != "" {
		out.Inherit = strings.TrimSpace(child.Inherit)
	}
	if child.ReadRoots != nil {
		out.ReadRoots = append([]string(nil), child.ReadRoots...)
	}
	if child.WriteRoots != nil {
		out.WriteRoots = append([]string(nil), child.WriteRoots...)
	}
	if child.ReadonlyRoots != nil {
		out.ReadonlyRoots = append([]string(nil), child.ReadonlyRoots...)
	}
	out.Approvals = mergeApprovals(parent.Approvals, child.Approvals)
	return out
}

func mergeApprovals(parent config.AccessPolicyApprovalConfig, child config.AccessPolicyApprovalConfig) config.AccessPolicyApprovalConfig {
	out := parent
	if strings.TrimSpace(child.ReadOutsideRoots) != "" {
		out.ReadOutsideRoots = strings.TrimSpace(child.ReadOutsideRoots)
	}
	if strings.TrimSpace(child.WriteOutsideRoots) != "" {
		out.WriteOutsideRoots = strings.TrimSpace(child.WriteOutsideRoots)
	}
	if strings.TrimSpace(child.BashComplexFilesystem) != "" {
		out.BashComplexFilesystem = strings.TrimSpace(child.BashComplexFilesystem)
	}
	if strings.TrimSpace(child.BashOpaqueCommand) != "" {
		out.BashOpaqueCommand = strings.TrimSpace(child.BashOpaqueCommand)
	}
	if strings.TrimSpace(child.BashWriteInWriteRoots) != "" {
		out.BashWriteInWriteRoots = strings.TrimSpace(child.BashWriteInWriteRoots)
	}
	return out
}

func buildPathPlan(mode AccessMode, rawPath string, path pathutil.Canonical, root pathutil.Canonical, accessLevel string, decision Decision, reason string) PathPlan {
	fingerprintHash := sha256.Sum256([]byte(string(mode) + "\x00" + path.Key))
	rootHash := sha256.Sum256([]byte(string(mode) + "\x00" + root.Key))
	command := "file_read " + path.Host
	if mode == WriteAccess {
		command = "file_write " + path.Host
	}
	return PathPlan{
		RawPath:     rawPath,
		Path:        path.Host,
		Root:        root.Host,
		RuleKey:     "access-" + string(mode) + "::" + hex.EncodeToString(rootHash[:8]),
		Fingerprint: hex.EncodeToString(fingerprintHash[:]),
		CommandText: command,
		Mode:        mode,
		Decision:    decision,
		Reason:      strings.TrimSpace(reason),
		AccessLevel: accessLevel,
	}
}

func decisionForAction(action string) Decision {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "allow":
		return DecisionAllow
	case "auto":
		return DecisionAutoApproved
	case "block":
		return DecisionBlock
	default:
		return DecisionRequiresApproval
	}
}

func outsideRootsReason(mode AccessMode) string {
	if mode == WriteAccess {
		return "write path is outside allowed roots"
	}
	return "read path is outside allowed roots"
}

func firstAllowedRoot(session QuerySession, workspaceRoot string, roots []string, candidate pathutil.Canonical) (pathutil.Canonical, bool) {
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		checkRoot := root
		rootAlias := strings.ToLower(checkRoot)
		workspaceRelative := !filepath.IsAbs(checkRoot) && !strings.HasPrefix(checkRoot, "@")
		if expanded := expandRootAlias(checkRoot, session); expanded != "" {
			checkRoot = expanded
		} else if strings.HasPrefix(checkRoot, "@") {
			continue
		}
		if !filepath.IsAbs(checkRoot) {
			if workspaceRoot == "" {
				continue
			}
			checkRoot = filepath.Join(workspaceRoot, checkRoot)
		}
		checkRootCanonical, err := pathutil.Canonicalize(checkRoot)
		if err != nil {
			continue
		}
		switch rootAlias {
		case "@workspace":
			semanticRoots, err := sessionRoots(session)
			if err != nil || semanticRoots.ClassifyCanonical(candidate) != rootpaths.ZoneWorkspace {
				continue
			}
		case "@chat":
			semanticRoots, err := sessionRoots(session)
			if err != nil || semanticRoots.ClassifyCanonical(candidate) != rootpaths.ZoneCurrentChat {
				continue
			}
		}
		if workspaceRelative {
			semanticRoots, err := sessionRoots(session)
			if err != nil ||
				semanticRoots.ClassifyCanonical(checkRootCanonical) != rootpaths.ZoneWorkspace ||
				semanticRoots.ClassifyCanonical(candidate) != rootpaths.ZoneWorkspace {
				continue
			}
		}
		if pathutil.WithinRoot(candidate, checkRootCanonical) {
			return checkRootCanonical, true
		}
	}
	return pathutil.Canonical{}, false
}

func expandRootAlias(root string, session QuerySession) string {
	switch strings.ToLower(strings.TrimSpace(root)) {
	case "@workspace":
		return SessionWorkspaceRoot(session)
	case "@chat":
		return SessionChatDir(session)
	case "@agent":
		return cleanAbs(session.RuntimeContext.LocalPaths.AgentDir)
	case "@skills":
		return cleanAbs(session.RuntimeContext.LocalPaths.SkillsDir)
	case "@skills-market":
		return cleanAbs(session.RuntimeContext.LocalPaths.SkillsMarketDir)
	case "@owner":
		return cleanAbs(session.RuntimeContext.LocalPaths.OwnerDir)
	default:
		return ""
	}
}

func appendSessionHostAccessRoots(roots []string, session QuerySession, mode AccessMode) []string {
	extra := session.RuntimeHostAccess.ReadRoots
	if mode == WriteAccess {
		extra = session.RuntimeHostAccess.WriteRoots
	}
	if len(extra) == 0 {
		return roots
	}
	out := append([]string(nil), roots...)
	out = append(out, extra...)
	return out
}

func cleanAbs(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(pathutil.ExpandHome(path))
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}

func sessionAccessLevel(session QuerySession) string {
	accessLevel, ok := NormalizeAccessLevel(session.AccessLevel)
	if !ok {
		return AccessLevelDefault
	}
	return accessLevel
}
