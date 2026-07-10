package server

import (
	"fmt"
	"log"
	"strings"
	"time"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/hitl"
)

func (s *Server) hydrateDeferredAwaitings() {
	if s == nil || s.deps.Chats == nil || s.deferredAwaitings == nil {
		return
	}

	items, err := s.deps.Chats.LoadAllPendingAwaitings()
	if err != nil {
		log.Printf("[server][awaiting] load pending awaitings failed: %v", err)
		return
	}
	for _, item := range items {
		nowMs := time.Now().UnixMilli()
		if mode := strings.ToLower(strings.TrimSpace(item.Mode)); mode != "" && !isAwaitingGateMode(mode) {
			log.Printf("[server][awaiting] clearing non-restorable pending awaiting chatId=%s awaitingId=%s mode=%s", item.ChatID, item.AwaitingID, item.Mode)
			_ = s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID)
			continue
		}
		ask, err := s.deps.Chats.LoadAwaitingAsk(item.ChatID, item.AwaitingID)
		if err != nil {
			log.Printf("[server][awaiting] load awaiting ask failed chatId=%s awaitingId=%s err=%v", item.ChatID, item.AwaitingID, err)
			continue
		}
		if ask == nil {
			log.Printf("[server][awaiting] clearing dangling pending awaiting chatId=%s awaitingId=%s", item.ChatID, item.AwaitingID)
			_ = s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID)
			continue
		}
		if ask.Payload == nil {
			ask.Payload = map[string]any{}
		}
		if strings.TrimSpace(ask.RunID) == "" {
			ask.RunID = item.RunID
		}
		if strings.TrimSpace(ask.Mode) == "" {
			ask.Mode = item.Mode
		}
		if strings.TrimSpace(stringValue(ask.Payload["runId"])) == "" && strings.TrimSpace(item.RunID) != "" {
			ask.Payload["runId"] = item.RunID
		}
		if strings.TrimSpace(stringValue(ask.Payload["mode"])) == "" && strings.TrimSpace(item.Mode) != "" {
			ask.Payload["mode"] = item.Mode
		}
		effectiveMode := firstNonBlank(item.Mode, ask.Mode, stringValue(ask.Payload["mode"]))
		if !isAwaitingGateMode(effectiveMode) {
			log.Printf("[server][awaiting] clearing non-restorable pending awaiting chatId=%s awaitingId=%s mode=%s", item.ChatID, item.AwaitingID, effectiveMode)
			_ = s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID)
			continue
		}
		timeoutSec := contracts.AnyIntNode(ask.Payload["timeout"])
		if timeoutSec > 0 && nowMs-item.CreatedAt > int64(timeoutSec)*1000 {
			log.Printf("[server][awaiting] clearing expired deferred awaiting chatId=%s awaitingId=%s age=%dms timeout=%ds", item.ChatID, item.AwaitingID, nowMs-item.CreatedAt, timeoutSec)
			_ = s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID)
			continue
		}
		s.deferredAwaitings.Register(DeferredAwaiting{
			ChatID:     item.ChatID,
			AwaitingID: item.AwaitingID,
			RunID:      firstNonBlank(item.RunID, ask.RunID),
			Mode:       effectiveMode,
			CreatedAt:  item.CreatedAt,
			Ask:        ask,
		})
	}
}

func (s *Server) resolveSubmit(req api.SubmitRequest) (api.SubmitResponse, int, string, error) {
	req = s.normalizeActiveSubmitRun(req)
	if err := validateSubmitIdentity(req); err != nil {
		return api.SubmitResponse{}, 0, "", err
	}

	if awaiting, ok := s.lookupActiveAwaiting(req); ok {
		if err := validateSubmitParams(awaiting, req.Params); err != nil {
			if strings.EqualFold(strings.TrimSpace(awaiting.Mode), "question") {
				return invalidQuestionSubmitResponse(req, activeSubmitChatID(s, req), err), 0, "success", nil
			}
			return api.SubmitResponse{}, 0, "", err
		}
		req = s.prepareActiveSubmitContinuation(req, awaiting)
		ack := s.deps.Runs.Submit(req)
		code := 0
		msg := "success"
		if ack.Status == "already_resolved" {
			code = 409
			msg = "already_resolved"
		}
		return api.SubmitResponse{
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
		return response, code, msg, nil
	}

	response, err := s.resolveDeferredSubmit(req)
	if err != nil {
		return api.SubmitResponse{}, 0, "", err
	}
	return response, 0, "success", nil
}

func (s *Server) resolveAlreadyHandledActiveSubmit(req api.SubmitRequest) (api.SubmitResponse, int, string, bool) {
	if s == nil || s.deps.Runs == nil {
		return api.SubmitResponse{}, 0, "", false
	}
	if _, ok := s.deps.Runs.RunStatus(req.RunID); !ok {
		return api.SubmitResponse{}, 0, "", false
	}
	ack := s.deps.Runs.Submit(req)
	if ack.Status != "already_resolved" && !(ack.Accepted && ack.Status == "accepted") {
		return api.SubmitResponse{}, 0, "", false
	}
	code := 0
	msg := "success"
	if ack.Status == "already_resolved" {
		code = 409
		msg = "already_resolved"
	}
	return api.SubmitResponse{
		Accepted:   ack.Accepted,
		Status:     ack.Status,
		ChatID:     activeSubmitChatID(s, req),
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   ack.SubmitID,
		Detail:     ack.Detail,
	}, code, msg, true
}

func (s *Server) prepareActiveSubmitContinuation(req api.SubmitRequest, awaiting contracts.AwaitingSubmitContext) api.SubmitRequest {
	if !strings.EqualFold(strings.TrimSpace(awaiting.Mode), "plan") {
		return req
	}
	if submitPlanDecision(req.Params) != "approve" {
		return req
	}
	if s == nil || s.deps.Runs == nil || s.deps.Registry == nil {
		return req
	}
	status, ok := s.deps.Runs.RunStatus(req.RunID)
	if !ok {
		return req
	}
	agentKey := firstNonBlank(req.AgentKey, status.AgentKey)
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok || !agentcoder.IsMode(agentDef.Mode) || catalog.AgentUsesACPCoderBackend(agentDef) {
		return req
	}
	if strings.TrimSpace(req.ChatID) == "" {
		req.ChatID = strings.TrimSpace(status.ChatID)
	}
	if strings.TrimSpace(req.ContinuationRunID) == "" {
		req.ContinuationRunID = newRunID()
	}
	return req
}

func submitPlanDecision(params api.SubmitParams) string {
	items, err := api.DecodeSubmitParams(params)
	if err != nil || len(items) != 1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(stringValue(items[0]["decision"])))
}

func (s *Server) resolveDeferredSubmit(req api.SubmitRequest) (api.SubmitResponse, error) {
	if s == nil || s.deferredAwaitings == nil {
		return api.SubmitResponse{}, fmt.Errorf("unknown awaitingId")
	}

	deferred, ok := s.deferredAwaitings.Lookup(req.AwaitingID)
	if !ok || strings.TrimSpace(deferred.RunID) != strings.TrimSpace(req.RunID) {
		response, handled, err := s.resolvePersistedAwaitingSubmit(req)
		if handled || err != nil {
			return response, err
		}
		return api.SubmitResponse{}, fmt.Errorf("unknown awaitingId")
	}
	if strings.TrimSpace(req.ChatID) != "" && strings.TrimSpace(req.ChatID) != strings.TrimSpace(deferred.ChatID) {
		return api.SubmitResponse{}, fmt.Errorf("chatId does not match awaiting")
	}
	if deferred.Ask == nil || deferred.Ask.Payload == nil {
		return api.SubmitResponse{}, fmt.Errorf("unknown awaitingId")
	}
	timeoutSec := contracts.AnyIntNode(deferred.Ask.Payload["timeout"])
	if timeoutSec > 0 && time.Now().UnixMilli()-deferred.CreatedAt > int64(timeoutSec)*1000 {
		s.deferredAwaitings.Remove(req.AwaitingID)
		_ = s.deps.Chats.ClearPendingAwaiting(deferred.ChatID, req.AwaitingID)
		return api.SubmitResponse{}, fmt.Errorf("awaiting has expired")
	}
	if err := validateDeferredSubmitParams(deferred.Mode, req.Params); err != nil {
		if strings.EqualFold(strings.TrimSpace(deferred.Mode), "question") {
			return invalidQuestionSubmitResponse(req, deferred.ChatID, err), nil
		}
		return api.SubmitResponse{}, err
	}
	if response, handled, err := s.resolvePersistedAwaitingSubmit(api.SubmitRequest{
		ChatID:     firstNonBlank(req.ChatID, deferred.ChatID),
		RunID:      req.RunID,
		AgentKey:   req.AgentKey,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Locale:     req.Locale,
		Params:     req.Params,
	}); handled || err != nil {
		return response, err
	}

	normalized, err := s.normalizeDeferredSubmit(deferred, req.Params)
	if err != nil {
		if strings.EqualFold(strings.TrimSpace(deferred.Mode), "question") {
			return invalidQuestionSubmitResponse(req, deferred.ChatID, err), nil
		}
		return api.SubmitResponse{}, err
	}
	if !isContinuableDeferredAwaitingMode(deferred.Mode) {
		return s.resolveNonContinuableDeferredSubmit(deferred, req, normalized)
	}
	if s.deferredPlanApproveStartsNewRun(deferred, normalized) && strings.TrimSpace(req.ContinuationRunID) == "" {
		req.ContinuationRunID = newRunID()
	}

	resolvedAt := time.Now().UnixMilli()
	submitPayload := map[string]any{
		"type":       "request.submit",
		"chatId":     deferred.ChatID,
		"runId":      req.RunID,
		"awaitingId": req.AwaitingID,
		"submitId":   req.SubmitID,
		"params":     req.Params,
	}
	answerPayload := contracts.CloneMap(normalized)
	answerPayload["type"] = "awaiting.answer"
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
		return api.SubmitResponse{}, err
	}
	if err := s.persistDeferredAwaitingToolAnswer(deferred.ChatID, req.RunID, req.AwaitingID, answerPayload, resolvedAt); err != nil {
		return api.SubmitResponse{}, err
	}
	if err := s.deps.Chats.ClearPendingAwaiting(deferred.ChatID, req.AwaitingID); err != nil {
		return api.SubmitResponse{}, err
	}
	s.deferredAwaitings.Remove(req.AwaitingID)
	s.broadcastDeferredAwaitingAnswer(deferred, answerPayload, resolvedAt)
	continued, continueErr := s.startAwaitingContinuation(deferred, req, answerPayload)
	if continueErr != nil {
		log.Printf("[server][awaiting] continue run failed chatId=%s runId=%s awaitingId=%s err=%v", deferred.ChatID, req.RunID, req.AwaitingID, continueErr)
	}

	return api.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		ChatID:     deferred.ChatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Continued:  continued,
		Detail:     "Frontend submit accepted",
	}, nil
}

func invalidQuestionSubmitResponse(req api.SubmitRequest, chatID string, err error) api.SubmitResponse {
	detail := "invalid question submit"
	if err != nil {
		detail = err.Error()
	}
	return api.SubmitResponse{
		Accepted:   false,
		Status:     "invalid",
		ChatID:     chatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Detail:     detail,
	}
}

func (s *Server) deferredPlanApproveStartsNewRun(deferred DeferredAwaiting, normalized map[string]any) bool {
	if planContinuationDecision(deferred.Mode, normalized) != "approve" {
		return false
	}
	if s == nil || s.deps.Chats == nil || s.deps.Registry == nil {
		return false
	}
	summary, err := s.deps.Chats.Summary(deferred.ChatID)
	if err != nil || summary == nil {
		return false
	}
	agentDef, ok := s.deps.Registry.AgentDefinition(summary.AgentKey)
	return ok && agentcoder.IsMode(agentDef.Mode) && !catalog.AgentUsesACPCoderBackend(agentDef)
}

func (s *Server) resolveNonContinuableDeferredSubmit(deferred DeferredAwaiting, req api.SubmitRequest, normalized map[string]any) (api.SubmitResponse, error) {
	mode := strings.ToLower(strings.TrimSpace(deferred.Mode))
	if !nonContinuableDeferredSubmitUnlocks(mode, normalized) {
		return api.SubmitResponse{}, fmt.Errorf("%s awaiting cannot be approved after restart; reject or cancel to unlock", mode)
	}

	resolvedAt := time.Now().UnixMilli()
	submitPayload := map[string]any{
		"type":       "request.submit",
		"chatId":     deferred.ChatID,
		"runId":      req.RunID,
		"awaitingId": req.AwaitingID,
		"submitId":   req.SubmitID,
		"params":     req.Params,
	}
	answerPayload := contracts.CloneMap(normalized)
	answerPayload["type"] = "awaiting.answer"
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
		return api.SubmitResponse{}, err
	}
	if err := s.deps.Chats.ClearPendingAwaiting(deferred.ChatID, req.AwaitingID); err != nil {
		return api.SubmitResponse{}, err
	}
	s.deferredAwaitings.Remove(req.AwaitingID)
	s.broadcastDeferredAwaitingAnswer(deferred, answerPayload, resolvedAt)

	return api.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		ChatID:     deferred.ChatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Continued:  false,
		Detail:     "Frontend submit accepted; original run was not continued",
	}, nil
}

func (s *Server) normalizeDeferredSubmit(deferred DeferredAwaiting, params api.SubmitParams) (map[string]any, error) {
	mode := strings.ToLower(strings.TrimSpace(deferred.Mode))
	switch mode {
	case "question":
		var (
			handler frontendtools.Handler
			ok      bool
		)
		if s.deps.FrontendTools != nil {
			handler, ok = s.deps.FrontendTools.Handler("ask_user_question")
		}
		if !ok || handler == nil {
			handler = frontendtools.NewAskUserQuestionHandler()
		}
		return handler.NormalizeSubmit(deferred.Ask.Payload, params)
	case "approval", "form", "plan":
		return hitl.Normalize(deferred.Ask.Payload, params)
	default:
		return nil, fmt.Errorf("unsupported awaiting mode: %s", deferred.Mode)
	}
}

func nonContinuableDeferredSubmitUnlocks(mode string, normalized map[string]any) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if strings.TrimSpace(stringValue(contracts.AnyMapNode(normalized["error"])["code"])) == "user_dismissed" {
		return true
	}
	switch mode {
	case "approval":
		return allNormalizedDecisionsReject(normalized["approvals"])
	case "form":
		return allNormalizedDecisionsReject(normalized["forms"])
	default:
		return false
	}
}

func allNormalizedDecisionsReject(value any) bool {
	items := normalizedDecisionItems(value)
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if strings.ToLower(strings.TrimSpace(stringValue(item["decision"]))) != "reject" {
			return false
		}
	}
	return true
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

func (s *Server) broadcastDeferredAwaitingAnswer(deferred DeferredAwaiting, normalized map[string]any, resolvedAt int64) {
	if s == nil || s.deps.Notifications == nil {
		return
	}
	payload := map[string]any{
		"chatId":     deferred.ChatID,
		"runId":      firstNonBlank(deferred.RunID, stringValue(normalized["runId"])),
		"awaitingId": deferred.AwaitingID,
		"mode":       strings.TrimSpace(stringValue(normalized["mode"])),
		"status":     strings.TrimSpace(stringValue(normalized["status"])),
		"resolvedAt": resolvedAt,
	}
	if errCode := strings.TrimSpace(stringValue(contracts.AnyMapNode(normalized["error"])["code"])); errCode != "" {
		payload["errorCode"] = errCode
	}
	if submitID := strings.TrimSpace(stringValue(normalized["submitId"])); submitID != "" {
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

func (s *Server) resolvePersistedAwaitingSubmit(req api.SubmitRequest) (api.SubmitResponse, bool, error) {
	chatID := strings.TrimSpace(req.ChatID)
	if s == nil || s.deps.Chats == nil || chatID == "" {
		return api.SubmitResponse{}, false, nil
	}
	latest, err := s.deps.Chats.LoadLatestAwaitingSubmit(chatID, req.AwaitingID)
	if err != nil || latest == nil {
		return api.SubmitResponse{}, latest != nil, err
	}
	if strings.TrimSpace(latest.RunID) != "" && strings.TrimSpace(latest.RunID) != strings.TrimSpace(req.RunID) {
		return api.SubmitResponse{}, false, nil
	}
	if strings.TrimSpace(req.SubmitID) != "" && strings.TrimSpace(latest.SubmitID) == strings.TrimSpace(req.SubmitID) {
		mode := firstNonBlank(stringValue(latest.Answer["mode"]), stringValue(latest.Submit["mode"]))
		deferred := DeferredAwaiting{
			ChatID:     chatID,
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
			Mode:       mode,
		}
		continued, continueErr := s.startAwaitingContinuation(deferred, req, latest.Answer)
		if continueErr != nil {
			log.Printf("[server][awaiting] continue accepted submit failed chatId=%s runId=%s awaitingId=%s submitId=%s err=%v", chatID, req.RunID, req.AwaitingID, req.SubmitID, continueErr)
		}
		return api.SubmitResponse{
			Accepted:   true,
			Status:     "accepted",
			ChatID:     chatID,
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
			SubmitID:   req.SubmitID,
			Continued:  continued,
			Detail:     "Frontend submit accepted",
		}, true, nil
	}
	return api.SubmitResponse{
		Accepted:   false,
		Status:     "already_resolved",
		ChatID:     chatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Detail:     "Frontend submit already resolved",
	}, true, nil
}

func activeSubmitChatID(s *Server, req api.SubmitRequest) string {
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

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
