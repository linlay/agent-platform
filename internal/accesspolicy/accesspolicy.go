package accesspolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
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
	workingDir := WorkingDirectory(cfg, session)
	candidate := expandHome(rawPath)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workingDir, candidate)
	}
	candidate = filepath.Clean(candidate)
	realCandidate, err := NormalizePath(candidate)
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
	root, ok := firstAllowedRoot(session, workingDir, roots, realCandidate)
	if mode == WriteAccess && ok {
		if readonlyRoot, readonly := firstAllowedRoot(session, workingDir, level.ReadonlyRoots, realCandidate); readonly {
			return buildPathPlan(mode, rawPath, realCandidate, readonlyRoot, accessLevel, DecisionBlock, "path is under a readonly root"), nil
		}
	}
	if ok {
		return buildPathPlan(mode, rawPath, realCandidate, root, accessLevel, DecisionAllow, ""), nil
	}
	root = nearestExistingAncestor(realCandidate)
	if root == "" {
		root = filepath.Dir(realCandidate)
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

func WorkingDirectory(cfg config.AccessPolicyConfig, session QuerySession) string {
	raw := strings.TrimSpace(cfg.WorkingDirectory)
	if raw == "" {
		raw = "@workspace"
	}
	if expanded := expandRootAlias(raw, session); expanded != "" {
		return expanded
	}
	if strings.EqualFold(raw, "@workspace") {
		if abs, err := filepath.Abs("."); err == nil {
			return filepath.Clean(abs)
		}
		return "."
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(expandHome(raw))
	}
	if workspace := SessionWorkspaceRoot(session); workspace != "" {
		return workspace
	}
	if abs, err := filepath.Abs(raw); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(raw)
}

func SessionWorkspaceRoot(session QuerySession) string {
	root := strings.TrimSpace(session.WorkspaceRoot)
	if root == "" {
		root = strings.TrimSpace(session.RuntimeContext.LocalPaths.WorkspaceDir)
	}
	if root == "" {
		root = strings.TrimSpace(session.RuntimeContext.LocalPaths.ChatAttachmentsDir)
	}
	if root == "" {
		root = strings.TrimSpace(session.RuntimeContext.LocalPaths.WorkingDirectory)
	}
	if root == "" {
		return ""
	}
	root = filepath.Clean(expandHome(root))
	if !filepath.IsAbs(root) {
		return ""
	}
	return root
}

func PathInSessionWorkspace(session QuerySession, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, root := range []string{
		SessionWorkspaceRoot(session),
		SessionChatDir(session),
	} {
		if pathInSessionRoot(root, path) {
			return true
		}
	}
	return false
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
	workingDir := SessionWorkspaceRoot(session)
	if workingDir == "" {
		workingDir = SessionChatDir(session)
	}
	if workingDir == "" {
		workingDir = "."
	}
	candidate := expandHome(path)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workingDir, candidate)
	}
	candidate, ok := normalizeExistingOrFuturePath(candidate)
	if !ok {
		return false
	}
	_, ok = firstAllowedRoot(session, workingDir, roots, candidate)
	return ok
}

func SessionChatDir(session QuerySession) string {
	return cleanAbs(session.RuntimeContext.LocalPaths.ChatAttachmentsDir)
}

func pathInSessionRoot(root string, path string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	root, ok := normalizeExistingOrFuturePath(root)
	if !ok {
		return false
	}
	candidate := expandHome(path)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, ok = normalizeExistingOrFuturePath(candidate)
	return ok && pathWithinRoot(candidate, root)
}

func NormalizePath(path string) (string, error) {
	if normalized, ok := normalizeExistingOrFuturePath(path); ok {
		return normalized, nil
	}
	return "", fmt.Errorf("resolve path: %s", path)
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

func buildPathPlan(mode AccessMode, rawPath, path, root, accessLevel string, decision Decision, reason string) PathPlan {
	fingerprintHash := sha256.Sum256([]byte(string(mode) + "\x00" + path))
	rootHash := sha256.Sum256([]byte(string(mode) + "\x00" + root))
	command := "file_read " + path
	if mode == WriteAccess {
		command = "file_write " + path
	}
	return PathPlan{
		RawPath:     rawPath,
		Path:        path,
		Root:        root,
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

func firstAllowedRoot(session QuerySession, workingDir string, roots []string, candidate string) (string, bool) {
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		checkRoot := root
		if expanded := expandRootAlias(checkRoot, session); expanded != "" {
			checkRoot = expanded
		} else if strings.EqualFold(checkRoot, "@workspace") {
			checkRoot = workingDir
		} else if strings.HasPrefix(checkRoot, "@") {
			continue
		}
		if !filepath.IsAbs(checkRoot) {
			checkRoot = filepath.Join(workingDir, checkRoot)
		}
		checkRoot, ok := normalizeExistingOrFuturePath(checkRoot)
		if !ok {
			continue
		}
		if pathWithinRoot(candidate, checkRoot) {
			return checkRoot, true
		}
	}
	return "", false
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
	path = filepath.Clean(expandHome(path))
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}

func normalizeExistingOrFuturePath(path string) (string, bool) {
	path = filepath.Clean(expandHome(strings.TrimSpace(path)))
	if path == "" {
		return "", false
	}
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(evaluated), true
	}
	existing := path
	missing := []string{}
	for {
		if existing == "." || existing == string(filepath.Separator) || existing == "" {
			break
		}
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	evaluatedParent, err := filepath.EvalSymlinks(existing)
	if err != nil {
		if abs, absErr := filepath.Abs(path); absErr == nil {
			return filepath.Clean(abs), true
		}
		return "", false
	}
	return filepath.Clean(filepath.Join(append([]string{evaluatedParent}, missing...)...)), true
}

func nearestExistingAncestor(path string) string {
	current := filepath.Clean(path)
	if current == "" {
		return ""
	}
	for {
		if info, err := os.Stat(current); err == nil {
			if info.IsDir() {
				return current
			}
			return filepath.Dir(current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}

func pathWithinRoot(path string, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if root == string(os.PathSeparator) {
		return filepath.IsAbs(path)
	}
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
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
