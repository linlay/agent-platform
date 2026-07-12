package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
)

func validateSubmitIdentity(req api.SubmitRequest) error {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.AwaitingID) == "" {
		return fmt.Errorf("runId and awaitingId are required")
	}
	if strings.TrimSpace(req.AgentKey) == "" && strings.TrimSpace(req.TeamID) == "" {
		return fmt.Errorf("agentKey or teamId is required")
	}
	return nil
}

func (s *Server) validateRunOwner(runID string, agentKey string, teamID string) *statusError {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return &statusError{status: http.StatusBadRequest, message: "runId is required"}
	}
	if s == nil || s.deps.Runs == nil {
		return &statusError{status: http.StatusNotFound, message: "run not found"}
	}
	status, ok := s.deps.Runs.RunStatus(runID)
	if !ok {
		return &statusError{status: http.StatusNotFound, message: "run not found"}
	}
	return validateRunStatusOwner(status, agentKey, teamID)
}

// validateRunAgentKey remains as a compatibility adapter for agent-only
// controls. New control endpoints should pass both public identity fields to
// validateRunOwner.
func (s *Server) validateRunAgentKey(runID string, agentKey string) *statusError {
	return s.validateRunOwner(runID, agentKey, "")
}

func validateRunStatusOwner(status contracts.RunStatusInfo, agentKey string, teamID string) *statusError {
	agentKey = strings.TrimSpace(agentKey)
	teamID = strings.TrimSpace(teamID)
	ownerType := status.OwnerType
	if ownerType == contracts.RunOwnerTypeTeam {
		if teamID == "" {
			return &statusError{status: http.StatusBadRequest, message: "teamId is required"}
		}
		if agentKey != "" {
			return &statusError{status: http.StatusBadRequest, message: "agentKey is not allowed for team run"}
		}
		if strings.TrimSpace(status.TeamID) != teamID {
			return &statusError{status: http.StatusForbidden, message: "teamId does not match run"}
		}
		return nil
	}

	// Empty OwnerType is treated as agent for compatibility with older run
	// manager implementations and server test doubles.
	if agentKey == "" {
		return &statusError{status: http.StatusBadRequest, message: "agentKey is required"}
	}
	if strings.TrimSpace(status.AgentKey) != agentKey {
		return &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}
	}
	if teamID != "" && strings.TrimSpace(status.TeamID) != teamID {
		return &statusError{status: http.StatusForbidden, message: "teamId does not match run"}
	}
	return nil
}

func (s *Server) validateSubmitOwner(req api.SubmitRequest) *statusError {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.AwaitingID) == "" {
		return &statusError{status: http.StatusBadRequest, message: "runId and awaitingId are required"}
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
				return &statusError{status: http.StatusInternalServerError, message: err.Error()}
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
			return &statusError{status: http.StatusInternalServerError, message: err.Error()}
		}
	}
	if strings.TrimSpace(req.AgentKey) == "" && strings.TrimSpace(req.TeamID) == "" {
		return &statusError{status: http.StatusBadRequest, message: "agentKey or teamId is required"}
	}
	return nil
}

func (s *Server) validateSubmitAgentKey(req api.SubmitRequest) *statusError {
	return s.validateSubmitOwner(req)
}

func runStatusOwnerFromChatSummary(summary *chat.Summary) contracts.RunStatusInfo {
	if summary == nil {
		return contracts.RunStatusInfo{}
	}
	status := contracts.RunStatusInfo{
		OwnerType: contracts.RunOwnerTypeAgent,
		AgentKey:  strings.TrimSpace(summary.AgentKey),
		TeamID:    strings.TrimSpace(summary.TeamID),
	}
	if strings.EqualFold(strings.TrimSpace(summary.OwnerType), string(contracts.RunOwnerTypeTeam)) ||
		(status.AgentKey == "" && status.TeamID != "") {
		status.OwnerType = contracts.RunOwnerTypeTeam
		status.AgentKey = ""
	}
	return status
}

func (s *Server) lookupActiveAwaiting(req api.SubmitRequest) (contracts.AwaitingSubmitContext, bool) {
	if s == nil || s.deps.Runs == nil {
		return contracts.AwaitingSubmitContext{}, false
	}
	return s.deps.Runs.LookupAwaiting(req.RunID, req.AwaitingID)
}

func validateSubmitParams(ctx contracts.AwaitingSubmitContext, params api.SubmitParams) error {
	if len(params) == 0 {
		return nil
	}
	if len(ctx.Routes) > 0 {
		return validateTeamMergedSubmitParams(ctx, params)
	}
	items, err := api.DecodeSubmitParams(params)
	if err != nil {
		return err
	}
	if len(items) != ctx.ItemCount {
		return fmt.Errorf("expected %d submit items, got %d", ctx.ItemCount, len(items))
	}
	for index, item := range items {
		if err := validateSubmitItem(ctx.Mode, index, item); err != nil {
			return err
		}
	}
	if strings.EqualFold(strings.TrimSpace(ctx.Mode), "question") && len(ctx.Questions) > 0 {
		if _, err := frontendtools.NewAskUserQuestionHandler().NormalizeSubmit(map[string]any{
			"questions": ctx.Questions,
		}, params); err != nil {
			return err
		}
	}
	return nil
}

func validateTeamMergedSubmitParams(ctx contracts.AwaitingSubmitContext, params api.SubmitParams) error {
	items, err := api.DecodeSubmitParams(params)
	if err != nil {
		return err
	}
	if len(items) != len(ctx.Routes) {
		return fmt.Errorf("expected %d submit items, got %d", len(ctx.Routes), len(items))
	}
	for index, item := range items {
		if err := validateSubmitItem("form", index, item); err != nil {
			return err
		}
		route := ctx.Routes[index]
		fieldID := strings.TrimSpace(stringValue(item["id"]))
		if fieldID == "" || fieldID != strings.TrimSpace(route.FieldID) {
			return fmt.Errorf("items[%d]: id must be %q", index, route.FieldID)
		}
		if strings.EqualFold(strings.TrimSpace(stringValue(item["decision"])), "reject") {
			continue
		}
		form := contracts.AnyMapNode(item["form"])
		rawParams, ok := form["params"]
		if !ok {
			return fmt.Errorf("items[%d]: form.params is required", index)
		}
		childParams, encodeErr := api.EncodeSubmitParams(rawParams)
		if encodeErr != nil {
			return fmt.Errorf("items[%d]: form.params must be an array", index)
		}
		childContext := contracts.AwaitingSubmitContext{
			AwaitingID: route.AwaitingID,
			TaskID:     route.TaskID,
			Mode:       route.Mode,
			ItemCount:  route.ItemCount,
			Questions:  append([]any(nil), route.Questions...),
		}
		if err := validateSubmitParams(childContext, childParams); err != nil {
			return fmt.Errorf("items[%d].form.params: %w", index, err)
		}
	}
	return nil
}

func validateDeferredSubmitParams(mode string, params api.SubmitParams) error {
	if len(params) == 0 {
		return nil
	}
	items, err := api.DecodeSubmitParams(params)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(mode), "planning") && len(items) != 1 {
		return fmt.Errorf("expected 1 submit items, got %d", len(items))
	}
	for index, item := range items {
		if err := validateSubmitItem(mode, index, item); err != nil {
			return err
		}
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
		decision := strings.ToLower(strings.TrimSpace(stringValue(item["decision"])))
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
	case "form":
		if _, hasAnswer := item["answer"]; hasAnswer {
			return fmt.Errorf("%s: form items do not allow answer", itemLabel)
		}
		if _, hasAnswers := item["answers"]; hasAnswers {
			return fmt.Errorf("%s: form items do not allow answers", itemLabel)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: form items do not allow payload", itemLabel)
		}
		if _, hasAction := item["action"]; hasAction {
			return fmt.Errorf("%s: form items no longer use action, use decision instead", itemLabel)
		}
		decision := strings.ToLower(strings.TrimSpace(stringValue(item["decision"])))
		if decision == "" {
			return fmt.Errorf("%s: form items require decision", itemLabel)
		}
		if rawForm, hasForm := item["form"]; hasForm {
			form, ok := rawForm.(map[string]any)
			if !ok || form == nil {
				return fmt.Errorf("%s: form field must be an object", itemLabel)
			}
		}
		switch decision {
		case "approve":
			if _, hasForm := item["form"]; !hasForm {
				return fmt.Errorf("%s: approve decision requires form", itemLabel)
			}
		case "reject":
		default:
			return fmt.Errorf("%s: unsupported form decision %q", itemLabel, decision)
		}
	case "planning":
		decision := strings.ToLower(strings.TrimSpace(stringValue(item["decision"])))
		if decision == "" {
			return fmt.Errorf("%s: planning items require decision", itemLabel)
		}
		switch decision {
		case "approve", "reject":
		default:
			return fmt.Errorf("%s: unsupported planning decision %q", itemLabel, decision)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: planning items do not allow payload", itemLabel)
		}
		if _, hasAnswer := item["answer"]; hasAnswer {
			return fmt.Errorf("%s: planning items do not allow answer", itemLabel)
		}
		if _, hasAnswers := item["answers"]; hasAnswers {
			return fmt.Errorf("%s: planning items do not allow answers", itemLabel)
		}
		if _, hasForm := item["form"]; hasForm {
			return fmt.Errorf("%s: planning items do not allow form", itemLabel)
		}
	default:
		return fmt.Errorf("unsupported awaiting mode: %s", mode)
	}
	return nil
}
