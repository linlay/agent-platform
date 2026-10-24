package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/view"
	"agent-platform/internal/viewquery"
	"agent-platform/internal/ws"
)

// ViewQueryRequest names a frozen snapshot, one of its declared queries and
// the Run whose content published the view. It has no operation selector.
type ViewQueryRequest struct {
	ChatID string `json:"chatId"`
	RunID  string `json:"runId"`
	View   struct {
		ConnectorID string `json:"connectorId"`
		Key         string `json:"key"`
		Hash        string `json:"hash"`
	} `json:"view"`
	Query  string          `json:"query"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ViewQueryResponse is the same for native, CLI and MCP queries; executedAt
// is Unix milliseconds when the query completed.
type ViewQueryResponse struct {
	Data       json.RawMessage `json:"data"`
	ExecutedAt int64           `json:"executedAt"`
}

func (s *Server) queryView(ctx context.Context, req ViewQueryRequest) (ViewQueryResponse, error) {
	ref := view.Reference{Source: "connector", ConnectorID: req.View.ConnectorID, Key: req.View.Key, Hash: req.View.Hash}
	if !chat.ValidChatID(req.ChatID) || strings.TrimSpace(req.RunID) == "" || req.View.Hash == "" || ref.Validate() != nil {
		return ViewQueryResponse{}, view.ErrInvalid
	}
	if s.deps.Chats == nil {
		return ViewQueryResponse{}, view.ErrNotFound
	}
	if principal := PrincipalFromContext(ctx); principal != nil && !s.principalCanAccessResourceChat(principal, req.ChatID) {
		return ViewQueryResponse{}, newAgentStatusError(http.StatusForbidden, "view_access_denied", "view access denied")
	}
	// Queries serve live Chats; archived Chats keep their frozen content only.
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if err != nil {
		return ViewQueryResponse{}, err
	}
	if summary == nil {
		return ViewQueryResponse{}, view.ErrNotFound
	}
	run, err := s.deps.Chats.LoadRunQuery(req.ChatID, req.RunID)
	if err != nil {
		return ViewQueryResponse{}, err
	}
	if run == nil {
		return ViewQueryResponse{}, newAgentStatusError(http.StatusNotFound, "view_query_run_not_found", "run not found in chat")
	}
	doc, err := view.LoadSnapshot(s.deps.Chats.ChatDir(req.ChatID), ref)
	if err != nil {
		return ViewQueryResponse{}, err
	}
	result, err := s.deps.ViewQueries.Execute(ctx, doc, req.Query, req.Params, viewquery.Context{ChatID: req.ChatID, RunID: req.RunID, AgentKey: run.AgentKey})
	if err != nil {
		return ViewQueryResponse{}, err
	}
	return ViewQueryResponse{Data: result.Data, ExecutedAt: result.ExecutedAt}, nil
}

func viewQueryStatus(err error) (int, string, string) {
	// Connector failures keep their code (connector_auth_required, …) for the
	// view to explain, but never 401/403: those would read as the viewer's own
	// Platform session failing.
	var connectorErr *connectorops.Error
	if errors.As(err, &connectorErr) {
		status := connectorErr.Status
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status < 400 {
			status = http.StatusConflict
		}
		return status, connectorErr.Code, connectorErr.Code
	}
	switch {
	case errors.Is(err, view.ErrDataInvalid):
		return http.StatusBadRequest, "view_query_params_invalid", err.Error()
	case errors.Is(err, viewquery.ErrFailed):
		return http.StatusUnprocessableEntity, "view_query_failed", strings.TrimPrefix(err.Error(), viewquery.ErrFailed.Error()+": ")
	case errors.Is(err, viewquery.ErrUnavailable):
		return http.StatusServiceUnavailable, "view_query_unavailable", "view query unavailable"
	}
	status, code := viewStatus(err)
	return status, code, code
}

func (s *Server) handleViewQuery(w http.ResponseWriter, r *http.Request) {
	var req ViewQueryRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid_view"))
		return
	}
	response, err := s.queryView(r.Context(), req)
	if err != nil {
		status, code, message := viewQueryStatus(err)
		writeJSON(w, status, api.Failure(status, code, map[string]any{"message": message}))
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) wsViewQuery(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	defer conn.CompleteRequest(req.ID)
	var payload ViewQueryRequest
	decoder := json.NewDecoder(bytes.NewReader(req.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		conn.SendError(req.ID, "invalid_view", http.StatusBadRequest, "invalid view query request", nil)
		return
	}
	response, err := s.queryView(ctx, payload)
	if err != nil {
		status, code, message := viewQueryStatus(err)
		conn.SendError(req.ID, code, status, message, nil)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
}
