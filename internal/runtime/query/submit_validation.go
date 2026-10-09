package query

import (
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	sessionbuild "agent-platform/internal/runtime/session"
	"agent-platform/internal/toolinteraction"
)

// ValidateSubmitParams checks the answer shape for the awaiting mode before the
// waiter is woken: planning/form take one param object, question/approval take
// a params list. An invalid submit never resolves the awaiting.
func ValidateSubmitParams(ctx contracts.AwaitingSubmitContext, req queryinput.SubmitRequest) error {
	items, err := submitItemsForMode(ctx.Mode, req)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(ctx.Mode), "form") && ctx.View != nil && ctx.View.Source == "builtin" && ctx.View.Key == "ask_user_form" {
		data := contracts.AnyMapNode(ctx.Form["data"])
		_, err := toolinteraction.NewAskUserFormHandler().NormalizeSubmit(map[string]any{
			"title": ctx.Form["title"], "html": data["html"], "values": data["values"],
		}, req.Param)
		return err
	}
	if len(items) == 0 {
		return nil
	}
	if len(items) != ctx.ItemCount {
		return fmt.Errorf("expected %d submit items, got %d", ctx.ItemCount, len(items))
	}
	if err := validateSubmitItems(ctx.Mode, items); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(ctx.Mode), "question") && len(ctx.Questions) > 0 {
		if _, err := toolinteraction.NewAskUserQuestionHandler().NormalizeSubmit(map[string]any{
			"questions": ctx.Questions,
		}, req.Params); err != nil {
			return err
		}
	}
	return nil
}

func singleAnswerMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "planning", "form":
		return true
	}
	return false
}

// submitItemsForMode validates field exclusivity. A single answer is fully
// validated here and yields nil; a question/approval list is returned decoded
// for the caller to count and validate.
func submitItemsForMode(mode string, req queryinput.SubmitRequest) ([]map[string]any, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if singleAnswerMode(mode) {
		if req.Params != nil {
			return nil, fmt.Errorf("%s awaiting accepts param, not params", mode)
		}
		if len(req.Param) == 0 {
			return nil, fmt.Errorf("%s awaiting requires a non-empty param object", mode)
		}
		return nil, validateSingleSubmit(mode, req.Param)
	}
	if req.Param != nil {
		return nil, fmt.Errorf("%s awaiting accepts params, not param", mode)
	}
	return queryinput.DecodeSubmitParams(req.Params)
}

func validateSubmitItems(mode string, items []map[string]any) error {
	for index, item := range items {
		if err := validateSubmitItem(mode, index, item); err != nil {
			return err
		}
	}
	return nil
}

func validateSingleSubmit(mode string, param map[string]any) error {
	for _, field := range []string{"answer", "answers", "payload", "form", "action", "id"} {
		if _, exists := param[field]; exists {
			return fmt.Errorf("param: %s awaiting does not allow %s", mode, field)
		}
	}
	rawData, hasData := param["data"]
	if hasData {
		if data, ok := rawData.(map[string]any); !ok || data == nil {
			return fmt.Errorf("param.data must be an object")
		}
	}
	decision := strings.ToLower(strings.TrimSpace(sessionbuild.StringValue(param["decision"])))
	switch decision {
	case "approve", "reject", "dismiss":
	case "":
		return fmt.Errorf("param.decision is required")
	default:
		return fmt.Errorf("param: unsupported %s decision %q", mode, decision)
	}
	if mode == "planning" {
		if hasData {
			return fmt.Errorf("param: planning awaiting does not allow data")
		}
		return nil
	}
	if decision == "approve" && !hasData {
		return fmt.Errorf("param.data is required for approve")
	}
	if decision == "dismiss" && hasData {
		return fmt.Errorf("param: dismiss does not allow data")
	}
	return nil
}

func validateSubmitItem(mode string, index int, item map[string]any) error {
	itemLabel := fmt.Sprintf("items[%d]", index)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		_, hasAnswer := item["answer"]
		_, hasAnswers := item["answers"]
		if hasAnswer == hasAnswers {
			return fmt.Errorf("%s: question items require exactly one of answer or answers", itemLabel)
		}
		if _, hasDecision := item["decision"]; hasDecision {
			return fmt.Errorf("%s: question items do not allow decision", itemLabel)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: question items do not allow payload", itemLabel)
		}
	case "approval":
		decision := strings.ToLower(strings.TrimSpace(sessionbuild.StringValue(item["decision"])))
		if decision == "" {
			return fmt.Errorf("%s: approval items require decision", itemLabel)
		}
		switch decision {
		case "approve", "approve_rule_run", "reject":
		default:
			return fmt.Errorf("%s: unsupported approval decision %q", itemLabel, decision)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: approval items do not allow payload", itemLabel)
		}
		if _, hasAnswer := item["answer"]; hasAnswer {
			return fmt.Errorf("%s: approval items do not allow answer", itemLabel)
		}
		if _, hasAnswers := item["answers"]; hasAnswers {
			return fmt.Errorf("%s: approval items do not allow answers", itemLabel)
		}
	default:
		return fmt.Errorf("unsupported awaiting mode: %s", mode)
	}
	return nil
}

func ValidateDeferredSubmitParams(mode string, req queryinput.SubmitRequest) error {
	items, err := submitItemsForMode(mode, req)
	if err != nil {
		return err
	}
	return validateSubmitItems(mode, items)
}

func (s *Service) lookupActiveAwaiting(req queryinput.SubmitRequest) (contracts.AwaitingSubmitContext, bool) {
	if s == nil || s.deps.Runs == nil {
		return contracts.AwaitingSubmitContext{}, false
	}
	return s.deps.Runs.LookupAwaiting(req.RunID, req.AwaitingID)
}

func (s *Service) ValidateSubmitOwner(req queryinput.SubmitRequest) *statusError {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.AwaitingID) == "" {
		return &statusError{Status: 400, Message: "runId and awaitingId are required"}
	}
	if s.deps.Runs != nil {
		status, ok := s.deps.Runs.RunStatus(req.RunID)
		if ok {
			return validateRunStatusOwner(status, req.AgentKey, req.TeamID)
		}
	}
	if s.deferredAwaitings != nil {
		deferred, ok := s.deferredAwaitings.Lookup(req.AwaitingID)
		if ok && strings.TrimSpace(deferred.RunID) == strings.TrimSpace(req.RunID) {
			summary, err := s.deps.Chats.Summary(deferred.ChatID)
			if err == nil && summary != nil {
				return validateRunStatusOwner(runStatusOwnerFromChatSummary(summary), req.AgentKey, req.TeamID)
			}
			if err != nil && !errors.Is(err, chat.ErrChatNotFound) {
				if isTimeContractViolation(err) {
					return timeContractStatusError(err)
				}
				return &statusError{Status: 500, Message: err.Error()}
			}
		}
	}
	if strings.TrimSpace(req.ChatID) != "" && s.deps.Chats != nil {
		summary, err := s.deps.Chats.Summary(req.ChatID)
		if err == nil && summary != nil {
			return validateRunStatusOwner(runStatusOwnerFromChatSummary(summary), req.AgentKey, req.TeamID)
		}
		if err != nil && !errors.Is(err, chat.ErrChatNotFound) {
			if isTimeContractViolation(err) {
				return timeContractStatusError(err)
			}
			return &statusError{Status: 500, Message: err.Error()}
		}
	}
	if strings.TrimSpace(req.AgentKey) == "" && strings.TrimSpace(req.TeamID) == "" {
		return &statusError{Status: 400, Message: "agentKey or teamId is required"}
	}
	if strings.TrimSpace(req.AgentKey) != "" && strings.TrimSpace(req.TeamID) != "" {
		return &statusError{Status: 400, Message: "agentKey must be omitted for a Team"}
	}
	return nil
}

func runStatusOwnerFromChatSummary(summary *chat.Summary) contracts.RunStatusInfo {
	if summary == nil {
		return contracts.RunStatusInfo{}
	}
	status := contracts.RunStatusInfo{
		AgentKey: strings.TrimSpace(summary.AgentKey),
		TeamID:   strings.TrimSpace(summary.TeamID),
	}
	if contracts.IsTeamRunOwner(status.AgentKey, status.TeamID) {
		status.AgentKey = ""
	}
	return status
}

func validateSubmitIdentity(req queryinput.SubmitRequest) error {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.AwaitingID) == "" {
		return fmt.Errorf("runId and awaitingId are required")
	}
	if strings.TrimSpace(req.AgentKey) == "" && strings.TrimSpace(req.TeamID) == "" {
		return fmt.Errorf("agentKey or teamId is required")
	}
	if strings.TrimSpace(req.AgentKey) != "" && strings.TrimSpace(req.TeamID) != "" {
		return fmt.Errorf("agentKey must be omitted for a Team")
	}
	return nil
}

func validateRunStatusOwner(status contracts.RunStatusInfo, agentKey string, teamID string) *statusError {
	agentKey = strings.TrimSpace(agentKey)
	teamID = strings.TrimSpace(teamID)
	if agentKey != "" && teamID != "" {
		return &statusError{Status: 400, Message: "historical Team runs are no longer supported; use teamId only for a Team"}
	}
	if contracts.IsTeamRunOwner(status.AgentKey, status.TeamID) {
		if teamID == "" {
			return &statusError{Status: 400, Message: "teamId is required"}
		}
		if agentKey != "" {
			return &statusError{Status: 400, Message: "agentKey is not allowed for team run"}
		}
		if strings.TrimSpace(status.TeamID) != teamID {
			return &statusError{Status: 403, Message: "teamId does not match run"}
		}
		return nil
	}

	if agentKey == "" {
		return &statusError{Status: 400, Message: "agentKey is required"}
	}
	if strings.TrimSpace(status.AgentKey) != agentKey {
		return &statusError{Status: 403, Message: "agentKey does not match run"}
	}
	if teamID != "" && strings.TrimSpace(status.TeamID) != teamID {
		return &statusError{Status: 403, Message: "teamId does not match run"}
	}
	return nil
}

func (s *Service) ValidateRunOwner(runID string, agentKey string, teamID string) *statusError {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return &statusError{Status: 400, Message: "runId is required"}
	}
	if s == nil || s.deps.Runs == nil {
		return &statusError{Status: 404, Message: "run not found"}
	}
	status, ok := s.deps.Runs.RunStatus(runID)
	if !ok {
		return &statusError{Status: 404, Message: "run not found"}
	}
	return validateRunStatusOwner(status, agentKey, teamID)
}
