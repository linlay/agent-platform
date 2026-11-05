package query

import (
	"context"
	"errors"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/runstate"
	sessionbuild "agent-platform/internal/runtime/session"
)

func runOwnerMatchesChat(summary *chat.Summary, agentKey string) bool {
	if summary == nil {
		return true
	}

	return strings.TrimSpace(summary.AgentKey) == agentKey
}

func (s *Service) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	return runstate.Snapshot(s.deps.Runs, s.deps.Chats, runID)
}

// runAccessLevelAcceptor is the authorization acceptance point provided by
// the Run manager. It is required: without it a permission decision could not
// be made atomically with the level it is based on, so starts are refused.
type runAccessLevelAcceptor interface {
	AcceptAtRunAccessLevel(runID string, accept func(accessLevel string, version int64) error) (bool, error)
}

// PrepareRunStart validates a new-Chat mail request and resolves its permission
// against the parent Run's live level. It is read-only: it creates no Chat,
// registers no Run and holds no reservation or catalog lease, so it is safe to
// call before a human review of unbounded duration.
func (s *Service) PrepareRunStart(ctx context.Context, request contracts.RunStartRequest) (contracts.RunStartPlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RunStartPlan{}, runNotStarted(runToolError("mail_acceptance_cancelled", err.Error()))
	}
	fail := func(code string, message string) (contracts.RunStartPlan, error) {
		return contracts.RunStartPlan{}, runNotStarted(runToolError(code, message))
	}
	if _, valid := contracts.NormalizeRequestedAccessLevel(request.AccessLevel); !valid {
		return fail("invalid_request", "accessLevel must be default, ask_for_approval, auto_approve, or full_access")
	}
	chatID := strings.TrimSpace(request.ChatID)
	if strings.TrimSpace(request.ChatName) != "" && chatID != "" {
		return fail("invalid_request", "chatName cannot be combined with chatId")
	}
	request, err := contracts.NormalizeRunStartRequest(request)
	if err != nil {
		return contracts.RunStartPlan{}, runNotStarted(err)
	}
	for _, id := range queryinput.ResourceIDs(request.References, "chat") {
		if request.Origin.Subject == "" && id != request.Origin.ChatID && !request.ReferenceReadScope.CanReadChat(id) {
			return fail("chat_reference_forbidden", "chat_send cannot grant access to an unreferenced Chat")
		}
	}
	agentKey := request.AgentKey
	plan := contracts.RunStartPlan{RequestedAccessLevel: strings.TrimSpace(request.AccessLevel)}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return fail("agent_not_found", "agent not found")
	}
	// Reject invalid overrides before admission.
	// Query admission validates again against the definition used to start.
	if request.ModelKey != "" || request.ReasoningEffort != "" {
		if err := s.deps.Proxy.Configure(&def); err != nil {
			return fail("invalid_request", err.Error())
		}
		options := &queryinput.QueryModelOptions{Key: strings.TrimSpace(request.ModelKey), ReasoningEffort: strings.TrimSpace(request.ReasoningEffort)}
		if err := s.ValidateQueryModelOptions(options, def); err != nil {
			return fail("invalid_request", err.Error())
		}
	}
	if chatID != "" {
		summary, err := s.deps.Chats.Summary(chatID)
		if err != nil && !errors.Is(err, chat.ErrChatNotFound) {
			return contracts.RunStartPlan{}, runNotStarted(err)
		}
		if summary != nil && !runOwnerMatchesChat(summary, agentKey) {
			return fail("target_owner_mismatch", "target identity does not match chat owner")
		}
	}
	parentRunID := strings.TrimSpace(request.Origin.RunID)
	if parentRunID == "" {
		return fail("run_context_required", "chat_send requires a parent runId")
	}
	// Always read the live control state: neither model input nor the parent
	// session snapshot may decide the permission baseline.
	parent, ok := s.deps.Runs.RunStatus(parentRunID)
	if !ok || parent.CompletedAt != 0 {
		return fail("run_parent_not_active", "the calling Run is no longer active")
	}
	plan.ParentAccessLevel = sessionbuild.NormalizedAccessLevel(parent.AccessLevel)
	if err := contracts.ValidateExplicitRunAccess(request.AccessLevel, plan.ParentAccessLevel); err != nil {
		return contracts.RunStartPlan{}, runNotStarted(err)
	}
	plan.AccessLevel, plan.AccessSource, _ = contracts.ResolveInitialAccessLevel(request.AccessLevel, def.AccessConfig)
	plan.TargetRevision = def.RuntimeRevision
	plan.RequestDigest = contracts.RunStartRequestDigest(request)
	return plan, nil
}

func runNotStarted(err error) error {
	if err == nil {
		return nil
	}
	var typed *contracts.RunToolError
	if !errors.As(err, &typed) {
		return &contracts.RunToolError{Code: "internal_error", Message: err.Error(), ExecutionState: "not_started"}
	}
	if typed.ExecutionState == "" {
		typed.ExecutionState = "not_started"
	}
	return typed
}

func runToolError(code string, message string) error {
	return &contracts.RunToolError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}
