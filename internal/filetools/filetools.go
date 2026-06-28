package filetools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
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
	pathKey            string
	rootKey            string
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
	pathCanonical, err := pathutil.Canonicalize(plan.Path)
	if err != nil {
		return AccessPlan{}, err
	}
	rootCanonical, err := pathutil.Canonicalize(plan.Root)
	if err != nil {
		return AccessPlan{}, err
	}
	return AccessPlan{
		RawPath:            plan.RawPath,
		Path:               plan.Path,
		Root:               plan.Root,
		pathKey:            pathCanonical.Key,
		rootKey:            rootCanonical.Key,
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

func PathInSessionWorkspace(session QuerySession, path string) bool {
	return accesspolicy.PathInSessionWorkspace(session, path)
}

func PathInSessionHostWriteRoot(session QuerySession, path string) bool {
	return accesspolicy.PathInSessionHostAccessRoot(session, accesspolicy.WriteAccess, path)
}

func SessionWorkspaceRoot(session QuerySession) string {
	return accesspolicy.SessionWorkspaceRoot(session)
}

func BuildWritePlanWithAccess(access AccessPlan, cfg config.FileToolsConfig, args map[string]any) (WritePlan, error) {
	content := AnyStringNode(args["content"])
	description := strings.TrimSpace(AnyStringNode(args["description"]))
	if len([]byte(content)) > maxPositive(cfg.MaxWriteBytes, 1<<20) {
		return WritePlan{}, fmt.Errorf("content exceeds max write bytes")
	}
	contentBytes := []byte(content)
	pathKey, rootKey, err := canonicalAccessKeys(access)
	if err != nil {
		return WritePlan{}, err
	}
	sum := sha256.Sum256([]byte(pathKey + "\x00" + hex.EncodeToString(sha256Bytes(contentBytes))))
	fingerprint := hex.EncodeToString(sum[:])
	rootHash := sha256.Sum256([]byte(rootKey))
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
	if len([]byte(newString)) > maxPositive(cfg.MaxWriteBytes, 1<<20) {
		return WritePlan{}, fmt.Errorf("new_string exceeds max write bytes")
	}
	replaceAll := AnyBoolNode(args["replace_all"])
	pathKey, rootKey, err := canonicalAccessKeys(access)
	if err != nil {
		return WritePlan{}, err
	}
	fingerprintInput := strings.Join([]string{
		pathKey,
		oldString,
		newString,
		fmt.Sprintf("%t", replaceAll),
	}, "\x00")
	sum := sha256.Sum256([]byte(fingerprintInput))
	rootHash := sha256.Sum256([]byte("file_edit\x00" + rootKey))
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

func canonicalAccessKeys(access AccessPlan) (string, string, error) {
	pathKey := strings.TrimSpace(access.pathKey)
	if pathKey == "" {
		pathCanonical, err := pathutil.Canonicalize(access.Path)
		if err != nil {
			return "", "", err
		}
		pathKey = pathCanonical.Key
	}
	rootKey := strings.TrimSpace(access.rootKey)
	if rootKey == "" {
		rootCanonical, err := pathutil.Canonicalize(access.Root)
		if err != nil {
			return "", "", err
		}
		rootKey = rootCanonical.Key
	}
	return pathKey, rootKey, nil
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
