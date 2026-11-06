package server

import (
	"context"
	"errors"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/controlscope"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/ws"
)

type userSender interface {
	Send(context.Context, queryinput.SendRequest, runtimetypes.QueryCommand) (mailbox.Receipt, error)
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	detail, ok := requestDetailLevel(w, r)
	if !ok {
		return
	}
	var input queryinput.SendRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, 400, api.Failure(400, err.Error()))
		return
	}
	cmd := runtimetypes.QueryCommand{Detail: detail, Locale: requestLocale(r, s.deps.Config.Prompts.Runtime.ResolveLocale("")), ResourceBaseURL: s.requestBaseURL(r), ClientTarget: runtimeClientTarget(webClientTargetFromHTTPRequest(r))}
	if p := PrincipalFromContext(r.Context()); p != nil {
		cmd.Caller.Subject = p.Subject
	}
	ctx := controlscope.WithContext(r.Context(), httpControlScope(r.Context(), "main"))
	result, err := s.sendUserMessage(ctx, input, cmd)
	if err != nil {
		writeQueryStartError(w, sendRequestError(err))
		return
	}
	writeJSON(w, 200, api.Success(result))
}
func (s *Server) sendUserMessage(ctx context.Context, r queryinput.SendRequest, cmd runtimetypes.QueryCommand) (mailbox.Receipt, error) {
	sender, ok := s.deps.Runtime.(userSender)
	if !ok {
		return mailbox.Receipt{}, &statusError{Status: 503, Message: "sending service unavailable"}
	}
	return sender.Send(ctx, r, cmd)
}
func sendRequestError(err error) error {
	var tool *contracts.RunToolError
	if errors.As(err, &tool) {
		status := 400
		if tool.Code == "idempotency_conflict" {
			status = 409
		}
		return &statusError{Status: status, Code: tool.Code, Message: tool.Message}
	}
	return err
}
func (s *Server) wsSend(ctx context.Context, conn *ws.Conn, frame ws.RequestFrame) {
	defer conn.CompleteRequest(frame.ID)
	if conn.QueryLane() != "main" {
		conn.SendError(frame.ID, "invalid_lane", 400, "send is main-lane only", nil)
		return
	}
	input, err := ws.DecodePayload[queryinput.SendRequest](frame)
	if err != nil {
		conn.SendError(frame.ID, "invalid_request", 400, err.Error(), nil)
		return
	}
	cmd := runtimetypes.QueryCommand{Detail: frame.DetailLevel(), Locale: conn.Locale(), ResourceBaseURL: conn.RequestBaseURL(), ClientTarget: runtimeClientTarget(conn.WebClientTarget())}
	if p := PrincipalFromContext(ctx); p != nil {
		cmd.Caller.Subject = p.Subject
	}
	result, err := s.sendUserMessage(controlscope.WithContext(ctx, wsControlScope(conn)), input, cmd)
	if err != nil {
		s.sendWSQueryStartError(conn, frame.ID, sendRequestError(err))
		return
	}
	conn.SendResponse(frame.Type, frame.ID, 0, "success", result)
}
