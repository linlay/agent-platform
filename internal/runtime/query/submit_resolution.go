package query

import (
	"fmt"
	"log"
	"strings"
	"time"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/hitl"
	sessionbuild "agent-platform/internal/runtime/session"
	"agent-platform/internal/toolinteraction"
)

func (s *Service) resolveSubmit(req queryinput.SubmitRequest) (queryinput.SubmitResponse, int, string, error) {
	req = s.normalizeActiveSubmitRun(req)
	if err := validateSubmitIdentity(req); err != nil {
		return queryinput.SubmitResponse{}, 0, "", err
	}

	if awaiting, ok := s.lookupActiveAwaiting(req); ok {
		if err := ValidateSubmitParams(awaiting, req); err != nil {
			if strings.EqualFold(strings.TrimSpace(awaiting.Mode), "question") {
				return invalidQuestionSubmitResponse(req, activeSubmitChatID(s, req), err), 0, "success", nil
			}
			return queryinput.SubmitResponse{}, 0, "", err
		}
		var err error
		req, err = s.prepareActiveSubmitContinuation(req, awaiting)
		if err != nil {
			return queryinput.SubmitResponse{}, 0, "", err
		}
		ack := s.deps.Runs.Submit(req)
		code := 0
		msg := "success"
		if ack.Status == "already_resolved" {
			return queryinput.SubmitResponse{}, 0, "", awaitingSubmitConflictError(
				req,
				activeSubmitChatID(s, req),
				"already_resolved",
				"already_resolved",
				"Tool interaction submit already resolved",
			)
		}
		return queryinput.SubmitResponse{
			Accepted:   ack.Accepted,
			Status:     ack.Status,
			ChatID:     activeSubmitChatID(s, req),
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
			SubmitID:   ack.SubmitID,
			Detail:     ack.Detail,
		}, code, msg, nil
	}

	if response, code, msg, ok := s.resolveAlreadyHandledActiveSubmit(req); ok {
		if code == 409 {
			return queryinput.SubmitResponse{}, 0, "", awaitingSubmitConflictError(
				req,
				response.ChatID,
				"already_resolved",
				"already_resolved",
				response.Detail,
			)
		}
		return response, code, msg, nil
	}

	response, err := s.resolveDeferredSubmit(req)
	if err != nil {
		return queryinput.SubmitResponse{}, 0, "", err
	}
	return response, 0, "success", nil
}

func (s *Service) resolveNonContinuableDeferredSubmit(deferred DeferredAwaiting, req queryinput.SubmitRequest, normalized map[string]any) (queryinput.SubmitResponse, error) {
	mode := strings.ToLower(strings.TrimSpace(deferred.Mode))
	if !nonContinuableDeferredSubmitUnlocks(mode, normalized) {
		return queryinput.SubmitResponse{}, fmt.Errorf("%s awaiting cannot be approved after restart; reject or cancel to unlock", mode)
	}

	resolvedAt := time.Now().UnixMilli()
	submitPayload := map[string]any{
		"type":       "request.submit",
		"timestamp":  resolvedAt,
		"chatId":     deferred.ChatID,
		"runId":      req.RunID,
		"awaitingId": req.AwaitingID,
		"submitId":   req.SubmitID,
	}
	req.WriteInput(submitPayload)
	answerPayload := contracts.CloneMap(normalized)
	answerPayload["type"] = "awaiting.answer"
	answerPayload["timestamp"] = resolvedAt
	answerPayload["awaitingId"] = req.AwaitingID
	answerPayload["runId"] = req.RunID
	if strings.TrimSpace(req.SubmitID) != "" {
		answerPayload["submitId"] = strings.TrimSpace(req.SubmitID)
	}
	if duration, ok := awaitingDurationMs(deferred.CreatedAt, resolvedAt); ok {
		answerPayload["durationMs"] = duration
	}

	if err := s.deps.Chats.AppendSubmitLine(deferred.ChatID, chat.SubmitLine{
		ChatID:    deferred.ChatID,
		RunID:     req.RunID,
		UpdatedAt: resolvedAt,
		Submit:    submitPayload,
		Answer:    answerPayload,
		Type:      "submit",
	}); err != nil {
		return queryinput.SubmitResponse{}, err
	}
	if err := s.deps.Chats.ClearPendingAwaiting(deferred.ChatID, req.AwaitingID); err != nil {
		return queryinput.SubmitResponse{}, err
	}
	s.deferredAwaitings.Remove(req.AwaitingID)
	s.broadcastDeferredAwaitingAnswer(deferred, answerPayload, resolvedAt)
	if s.mailbox != nil {
		s.mailbox.Kick(deferred.ChatID)
	}

	return queryinput.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		ChatID:     deferred.ChatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Continued:  false,
		Detail:     "Tool interaction submit accepted; original run was not continued",
	}, nil
}

func (s *Service) resolveAlreadyHandledActiveSubmit(req queryinput.SubmitRequest) (queryinput.SubmitResponse, int, string, bool) {
	if s == nil || s.deps.Runs == nil {
		return queryinput.SubmitResponse{}, 0, "", false
	}
	if _, ok := s.deps.Runs.RunStatus(req.RunID); !ok {
		return queryinput.SubmitResponse{}, 0, "", false
	}
	ack := s.deps.Runs.Submit(req)
	if ack.Status != "already_resolved" && !(ack.Accepted && ack.Status == "accepted") {
		return queryinput.SubmitResponse{}, 0, "", false
	}
	code := 0
	msg := "success"
	if ack.Status == "already_resolved" {
		code = 409
		msg = "already_resolved"
	}
	return queryinput.SubmitResponse{
		Accepted:   ack.Accepted,
		Status:     ack.Status,
		ChatID:     activeSubmitChatID(s, req),
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   ack.SubmitID,
		Detail:     ack.Detail,
	}, code, msg, true
}

func (s *Service) resolvePersistedAwaitingSubmit(req queryinput.SubmitRequest) (queryinput.SubmitResponse, bool, error) {
	chatID := strings.TrimSpace(req.ChatID)
	if s == nil || s.deps.Chats == nil || chatID == "" {
		return queryinput.SubmitResponse{}, false, nil
	}
	latest, err := s.deps.Chats.LoadLatestAwaitingSubmit(chatID, req.AwaitingID)
	if err != nil || latest == nil {
		return queryinput.SubmitResponse{}, latest != nil, err
	}
	if strings.TrimSpace(latest.RunID) != "" && strings.TrimSpace(latest.RunID) != strings.TrimSpace(req.RunID) {
		return queryinput.SubmitResponse{}, false, nil
	}
	switch restartTerminalAwaitingCode(latest.Answer) {
	case "timeout":
		return queryinput.SubmitResponse{}, true, awaitingSubmitConflictError(req, chatID, "expired", "awaiting_expired", "awaiting has expired")
	case "runtime_restarted":
		return queryinput.SubmitResponse{}, true, awaitingSubmitConflictError(req, chatID, "interrupted", "awaiting_interrupted", "awaiting was interrupted by a Platform restart")
	}
	if strings.TrimSpace(req.SubmitID) != "" && strings.TrimSpace(latest.SubmitID) == strings.TrimSpace(req.SubmitID) {
		// A durable receipt may acknowledge a repeated submit, but it cannot
		// recreate an executor or approve a plan from a previous process.
		return queryinput.SubmitResponse{
			Accepted:   true,
			Status:     "accepted",
			ChatID:     chatID,
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
			SubmitID:   req.SubmitID,
			Continued:  false,
			Detail:     "Tool interaction submit accepted",
		}, true, nil
	}
	return queryinput.SubmitResponse{}, true, awaitingSubmitConflictError(req, chatID, "already_resolved", "already_resolved", "Tool interaction submit already resolved")
}

func (s *Service) normalizeDeferredSubmit(deferred DeferredAwaiting, params any) (map[string]any, error) {
	if deferred.Mode == "wait" {
		return nil, fmt.Errorf("wait is resolved by time, events, or steer")
	}
	mode := strings.ToLower(strings.TrimSpace(deferred.Mode))
	switch mode {
	case "question":
		var (
			handler toolinteraction.Handler
			ok      bool
		)
		if s.deps.ToolInteractions != nil {
			handler, ok = s.deps.ToolInteractions.Handler("ask_user_question")
		}
		if !ok || handler == nil {
			handler = toolinteraction.NewAskUserQuestionHandler()
		}
		return handler.NormalizeSubmit(deferred.Ask.Payload, params)
	case "approval", "form", "planning":
		return hitl.Normalize(deferred.Ask.Payload, params)
	default:
		return nil, fmt.Errorf("unsupported awaiting mode: %s", deferred.Mode)
	}
}

func (s *Service) resolveDeferredSubmit(req queryinput.SubmitRequest) (queryinput.SubmitResponse, error) {
	if s == nil || s.deferredAwaitings == nil {
		return queryinput.SubmitResponse{}, unknownAwaitingSubmitError(req)
	}

	deferred, ok := s.deferredAwaitings.Lookup(req.AwaitingID)
	if !ok || strings.TrimSpace(deferred.RunID) != strings.TrimSpace(req.RunID) {
		response, handled, err := s.resolvePersistedAwaitingSubmit(req)
		if handled || err != nil {
			return response, err
		}
		return queryinput.SubmitResponse{}, unknownAwaitingSubmitError(req)
	}
	if strings.TrimSpace(req.ChatID) != "" && strings.TrimSpace(req.ChatID) != strings.TrimSpace(deferred.ChatID) {
		return queryinput.SubmitResponse{}, unknownAwaitingSubmitError(req)
	}
	switch strings.TrimSpace(deferred.TerminalCode) {
	case "timeout":
		return queryinput.SubmitResponse{}, awaitingSubmitConflictError(req, deferred.ChatID, "expired", "awaiting_expired", "awaiting has expired")
	case "runtime_restarted":
		return queryinput.SubmitResponse{}, awaitingSubmitConflictError(req, deferred.ChatID, "interrupted", "awaiting_interrupted", "awaiting was interrupted by a Platform restart")
	}
	if deferred.Ask == nil || deferred.Ask.Payload == nil {
		return queryinput.SubmitResponse{}, unknownAwaitingSubmitError(req)
	}
	timeoutSec := contracts.AnyIntNode(deferred.Ask.Payload["timeout"])
	if nowMs := time.Now().UnixMilli(); awaitingTimeoutApplies(deferred.Mode) && timeoutSec > 0 && nowMs-deferred.CreatedAt > int64(timeoutSec)*1000 {
		answer := contracts.AwaitingTimeoutAnswer(deferred.Mode, int64(timeoutSec), maxInt64((nowMs-deferred.CreatedAt)/1000, int64(timeoutSec)))
		item := chat.PendingAwaitingWithChat{
			ChatID:     deferred.ChatID,
			AwaitingID: req.AwaitingID,
			RunID:      req.RunID,
			Mode:       deferred.Mode,
			CreatedAt:  deferred.CreatedAt,
		}
		state, err := s.FinishTerminalAwaiting(item, answer, nowMs)
		if err != nil {
			return queryinput.SubmitResponse{}, err
		}
		if state == contracts.AwaitingResolutionOwned {
			return queryinput.SubmitResponse{}, awaitingSubmitConflictError(req, deferred.ChatID, "already_resolved", "already_resolved", "awaiting continuation is already resuming")
		}
		return queryinput.SubmitResponse{}, awaitingSubmitConflictError(req, deferred.ChatID, "expired", "awaiting_expired", "awaiting has expired")
	}
	if err := ValidateDeferredSubmitParams(deferred.Mode, req); err != nil {
		if strings.EqualFold(strings.TrimSpace(deferred.Mode), "question") {
			return invalidQuestionSubmitResponse(req, deferred.ChatID, err), nil
		}
		return queryinput.SubmitResponse{}, err
	}
	if response, handled, err := s.resolvePersistedAwaitingSubmit(queryinput.SubmitRequest{
		ChatID:   firstNonBlank(req.ChatID, deferred.ChatID),
		RunID:    req.RunID,
		AgentKey: req.AgentKey,

		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Locale:     req.Locale,
		Param:      req.Param,
		Params:     req.Params,
	}); handled || err != nil {
		return response, err
	}

	normalized, err := s.normalizeDeferredSubmit(deferred, req.Input())
	if err != nil {
		if strings.EqualFold(strings.TrimSpace(deferred.Mode), "question") {
			return invalidQuestionSubmitResponse(req, deferred.ChatID, err), nil
		}
		return queryinput.SubmitResponse{}, err
	}
	if !isContinuableDeferredAwaitingMode(deferred.Mode) {
		return s.resolveNonContinuableDeferredSubmit(deferred, req, normalized)
	}
	continuationAdmission, err := s.resolveAwaitingContinuationAdmission(deferred.ChatID, req.AgentKey)
	if err != nil {
		return queryinput.SubmitResponse{}, err
	}
	startsNewExecutionRun := agentbuiltin.StartsNewExecutionRun(deferred.Mode, normalized, continuationAdmission.AgentDef.Mode, continuationAdmission.AgentDef.ACPBridgeID)
	if startsNewExecutionRun {
		req.ChatID = deferred.ChatID
		if err := s.preparePlanExecutionAccess(req, &continuationAdmission); err != nil {
			return queryinput.SubmitResponse{}, err
		}
	}
	if startsNewExecutionRun && strings.TrimSpace(req.ContinuationRunID) == "" {
		req.ContinuationRunID = newRunID()
	}
	if s.deps.Runs != nil {
		// A run registered after the persisted-submit lookup owns this
		// awaiting; its live executor handles all writes.
		if _, registered := s.deps.Runs.RunStatus(req.RunID); registered {
			return queryinput.SubmitResponse{}, awaitingSubmitConflictError(req, deferred.ChatID, "already_resolved", "already_resolved", "awaiting continuation is already resuming")
		}
	}

	resolvedAt := time.Now().UnixMilli()
	submitPayload := map[string]any{
		"type":       "request.submit",
		"timestamp":  resolvedAt,
		"chatId":     deferred.ChatID,
		"runId":      req.RunID,
		"awaitingId": req.AwaitingID,
		"submitId":   req.SubmitID,
	}
	req.WriteInput(submitPayload)
	answerPayload := contracts.CloneMap(normalized)
	answerPayload["type"] = "awaiting.answer"
	answerPayload["timestamp"] = resolvedAt
	answerPayload["awaitingId"] = req.AwaitingID
	answerPayload["runId"] = req.RunID
	if strings.TrimSpace(req.SubmitID) != "" {
		answerPayload["submitId"] = strings.TrimSpace(req.SubmitID)
	}
	if duration, ok := awaitingDurationMs(deferred.CreatedAt, resolvedAt); ok {
		answerPayload["durationMs"] = duration
	}
	if err := s.deps.Chats.AppendSubmitLine(deferred.ChatID, chat.SubmitLine{
		ChatID:    deferred.ChatID,
		RunID:     req.RunID,
		UpdatedAt: resolvedAt,
		Submit:    submitPayload,
		Answer:    answerPayload,
		Type:      "submit",
	}); err != nil {
		return queryinput.SubmitResponse{}, err
	}
	if err := s.PersistDeferredAwaitingToolAnswer(deferred.ChatID, req.RunID, req.AwaitingID, answerPayload, resolvedAt); err != nil {
		return queryinput.SubmitResponse{}, err
	}
	if err := s.deps.Chats.ClearPendingAwaiting(deferred.ChatID, req.AwaitingID); err != nil {
		return queryinput.SubmitResponse{}, err
	}
	s.deferredAwaitings.Remove(req.AwaitingID)
	s.broadcastDeferredAwaitingAnswer(deferred, answerPayload, resolvedAt)
	continued, continueErr := s.startAwaitingContinuationWithAdmission(deferred, req, answerPayload, &continuationAdmission)
	if continueErr != nil {
		log.Printf("[server][awaiting] continue run failed chatId=%s runId=%s awaitingId=%s err=%v", deferred.ChatID, req.RunID, req.AwaitingID, continueErr)
		if statusErr, ok := continueErr.(*statusError); ok && statusErr.Code == "time_contract_violation" {
			return queryinput.SubmitResponse{}, statusErr
		}
	}
	if s.mailbox != nil {
		s.mailbox.Kick(deferred.ChatID)
	}

	return queryinput.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		ChatID:     deferred.ChatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Continued:  continued,
		Detail:     "Tool interaction submit accepted",
	}, nil
}

func activeSubmitChatID(s *Service, req queryinput.SubmitRequest) string {
	if strings.TrimSpace(req.ChatID) != "" {
		return strings.TrimSpace(req.ChatID)
	}
	if s != nil && s.deps.Runs != nil {
		if status, ok := s.deps.Runs.RunStatus(req.RunID); ok {
			return strings.TrimSpace(status.ChatID)
		}
	}
	return ""
}

func awaitingDurationMs(createdAt int64, resolvedAt int64) (int64, bool) {
	if createdAt <= 0 || resolvedAt <= 0 {
		return 0, false
	}
	duration := resolvedAt - createdAt
	if duration < 0 {
		duration = 0
	}
	return duration, true
}

func allNormalizedDecisionsReject(value any) bool {
	items := normalizedDecisionItems(value)
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if strings.ToLower(strings.TrimSpace(sessionbuild.StringValue(item["decision"]))) != "reject" {
			return false
		}
	}
	return true
}

func invalidQuestionSubmitResponse(req queryinput.SubmitRequest, chatID string, err error) queryinput.SubmitResponse {
	detail := "invalid question submit"
	if err != nil {
		detail = err.Error()
	}
	return queryinput.SubmitResponse{
		Accepted:   false,
		Status:     "invalid",
		ChatID:     chatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Detail:     detail,
	}
}

func normalizedDecisionItems(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			if item := contracts.AnyMapNode(raw); len(item) > 0 {
				items = append(items, item)
			}
		}
		return items
	default:
		return nil
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Service) broadcastDeferredAwaitingAnswer(deferred DeferredAwaiting, normalized map[string]any, answeredAt int64) {
	if s == nil || s.deps.Notifications == nil {
		return
	}
	payload := map[string]any{
		"chatId":     deferred.ChatID,
		"runId":      firstNonBlank(deferred.RunID, sessionbuild.StringValue(normalized["runId"])),
		"awaitingId": deferred.AwaitingID,
		"mode":       strings.TrimSpace(sessionbuild.StringValue(normalized["mode"])),
		"status":     strings.TrimSpace(sessionbuild.StringValue(normalized["status"])),
		"answeredAt": answeredAt,
	}
	if errCode := strings.TrimSpace(sessionbuild.StringValue(contracts.AnyMapNode(normalized["error"])["code"])); errCode != "" {
		payload["errorCode"] = errCode
	}
	if submitID := strings.TrimSpace(sessionbuild.StringValue(normalized["submitId"])); submitID != "" {
		payload["submitId"] = submitID
	}
	if _, ok := normalized["durationMs"]; ok {
		payload["durationMs"] = contracts.AnyIntNode(normalized["durationMs"])
	}
	if payload["mode"] == "" {
		payload["mode"] = deferred.Mode
	}
	s.deps.Notifications.Broadcast("awaiting.answered", payload)
}

func (s *Service) prepareActiveSubmitContinuation(req queryinput.SubmitRequest, awaiting contracts.AwaitingSubmitContext) (queryinput.SubmitRequest, error) {
	if !strings.EqualFold(strings.TrimSpace(awaiting.Mode), "planning") {
		return req, nil
	}
	if agentbuiltin.SubmitPlanningDecision(req.Param) != "approve" {
		return req, nil
	}
	if s == nil || s.deps.Runs == nil || s.deps.Registry == nil {
		return req, nil
	}
	status, ok := s.deps.Runs.RunStatus(req.RunID)
	if !ok {
		return req, nil
	}
	agentKey := firstNonBlank(req.AgentKey, status.AgentKey)
	if statusChatID := strings.TrimSpace(status.ChatID); statusChatID != "" {
		req.ChatID = statusChatID
	}
	admission, err := s.resolveAwaitingContinuationAdmission(req.ChatID, agentKey)
	if err != nil {
		return req, err
	}
	if !agentbuiltin.NativePlanning(admission.AgentDef.Mode, admission.AgentDef.ACPBridgeID) {
		return req, nil
	}
	if strings.TrimSpace(req.ContinuationRunID) == "" {
		req.ContinuationRunID = newRunID()
	}
	if err := s.preparePlanExecutionAccess(req, &admission); err != nil {
		return req, err
	}
	req.ContinuationState = &admission
	return req, nil
}

func nonContinuableDeferredSubmitUnlocks(mode string, normalized map[string]any) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if strings.TrimSpace(sessionbuild.StringValue(contracts.AnyMapNode(normalized["error"])["code"])) == "user_dismissed" {
		return true
	}
	switch mode {
	case "approval":
		return allNormalizedDecisionsReject(normalized["approvals"])
	case "form":
		return strings.EqualFold(sessionbuild.StringValue(contracts.AnyMapNode(normalized["form"])["decision"]), "reject")
	default:
		return false
	}
}
