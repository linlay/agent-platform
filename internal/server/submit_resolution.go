package server

import (
	"fmt"
	"log"
	"strings"
	"time"

	"agent-platform/internal/api"
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
		if mode := strings.ToLower(strings.TrimSpace(item.Mode)); mode != "" && !isRestorableAwaitingMode(mode) {
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
		if !isRestorableAwaitingMode(effectiveMode) {
			log.Printf("[server][awaiting] clearing non-restorable pending awaiting chatId=%s awaitingId=%s mode=%s", item.ChatID, item.AwaitingID, effectiveMode)
			_ = s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID)
			continue
		}
		timeoutMs := contracts.AnyIntNode(ask.Payload["timeout"])
		if timeoutMs > 0 && nowMs-item.CreatedAt > int64(timeoutMs) {
			log.Printf("[server][awaiting] clearing expired deferred awaiting chatId=%s awaitingId=%s age=%dms timeout=%dms", item.ChatID, item.AwaitingID, nowMs-item.CreatedAt, timeoutMs)
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
	if err := validateSubmitIdentity(req); err != nil {
		return api.SubmitResponse{}, 0, "", err
	}

	if awaiting, ok := s.lookupActiveAwaiting(req); ok {
		if err := validateSubmitParams(awaiting, req.Params); err != nil {
			return api.SubmitResponse{}, 0, "", err
		}
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

	response, err := s.resolveDeferredSubmit(req)
	if err != nil {
		return api.SubmitResponse{}, 0, "", err
	}
	return response, 0, "success", nil
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
	if !isRestorableAwaitingMode(deferred.Mode) {
		s.deferredAwaitings.Remove(req.AwaitingID)
		_ = s.deps.Chats.ClearPendingAwaiting(deferred.ChatID, req.AwaitingID)
		return api.SubmitResponse{}, fmt.Errorf("awaiting is not restorable")
	}
	timeoutMs := contracts.AnyIntNode(deferred.Ask.Payload["timeout"])
	if timeoutMs > 0 && time.Now().UnixMilli()-deferred.CreatedAt > int64(timeoutMs) {
		s.deferredAwaitings.Remove(req.AwaitingID)
		_ = s.deps.Chats.ClearPendingAwaiting(deferred.ChatID, req.AwaitingID)
		return api.SubmitResponse{}, fmt.Errorf("awaiting has expired")
	}
	if err := validateDeferredSubmitParams(deferred.Mode, req.Params); err != nil {
		return api.SubmitResponse{}, err
	}
	if response, handled, err := s.resolvePersistedAwaitingSubmit(api.SubmitRequest{
		ChatID:     firstNonBlank(req.ChatID, deferred.ChatID),
		RunID:      req.RunID,
		AgentKey:   req.AgentKey,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Params:     req.Params,
	}); handled || err != nil {
		return response, err
	}

	normalized, err := s.normalizeDeferredSubmit(deferred, req.Params)
	if err != nil {
		return api.SubmitResponse{}, err
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
	if payload["mode"] == "" {
		payload["mode"] = deferred.Mode
	}
	s.deps.Notifications.Broadcast("awaiting.answer", payload)
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

func isRestorableAwaitingMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question", "plan":
		return true
	default:
		return false
	}
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
