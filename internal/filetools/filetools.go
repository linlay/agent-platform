package filetools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

type AccessMode string

const (
	ReadAccess  AccessMode = "read"
	WriteAccess AccessMode = "write"
)

type ResolvedPath struct {
	Raw  string
	Path string
	Root string
}

type AccessPlan struct {
	RawPath            string
	Path               string
	Root               string
	RuleKey            string
	Fingerprint        string
	CommandText        string
	AllowedByWhitelist bool
	AutoApproved       bool
	Blocked            bool
	Reason             string
	AccessLevel        string
	Mode               AccessMode
}

type WritePlan struct {
	FilePath    string
	Root        string
	Content     []byte
	Description string
	Fingerprint string
	RuleKey     string
	CommandText string
	ToolName    string
	Operation   string
	OldString   string
	NewString   string
	ReplaceAll  bool
}

func BuildAccessPlan(cfg config.FileToolsConfig, mode AccessMode, rawPath string) (AccessPlan, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return AccessPlan{}, fmt.Errorf("file_path is required")
	}
	candidate := expandHome(rawPath)
	if !filepath.IsAbs(candidate) {
		workingDir := cfg.WorkingDirectory
		if strings.TrimSpace(workingDir) == "" {
			workingDir = "."
		}
		candidate = filepath.Join(expandHome(workingDir), candidate)
	}
	candidate = filepath.Clean(candidate)
	realCandidate, err := evaluatePath(candidate)
	if err != nil {
		return AccessPlan{}, err
	}
	roots := cfg.AllowedReadPaths
	if mode == WriteAccess {
		roots = cfg.AllowedWritePaths
	}
	root, ok := firstAllowedRoot(cfg.WorkingDirectory, roots, realCandidate)
	if !ok {
		root = nearestExistingAncestor(realCandidate)
	}
	if root == "" {
		root = filepath.Dir(realCandidate)
	}
	fingerprintHash := sha256.Sum256([]byte(string(mode) + "\x00" + realCandidate))
	rootHash := sha256.Sum256([]byte(string(mode) + "\x00" + root))
	return AccessPlan{
		RawPath:            rawPath,
		Path:               realCandidate,
		Root:               root,
		RuleKey:            "file-" + string(mode) + "::" + hex.EncodeToString(rootHash[:8]),
		Fingerprint:        hex.EncodeToString(fingerprintHash[:]),
		CommandText:        accessModeCommandName(mode) + " " + realCandidate,
		AllowedByWhitelist: ok,
		Mode:               mode,
	}, nil
}

func BuildAccessPlanFromPolicy(cfg config.AccessPolicyConfig, session QuerySession, mode AccessMode, rawPath string) (AccessPlan, error) {
	policyMode := accesspolicy.ReadAccess
	if mode == WriteAccess {
		policyMode = accesspolicy.WriteAccess
	}
	plan, err := accesspolicy.BuildPathPlan(cfg, session, policyMode, rawPath)
	if err != nil {
		if strings.Contains(err.Error(), "path is required") {
			return AccessPlan{}, fmt.Errorf("file_path is required")
		}
		return AccessPlan{}, err
	}
	return AccessPlan{
		RawPath:            plan.RawPath,
		Path:               plan.Path,
		Root:               plan.Root,
		RuleKey:            "file-" + string(mode) + strings.TrimPrefix(plan.RuleKey, "access-"+string(policyMode)),
		Fingerprint:        plan.Fingerprint,
		CommandText:        accessModeCommandName(mode) + " " + plan.Path,
		AllowedByWhitelist: plan.Decision == accesspolicy.DecisionAllow,
		AutoApproved:       plan.Decision == accesspolicy.DecisionAutoApproved,
		Blocked:            plan.Decision == accesspolicy.DecisionBlock,
		Reason:             plan.Reason,
		AccessLevel:        plan.AccessLevel,
		Mode:               mode,
	}, nil
}

func ConfigWithSessionReadRoots(cfg config.FileToolsConfig, mode AccessMode, session QuerySession) config.FileToolsConfig {
	if mode != ReadAccess {
		return cfg
	}
	local := session.RuntimeContext.LocalPaths
	workspaceRoot := sessionWorkspaceRoot(session)
	if workspaceRoot != "" {
		cfg.WorkingDirectory = workspaceRoot
	}
	roots := append([]string(nil), cfg.AllowedReadPaths...)
	if workspaceRoot != "" {
		roots = []string{workspaceRoot}
	}
	for _, root := range []string{
		local.AgentDir,
		local.SkillsDir,
		local.SkillsMarketDir,
	} {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		roots = append(roots, filepath.Clean(expandHome(root)))
	}
	cfg.AllowedReadPaths = uniqueNonEmptyStrings(roots)
	return cfg
}

func ConfigWithSessionWriteRoots(cfg config.FileToolsConfig, session QuerySession) config.FileToolsConfig {
	workspaceRoot := sessionWorkspaceRoot(session)
	if workspaceRoot == "" {
		return cfg
	}
	cfg.WorkingDirectory = workspaceRoot
	cfg.AllowedWritePaths = []string{workspaceRoot}
	return cfg
}

func PathInSessionWorkspace(session QuerySession, path string) bool {
	workspaceRoot := SessionWorkspaceRoot(session)
	if workspaceRoot == "" || strings.TrimSpace(path) == "" {
		return false
	}
	workspaceRoot, ok := normalizeExistingOrFuturePath(workspaceRoot)
	if !ok {
		return false
	}
	candidate := expandHome(path)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspaceRoot, candidate)
	}
	candidate, ok = normalizeExistingOrFuturePath(candidate)
	if !ok {
		return false
	}
	return candidate == workspaceRoot || strings.HasPrefix(candidate, workspaceRoot+string(os.PathSeparator))
}

func sessionWorkspaceRoot(session QuerySession) string {
	return SessionWorkspaceRoot(session)
}

func SessionWorkspaceRoot(session QuerySession) string {
	return accesspolicy.SessionWorkspaceRoot(session)
}

func normalizeExistingOrFuturePath(path string) (string, bool) {
	path = filepath.Clean(expandHome(strings.TrimSpace(path)))
	if path == "" {
		return "", false
	}
	if evaluated, err := evaluatePath(path); err == nil {
		return evaluated, true
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs), true
	}
	return "", false
}

func BuildWritePlan(cfg config.FileToolsConfig, args map[string]any) (WritePlan, error) {
	access, err := BuildAccessPlan(cfg, WriteAccess, AnyStringNode(args["file_path"]))
	if err != nil {
		return WritePlan{}, err
	}
	content := AnyStringNode(args["content"])
	description := strings.TrimSpace(AnyStringNode(args["description"]))
	if description == "" {
		return WritePlan{}, fmt.Errorf("description is required for write")
	}
	if len([]byte(content)) > maxPositive(cfg.MaxWriteBytes, 1<<20) {
		return WritePlan{}, fmt.Errorf("content exceeds max write bytes")
	}
	contentBytes := []byte(content)
	sum := sha256.Sum256([]byte(access.Path + "\x00" + hex.EncodeToString(sha256Bytes(contentBytes))))
	fingerprint := hex.EncodeToString(sum[:])
	rootHash := sha256.Sum256([]byte(access.Root))
	ruleKey := "file-write::" + hex.EncodeToString(rootHash[:8])
	return WritePlan{
		FilePath:    access.Path,
		Root:        access.Root,
		Content:     contentBytes,
		Description: description,
		Fingerprint: fingerprint,
		RuleKey:     ruleKey,
		CommandText: fmt.Sprintf("file_write %s (%d bytes)", access.Path, len(contentBytes)),
		ToolName:    "file_write",
		Operation:   "write",
	}, nil
}

func BuildWritePlanWithAccess(access AccessPlan, cfg config.FileToolsConfig, args map[string]any) (WritePlan, error) {
	content := AnyStringNode(args["content"])
	description := strings.TrimSpace(AnyStringNode(args["description"]))
	if description == "" {
		return WritePlan{}, fmt.Errorf("description is required for write")
	}
	if len([]byte(content)) > maxPositive(cfg.MaxWriteBytes, 1<<20) {
		return WritePlan{}, fmt.Errorf("content exceeds max write bytes")
	}
	contentBytes := []byte(content)
	sum := sha256.Sum256([]byte(access.Path + "\x00" + hex.EncodeToString(sha256Bytes(contentBytes))))
	fingerprint := hex.EncodeToString(sum[:])
	rootHash := sha256.Sum256([]byte(access.Root))
	ruleKey := "file-write::" + hex.EncodeToString(rootHash[:8])
	return WritePlan{
		FilePath:    access.Path,
		Root:        access.Root,
		Content:     contentBytes,
		Description: description,
		Fingerprint: fingerprint,
		RuleKey:     ruleKey,
		CommandText: fmt.Sprintf("file_write %s (%d bytes)", access.Path, len(contentBytes)),
		ToolName:    "file_write",
		Operation:   "write",
	}, nil
}

func BuildEditPlan(cfg config.FileToolsConfig, args map[string]any) (WritePlan, error) {
	access, err := BuildAccessPlan(cfg, WriteAccess, AnyStringNode(args["file_path"]))
	if err != nil {
		return WritePlan{}, err
	}
	oldString, ok := args["old_string"].(string)
	if !ok {
		return WritePlan{}, fmt.Errorf("old_string is required for edit")
	}
	newString, ok := args["new_string"].(string)
	if !ok {
		return WritePlan{}, fmt.Errorf("new_string is required for edit")
	}
	if oldString == newString {
		return WritePlan{}, fmt.Errorf("old_string and new_string must be different")
	}
	description := strings.TrimSpace(AnyStringNode(args["description"]))
	if description == "" {
		return WritePlan{}, fmt.Errorf("description is required for edit")
	}
	if len([]byte(newString)) > maxPositive(cfg.MaxWriteBytes, 1<<20) {
		return WritePlan{}, fmt.Errorf("new_string exceeds max write bytes")
	}
	replaceAll := AnyBoolNode(args["replace_all"])
	fingerprintInput := strings.Join([]string{
		access.Path,
		oldString,
		newString,
		fmt.Sprintf("%t", replaceAll),
	}, "\x00")
	sum := sha256.Sum256([]byte(fingerprintInput))
	rootHash := sha256.Sum256([]byte("file_edit\x00" + access.Root))
	commandText := fmt.Sprintf("file_edit %s (%d -> %d bytes)", access.Path, len([]byte(oldString)), len([]byte(newString)))
	if replaceAll {
		commandText += " replace_all"
	}
	return WritePlan{
		FilePath:    access.Path,
		Root:        access.Root,
		Description: description,
		Fingerprint: hex.EncodeToString(sum[:]),
		RuleKey:     "file-edit::" + hex.EncodeToString(rootHash[:8]),
		CommandText: commandText,
		ToolName:    "file_edit",
		Operation:   "edit",
		OldString:   oldString,
		NewString:   newString,
		ReplaceAll:  replaceAll,
	}, nil
}

func BuildEditPlanWithAccess(access AccessPlan, cfg config.FileToolsConfig, args map[string]any) (WritePlan, error) {
	oldString, ok := args["old_string"].(string)
	if !ok {
		return WritePlan{}, fmt.Errorf("old_string is required for edit")
	}
	newString, ok := args["new_string"].(string)
	if !ok {
		return WritePlan{}, fmt.Errorf("new_string is required for edit")
	}
	if oldString == newString {
		return WritePlan{}, fmt.Errorf("old_string and new_string must be different")
	}
	description := strings.TrimSpace(AnyStringNode(args["description"]))
	if description == "" {
		return WritePlan{}, fmt.Errorf("description is required for edit")
	}
	if len([]byte(newString)) > maxPositive(cfg.MaxWriteBytes, 1<<20) {
		return WritePlan{}, fmt.Errorf("new_string exceeds max write bytes")
	}
	replaceAll := AnyBoolNode(args["replace_all"])
	fingerprintInput := strings.Join([]string{
		access.Path,
		oldString,
		newString,
		fmt.Sprintf("%t", replaceAll),
	}, "\x00")
	sum := sha256.Sum256([]byte(fingerprintInput))
	rootHash := sha256.Sum256([]byte("file_edit\x00" + access.Root))
	commandText := fmt.Sprintf("file_edit %s (%d -> %d bytes)", access.Path, len([]byte(oldString)), len([]byte(newString)))
	if replaceAll {
		commandText += " replace_all"
	}
	return WritePlan{
		FilePath:    access.Path,
		Root:        access.Root,
		Description: description,
		Fingerprint: hex.EncodeToString(sum[:]),
		RuleKey:     "file-edit::" + hex.EncodeToString(rootHash[:8]),
		CommandText: commandText,
		ToolName:    "file_edit",
		Operation:   "edit",
		OldString:   oldString,
		NewString:   newString,
		ReplaceAll:  replaceAll,
	}, nil
}

func accessModeCommandName(mode AccessMode) string {
	if mode == WriteAccess {
		return "file_write"
	}
	return "file_read"
}

func ConsumeReadApproval(execCtx *ExecutionContext, plan AccessPlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileReadRuleApprovals != nil && execCtx.FileReadRuleApprovals[plan.RuleKey] {
		return true
	}
	return consumeApproval(execCtx.FileReadApprovals, plan.Fingerprint)
}

func RegisterExactReadApproval(execCtx *ExecutionContext, fingerprint string) {
	if execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if execCtx.FileReadApprovals == nil {
		execCtx.FileReadApprovals = map[string]int{}
	}
	execCtx.FileReadApprovals[fingerprint]++
}

func RegisterRuleReadApproval(execCtx *ExecutionContext, ruleKey string) {
	if execCtx == nil || strings.TrimSpace(ruleKey) == "" {
		return
	}
	if execCtx.FileReadRuleApprovals == nil {
		execCtx.FileReadRuleApprovals = map[string]bool{}
	}
	execCtx.FileReadRuleApprovals[ruleKey] = true
}

func HasReadApproval(execCtx *ExecutionContext, plan AccessPlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileReadRuleApprovals != nil && execCtx.FileReadRuleApprovals[plan.RuleKey] {
		return true
	}
	return execCtx.FileReadApprovals != nil && execCtx.FileReadApprovals[plan.Fingerprint] > 0
}

func ConsumeAccessApproval(execCtx *ExecutionContext, plan AccessPlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileAccessRuleApprovals != nil && execCtx.FileAccessRuleApprovals[plan.RuleKey] {
		return true
	}
	return consumeApproval(execCtx.FileAccessApprovals, plan.Fingerprint)
}

func RegisterExactAccessApproval(execCtx *ExecutionContext, fingerprint string) {
	if execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if execCtx.FileAccessApprovals == nil {
		execCtx.FileAccessApprovals = map[string]int{}
	}
	execCtx.FileAccessApprovals[fingerprint]++
}

func RegisterRuleAccessApproval(execCtx *ExecutionContext, ruleKey string) {
	if execCtx == nil || strings.TrimSpace(ruleKey) == "" {
		return
	}
	if execCtx.FileAccessRuleApprovals == nil {
		execCtx.FileAccessRuleApprovals = map[string]bool{}
	}
	execCtx.FileAccessRuleApprovals[ruleKey] = true
}

func HasAccessApproval(execCtx *ExecutionContext, plan AccessPlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileAccessRuleApprovals != nil && execCtx.FileAccessRuleApprovals[plan.RuleKey] {
		return true
	}
	return execCtx.FileAccessApprovals != nil && execCtx.FileAccessApprovals[plan.Fingerprint] > 0
}

func ConsumeWriteApproval(execCtx *ExecutionContext, plan WritePlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileWriteRuleApprovals != nil && execCtx.FileWriteRuleApprovals[plan.RuleKey] {
		return true
	}
	return consumeApproval(execCtx.FileWriteApprovals, plan.Fingerprint)
}

func consumeApproval(approvals map[string]int, fingerprint string) bool {
	if strings.TrimSpace(fingerprint) == "" || len(approvals) == 0 {
		return false
	}
	remaining := approvals[fingerprint]
	if remaining <= 0 {
		return false
	}
	if remaining == 1 {
		delete(approvals, fingerprint)
		return true
	}
	approvals[fingerprint] = remaining - 1
	return true
}

func uniqueNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func RegisterExactWriteApproval(execCtx *ExecutionContext, fingerprint string) {
	if execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if execCtx.FileWriteApprovals == nil {
		execCtx.FileWriteApprovals = map[string]int{}
	}
	execCtx.FileWriteApprovals[fingerprint]++
}

func RegisterRuleWriteApproval(execCtx *ExecutionContext, ruleKey string) {
	if execCtx == nil || strings.TrimSpace(ruleKey) == "" {
		return
	}
	if execCtx.FileWriteRuleApprovals == nil {
		execCtx.FileWriteRuleApprovals = map[string]bool{}
	}
	execCtx.FileWriteRuleApprovals[ruleKey] = true
}

func HasWriteApproval(execCtx *ExecutionContext, plan WritePlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileWriteRuleApprovals != nil && execCtx.FileWriteRuleApprovals[plan.RuleKey] {
		return true
	}
	return execCtx.FileWriteApprovals != nil && execCtx.FileWriteApprovals[plan.Fingerprint] > 0
}

func evaluatePath(path string) (string, error) {
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(evaluated), nil
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
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return filepath.Clean(filepath.Join(append([]string{evaluatedParent}, missing...)...)), nil
}

func nearestExistingAncestor(path string) string {
	current := filepath.Clean(path)
	if current == "" {
		return ""
	}
	if info, err := os.Lstat(current); err == nil && info.IsDir() {
		if evaluated, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Clean(evaluated)
		}
		return current
	}
	for {
		parent := filepath.Dir(current)
		if parent == current || parent == "." || parent == "" {
			if evaluated, err := filepath.EvalSymlinks(current); err == nil {
				return filepath.Clean(evaluated)
			}
			return current
		}
		if info, err := os.Lstat(parent); err == nil && info.IsDir() {
			if evaluated, err := filepath.EvalSymlinks(parent); err == nil {
				return filepath.Clean(evaluated)
			}
			return parent
		}
		current = parent
	}
}

func firstAllowedRoot(workingDir string, roots []string, path string) (string, bool) {
	for _, root := range roots {
		resolvedRoot := strings.TrimSpace(root)
		if resolvedRoot == "" {
			continue
		}
		resolvedRoot = expandHome(resolvedRoot)
		if !filepath.IsAbs(resolvedRoot) {
			base := workingDir
			if strings.TrimSpace(base) == "" {
				base = "."
			}
			resolvedRoot = filepath.Join(expandHome(base), resolvedRoot)
		}
		evaluatedRoot, err := evaluatePath(filepath.Clean(resolvedRoot))
		if err != nil {
			continue
		}
		if path == evaluatedRoot || strings.HasPrefix(path, evaluatedRoot+string(os.PathSeparator)) {
			return evaluatedRoot, true
		}
	}
	return "", false
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func maxPositive(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
