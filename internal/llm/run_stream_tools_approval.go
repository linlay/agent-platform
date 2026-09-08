package llm

import (
	"context"
	"fmt"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/agentconfig"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func (s *llmRunStream) lookupBashSecurityReview(invocation *preparedToolInvocation) bashsec.ReviewResult {
	if invocation == nil || !isBashTool(invocation.toolName) {
		return bashsec.ReviewResult{Decision: bashsec.ReviewAllow}
	}
	if invocation.bashSecurityReview != nil {
		return *invocation.bashSecurityReview
	}
	review := s.reviewBashSecurity(strings.TrimSpace(mapStringArg(invocation.args, "command")))
	return review
}

func (s *llmRunStream) lookupBashAccessReview(invocation *preparedToolInvocation) accesspolicy.BashPlan {
	if invocation == nil || !isBashTool(invocation.toolName) {
		return accesspolicy.BashPlan{Decision: accesspolicy.DecisionAllow}
	}
	if s.engine == nil {
		return accesspolicy.BashPlan{Decision: accesspolicy.DecisionAllow}
	}
	// File versions, wrappers and runtime environment can change while queued.
	return accesspolicy.PendingBashPlan(s.execCtx, s.rawBashAccessReview(invocation))
}

func (s *llmRunStream) rawBashAccessReview(invocation *preparedToolInvocation) accesspolicy.BashPlan {
	if invocation == nil || !isBashTool(invocation.toolName) || s.engine == nil {
		return accesspolicy.BashPlan{Decision: accesspolicy.DecisionAllow}
	}
	if reviewer, ok := s.engine.tools.(interface {
		ReviewBashAccess(context.Context, map[string]any, *ExecutionContext, config.AccessPolicyConfig) accesspolicy.BashPlan
	}); ok {
		reviewCtx := s.execCtx
		if reviewCtx != nil && !hasLocalFileRoots(reviewCtx.Session) {
			cloned := *reviewCtx
			cloned.Session = s.fileAccessSession()
			reviewCtx = &cloned
		}
		return reviewer.ReviewBashAccess(s.ctx, invocation.args, reviewCtx, s.engine.cfg.AccessPolicy)
	}
	cwd := strings.TrimSpace(mapStringArg(invocation.args, "cwd"))
	var variables map[string]string
	if s.execCtx != nil {
		variables = s.knownRuntimeVariables()
	}
	cfg := config.AccessPolicyConfig{}
	if s.engine != nil {
		cfg = s.engine.cfg.AccessPolicy
	}
	return accesspolicy.ReviewBashCommand(cfg, s.fileAccessSession(), strings.TrimSpace(mapStringArg(invocation.args, "command")), cwd, variables, s.execCtx)
}

func (s *llmRunStream) lookupFileWritePlan(invocation *preparedToolInvocation) *filetools.WritePlan {
	if invocation == nil || !isWriteTool(invocation.toolName) {
		return nil
	}
	if invocation.fileWritePlan != nil {
		return invocation.fileWritePlan
	}
	plan, err := s.buildFileWritePlan(invocation)
	if err != nil {
		return nil
	}
	invocation.fileWritePlan = &plan
	return &plan
}

func (s *llmRunStream) buildFileWritePlan(invocation *preparedToolInvocation) (filetools.WritePlan, error) {
	access, ok := s.buildFileAccessPlan(invocation)
	if !ok || access == nil {
		return filetools.WritePlan{}, fmt.Errorf("file access plan is required")
	}
	if strings.EqualFold(strings.TrimSpace(invocation.toolName), "file_edit") {
		return filetools.BuildEditPlanWithAccess(*access, s.sessionFileToolsConfig(filetools.WriteAccess), invocation.args)
	}
	return filetools.BuildWritePlanWithAccess(*access, s.sessionFileToolsConfig(filetools.WriteAccess), invocation.args)
}

func (s *llmRunStream) buildFileAccessPlan(invocation *preparedToolInvocation) (*filetools.AccessPlan, bool) {
	if invocation == nil {
		return nil, false
	}
	if strings.EqualFold(strings.TrimSpace(invocation.toolName), "desktop_cdp") && s.engine.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return nil, false
	}
	mode, rawPath, ok := fileAccessPlanInput(invocation.toolName, invocation.args)
	if !ok {
		return nil, false
	}
	session := s.fileAccessSession()
	plan, err := filetools.BuildAccessPlanFromPolicy(s.engine.cfg.AccessPolicy, session, mode, rawPath)
	if err != nil {
		return nil, false
	}
	if strings.EqualFold(strings.TrimSpace(invocation.toolName), "file_edit") && plan.Mode == filetools.WriteAccess {
		plan.CommandText = "file_edit " + plan.Path
	}
	return &plan, true
}

func (s *llmRunStream) fileAccessSession() QuerySession {
	if s == nil {
		return QuerySession{}
	}
	if hasLocalFileRoots(s.session) {
		return s.session
	}
	if s.execCtx != nil {
		return s.execCtx.Session
	}
	return s.session
}

func hasLocalFileRoots(session QuerySession) bool {
	paths := session.RuntimeContext.LocalPaths
	return strings.TrimSpace(session.WorkspaceRoot) != "" ||
		session.ScopedFilePolicy != nil ||
		strings.TrimSpace(paths.WorkspaceDir) != "" ||
		strings.TrimSpace(paths.ChatDir) != "" ||
		strings.TrimSpace(paths.AgentDir) != "" ||
		strings.TrimSpace(paths.SkillsDir) != "" ||
		strings.TrimSpace(paths.SkillsCenterDir) != ""
}

func (s *llmRunStream) lookupFileAccessPlan(invocation *preparedToolInvocation) *filetools.AccessPlan {
	if invocation == nil {
		return nil
	}
	if invocation.fileAccessPlan != nil {
		return invocation.fileAccessPlan
	}
	plan, ok := s.buildFileAccessPlan(invocation)
	if !ok {
		return nil
	}
	invocation.fileAccessPlan = plan
	return plan
}

func (s *llmRunStream) combinedFileWriteApprovalPlans(invocation *preparedToolInvocation) (*filetools.AccessPlan, *filetools.WritePlan, bool) {
	accessPlan := s.lookupFileAccessPlan(invocation)
	if accessPlan == nil || accessPlan.Mode != filetools.WriteAccess || !s.fileAccessPlanNeedsApproval(*accessPlan) {
		return nil, nil, false
	}
	writePlan := s.lookupFileWritePlan(invocation)
	if writePlan == nil || !s.fileWritePlanNeedsApproval(*writePlan) || filetools.HasWriteApproval(s.execCtx, *writePlan) {
		return nil, nil, false
	}
	return accessPlan, writePlan, true
}

func (s *llmRunStream) sessionFileToolsConfig(_ filetools.AccessMode) config.FileToolsConfig {
	return s.engine.cfg.FileTools
}

func (s *llmRunStream) fileWritePlanNeedsApproval(plan filetools.WritePlan) bool {
	if !s.engine.cfg.FileTools.RequireWriteApproval {
		return false
	}
	session := s.fileAccessSession()
	accessLevel, _ := NormalizeAccessLevel(session.AccessLevel)
	if accessLevel == AccessLevelAutoApprove || accessLevel == AccessLevelFullAccess {
		return false
	}
	return !filetools.PathInSessionWorkspace(session, plan.FilePath) &&
		!filetools.PathInSessionChat(session, plan.FilePath) &&
		!filetools.PathInSessionTemp(session, plan.FilePath) &&
		!filetools.PathInSessionHostWriteRoot(session, plan.FilePath)
}

func (s *llmRunStream) fileAccessPlanNeedsApproval(plan filetools.AccessPlan) bool {
	if plan.Blocked || plan.AutoApproved {
		return false
	}
	if plan.Mode == filetools.WriteAccess {
		if err := filetools.ValidateScopedWrite(s.fileAccessSession(), plan.Path); err != nil {
			// The tool executor remains the authoritative error producer. Avoid
			// asking for an approval that cannot widen the KBASE source mutation
			// capability of this run.
			return false
		}
	}
	if plan.AllowedByWhitelist {
		return false
	}
	if plan.Mode == filetools.ReadAccess {
		return !filetools.HasReadApproval(s.execCtx, plan)
	}
	return !filetools.HasAccessApproval(s.execCtx, plan)
}

func fileAccessPlanInput(toolName string, args map[string]any) (filetools.AccessMode, string, bool) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "file_read":
		return filetools.ReadAccess, mapStringArg(args, "file_path"), strings.TrimSpace(mapStringArg(args, "file_path")) != ""
	case "desktop_cdp":
		path, ok := args["paramsFile"].(string)
		_, hasParams := args["params"]
		return filetools.ReadAccess, path, ok && strings.TrimSpace(path) != "" && !hasParams && strings.TrimSpace(mapStringArg(args, "method")) != ""
	case "file_glob", "file_grep":
		rawPath := strings.TrimSpace(mapStringArg(args, "path"))
		if rawPath == "" {
			rawPath = "."
		}
		return filetools.ReadAccess, rawPath, true
	case "file_write":
		return filetools.WriteAccess, mapStringArg(args, "file_path"), strings.TrimSpace(mapStringArg(args, "file_path")) != ""
	case "file_edit":
		return filetools.WriteAccess, mapStringArg(args, "file_path"), strings.TrimSpace(mapStringArg(args, "file_path")) != ""
	default:
		return "", "", false
	}
}

func (s *llmRunStream) reviewBashSecurity(command string) bashsec.ReviewResult {
	if s == nil || s.execCtx == nil {
		return bashsec.ReviewBashSecurity(command)
	}
	return bashsec.ReviewBashSecurityWithKnownVariables(command, s.knownRuntimeVariables())
}

func (s *llmRunStream) knownRuntimeVariables() map[string]string {
	if s == nil || s.execCtx == nil {
		return nil
	}
	var dynamic map[string]string
	if s.execCtx.RunEnvironment != nil {
		if snapshot, _, err := s.execCtx.RunEnvironment.Snapshot(); err == nil {
			dynamic = snapshot
		}
	}
	return agentconfig.Merge(s.execCtx.StaticRuntimeEnv, dynamic)
}

func (s *llmRunStream) executeApprovedFileAccessInvocation(invocation *preparedToolInvocation, plan filetools.AccessPlan) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		if plan.Mode == filetools.ReadAccess {
			s.appendOriginalToolResult(invocation, fileAccessDeniedToolResult(invocation, "file_read_denied"))
			return nil
		}
		s.appendOriginalToolResult(invocation, fileAccessDeniedToolResult(invocation, fileMutationDeniedCode(invocation)))
		return nil
	case "approve_rule_run":
		if accessPlan, writePlan, ok := s.combinedFileWriteApprovalPlans(invocation); ok {
			filetools.RegisterRuleAccessApproval(s.execCtx, accessPlan.RuleKey)
			filetools.RegisterRuleWriteApproval(s.execCtx, writePlan.RuleKey)
			invocation.approvalDecision = ""
			return s.executeOriginalBash(invocation)
		}
		if plan.Mode == filetools.ReadAccess {
			filetools.RegisterRuleReadApproval(s.execCtx, plan.RuleKey)
		} else {
			filetools.RegisterRuleAccessApproval(s.execCtx, plan.RuleKey)
		}
		invocation.approvalDecision = ""
		return s.executeAfterFileAccessApproval(invocation)
	case "approve":
		if accessPlan, writePlan, ok := s.combinedFileWriteApprovalPlans(invocation); ok {
			filetools.RegisterExactAccessApproval(s.execCtx, accessPlan.Fingerprint)
			filetools.RegisterExactWriteApproval(s.execCtx, writePlan.Fingerprint)
			invocation.approvalDecision = ""
			return s.executeOriginalBash(invocation)
		}
		if plan.Mode == filetools.ReadAccess {
			filetools.RegisterExactReadApproval(s.execCtx, plan.Fingerprint)
		} else {
			filetools.RegisterExactAccessApproval(s.execCtx, plan.Fingerprint)
		}
		invocation.approvalDecision = ""
		return s.executeAfterFileAccessApproval(invocation)
	default:
		return s.emitApprovalRequestDeltas(s.fileAccessApprovalRequest(invocation, plan))
	}
}

func (s *llmRunStream) executeAfterFileAccessApproval(invocation *preparedToolInvocation) error {
	if plan := s.lookupFileWritePlan(invocation); plan != nil && s.fileWritePlanNeedsApproval(*plan) && !filetools.HasWriteApproval(s.execCtx, *plan) {
		return s.emitApprovalRequestDeltas(s.fileWriteApprovalRequest(invocation, *plan))
	}
	return s.executeOriginalBash(invocation)
}

func (s *llmRunStream) executeApprovedFileWriteInvocation(invocation *preparedToolInvocation, plan filetools.WritePlan) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve_rule_run":
		filetools.RegisterRuleWriteApproval(s.execCtx, plan.RuleKey)
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	case "approve":
		filetools.RegisterExactWriteApproval(s.execCtx, plan.Fingerprint)
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	default:
		return s.emitApprovalRequestDeltas(s.fileWriteApprovalRequest(invocation, plan))
	}
}

func fileAccessDeniedToolResult(invocation *preparedToolInvocation, code string) ToolExecutionResult {
	message := "file access rejected"
	if invocation != nil && strings.EqualFold(strings.TrimSpace(invocation.toolName), "file_write") {
		message = "file write access rejected"
	} else if invocation != nil && strings.EqualFold(strings.TrimSpace(invocation.toolName), "file_edit") {
		message = "file edit access rejected"
	}
	result := structuredResult(map[string]any{
		"error":   code,
		"message": message,
	})
	result.Error = code
	result.ExitCode = -1
	return result
}

func (s *llmRunStream) executeApprovedBashSecurityInvocation(invocation *preparedToolInvocation, review bashsec.ReviewResult) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve_rule_run":
		s.registerRuleWhitelist(review.RuleKey)
		invocation.approvalDecision = ""
		s.registerBashSecurityApproval(review.Fingerprint)
		return s.executeOriginalBash(invocation)
	case "approve":
		invocation.approvalDecision = ""
		s.registerBashSecurityApproval(review.Fingerprint)
		return s.executeOriginalBash(invocation)
	case "auto_approved":
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	default:
		return s.emitApprovalRequestDeltas(s.bashSecurityApprovalRequest(invocation, review))
	}
}

func (s *llmRunStream) executeApprovedBashAccessInvocation(invocation *preparedToolInvocation, review accesspolicy.BashPlan) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve_rule_run":
		s.grantDisplayedBashAccess(invocation.approvalDecision, review)
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	case "approve":
		s.grantDisplayedBashAccess(invocation.approvalDecision, review)
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	default:
		return s.emitApprovalRequestDeltas(s.bashAccessApprovalRequest(invocation, review))
	}
}

func (s *llmRunStream) grantDisplayedBashAccess(decision string, review accesspolicy.BashPlan) {
	if !review.RequiresApproval() {
		return
	}
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "approve_rule_run":
		for _, rule := range accesspolicy.ApprovalRules(review) {
			s.registerRuleWhitelist(rule)
			accesspolicy.RegisterRuleApproval(s.execCtx, rule)
		}
	case "approve":
		accesspolicy.RegisterExactApproval(s.execCtx, review.Fingerprint)
	}
}
