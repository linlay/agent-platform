package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/view"
	"agent-platform/internal/viewaction"
	"agent-platform/internal/ws"
)

type ViewActionRequest struct {
	ChatID string `json:"chatId"`
	RunID  string `json:"runId"`
	View   struct {
		ConnectorID string `json:"connectorId"`
		Key         string `json:"key"`
		Hash        string `json:"hash"`
	} `json:"view"`
	Action    string          `json:"action"`
	Params    json.RawMessage `json:"params"`
	RequestID string          `json:"requestId"`
}

func (s *Server) invokeView(ctx context.Context, req ViewActionRequest) (viewaction.Result, error) {
	ref := view.Reference{Source: "connector", ConnectorID: req.View.ConnectorID, Key: req.View.Key, Hash: req.View.Hash}
	if !chat.ValidChatID(req.ChatID) || strings.TrimSpace(req.RunID) == "" || ref.Hash == "" || ref.Validate() != nil {
		return viewaction.Result{}, view.ErrInvalid
	}
	if s.deps.Chats == nil {
		return viewaction.Result{}, view.ErrNotFound
	}
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if err != nil {
		return viewaction.Result{}, err
	}
	if summary == nil {
		return viewaction.Result{}, view.ErrNotFound
	}
	// Use live Chat ownership, never an archive/resource ticket or Run's grant.
	if !queryPrincipalCanReferenceChat(ctx, *summary) {
		return viewaction.Result{}, viewaction.ErrDenied
	}
	run, err := s.deps.Chats.LoadRunQuery(req.ChatID, req.RunID)
	if err != nil {
		return viewaction.Result{}, err
	}
	if run == nil {
		return viewaction.Result{}, newAgentStatusError(404, "view_action_run_not_found", "run not found in chat")
	}
	doc, err := view.LoadSnapshot(s.deps.Chats.ChatDir(req.ChatID), ref)
	if err != nil {
		return viewaction.Result{}, err
	}
	subject := "local"
	if p := PrincipalFromContext(ctx); p != nil && strings.TrimSpace(p.Subject) != "" {
		subject = p.Subject
	}
	return s.deps.ViewActions.Execute(ctx, doc, req.Action, req.Params, req.RequestID, viewaction.Context{ChatID: req.ChatID, RunID: req.RunID, AgentKey: run.AgentKey, Subject: subject})
}

func viewActionStatus(err error) (int, string, string) {
	switch {
	case errors.Is(err, viewaction.ErrDenied):
		return 403, "view_action_denied", "view action denied"
	case errors.Is(err, viewaction.ErrStale):
		return 409, "view_action_stale", "view action declaration changed; regenerate the view"
	case errors.Is(err, viewaction.ErrUnavailable):
		return 503, "view_action_unavailable", "view action unavailable"
	case errors.Is(err, view.ErrDataInvalid):
		return 400, "view_action_params_invalid", err.Error()
	}
	return viewQueryStatus(err)
}
func (s *Server) handleViewInvoke(w http.ResponseWriter, r *http.Request) {
	var req ViewActionRequest
	if decodeStrictJSON(r, &req) != nil {
		writeJSON(w, 400, api.Failure(400, "invalid_view"))
		return
	}
	result, err := s.invokeView(r.Context(), req)
	if err != nil {
		status, code, message := viewActionStatus(err)
		writeJSON(w, status, api.Failure(status, code, map[string]any{"message": message}))
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, api.Success(result))
}
func (s *Server) wsViewInvoke(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	defer conn.CompleteRequest(req.ID)
	var payload ViewActionRequest
	decoder := json.NewDecoder(bytes.NewReader(req.Payload))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&payload) != nil || decoder.Decode(&trailing) != io.EOF {
		conn.SendError(req.ID, "invalid_view", 400, "invalid view action request", nil)
		return
	}
	result, err := s.invokeView(ctx, payload)
	if err != nil {
		status, code, message := viewActionStatus(err)
		conn.SendError(req.ID, code, status, message, nil)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", result)
}
