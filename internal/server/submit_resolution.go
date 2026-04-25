package server

import (
	"fmt"
	"log"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/hitlsubmit"
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
		s.deferredAwaitings.Register(DeferredAwaiting{
			ChatID:     item.ChatID,
			AwaitingID: item.AwaitingID,
			RunID:      firstNonBlank(item.RunID, ask.RunID),
			Mode:       firstNonBlank(item.Mode, ask.Mode),
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
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
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
		return api.SubmitResponse{}, fmt.Errorf("unknown awaitingId")
	}
	if deferred.Ask == nil || deferred.Ask.Payload == nil {
		return api.SubmitResponse{}, fmt.Errorf("unknown awaitingId")
	}
	if err := validateDeferredSubmitParams(deferred.Mode, req.Params); err != nil {
		return api.SubmitResponse{}, err
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
		"params":     req.Params,
	}
	answerPayload := contracts.CloneMap(normalized)
	answerPayload["type"] = "awaiting.answer"
	answerPayload["awaitingId"] = req.AwaitingID
	answerPayload["runId"] = req.RunID

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
	s.broadcastDeferredAwaitingAnswer(deferred, normalized, resolvedAt)

	return api.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
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
	case "approval", "form":
		return hitlsubmit.Normalize(deferred.Ask.Payload, params)
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
	if payload["mode"] == "" {
		payload["mode"] = deferred.Mode
	}
	s.deps.Notifications.Broadcast("awaiting.answer", payload)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
