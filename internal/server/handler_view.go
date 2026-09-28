package server

import (
	"context"
	"errors"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/view"
	"agent-platform/internal/ws"
)

// ViewRequest deliberately has no Agent/path/URL selector. A Chat determines
// the owner; a snapshot hash supports history and Team/member presentations.
type ViewRequest struct {
	ChatID      string `json:"chatId"`
	ConnectorID string `json:"connectorId"`
	Key         string `json:"key"`
	Hash        string `json:"hash,omitempty"`
	Usage       string `json:"usage,omitempty"`
}

func (s *Server) getView(ctx context.Context, req ViewRequest) (view.Document, error) {
	ref := view.Reference{ConnectorID: req.ConnectorID, Key: req.Key, Hash: req.Hash}
	if !chat.ValidChatID(req.ChatID) {
		return view.Document{}, view.ErrInvalid
	}
	if err := ref.Validate(); err != nil {
		return view.Document{}, err
	}
	if s.deps.Chats == nil {
		return view.Document{}, view.ErrNotFound
	}
	if principal := PrincipalFromContext(ctx); principal != nil && !s.principalCanAccessResourceChat(principal, req.ChatID) {
		return view.Document{}, newAgentStatusError(http.StatusForbidden, "view_access_denied", "view access denied")
	}
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if err != nil {
		return view.Document{}, err
	}
	if req.Hash != "" {
		if summary != nil {
			return view.LoadSnapshot(s.deps.Chats.ChatDir(req.ChatID), ref)
		}
		if s.deps.Archives != nil {
			archived, err := s.deps.Archives.LoadArchived(req.ChatID)
			if err == nil && archived != nil {
				return view.LoadSnapshot(s.deps.Archives.ChatDir(req.ChatID), ref)
			}
		}
		return view.Document{}, view.ErrNotFound
	}
	if summary == nil {
		return view.Document{}, view.ErrNotFound
	}
	if summary.TeamID != "" {
		return view.Document{}, newAgentStatusError(http.StatusBadRequest, "view_snapshot_required", "Team views require an event snapshot hash")
	}
	def, release, ok := acquireAgentRuntime(s.deps.Registry, summary.AgentKey)
	if !ok {
		return view.Document{}, view.ErrNotFound
	}
	defer release()
	mounts, err := mountedViews(def)
	if err != nil {
		return view.Document{}, err
	}
	usage := req.Usage
	if usage == "" {
		usage = "display"
	}
	doc, err := s.viewService().Resolve(ctx, mounts, ref, usage)
	if err != nil {
		return view.Document{}, err
	}
	doc.View, err = view.SaveSnapshot(s.deps.Chats.ChatDir(req.ChatID), doc)
	return doc, err
}

func viewStatus(err error) (int, string) {
	if errors.Is(err, view.ErrNotFound) {
		return http.StatusNotFound, "view_not_found"
	}
	if errors.Is(err, view.ErrUnavailable) {
		return http.StatusServiceUnavailable, "view_unavailable"
	}
	if errors.Is(err, view.ErrInvalid) {
		return http.StatusBadRequest, "invalid_view"
	}
	var status agentStatusError
	if errors.As(err, &status) {
		return status.status, status.code
	}
	return http.StatusInternalServerError, "view_unavailable"
}

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	doc, err := s.getView(r.Context(), ViewRequest{ChatID: q.Get("chatId"), ConnectorID: q.Get("connectorId"), Key: q.Get("key"), Hash: q.Get("hash"), Usage: q.Get("usage")})
	if err != nil {
		status, code := viewStatus(err)
		writeJSON(w, status, api.Failure(status, code))
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, api.Success(doc))
}

func (s *Server) wsView(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[ViewRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_view", 400, "invalid view request", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	doc, err := s.getView(ctx, payload)
	if err != nil {
		status, code := viewStatus(err)
		conn.SendError(req.ID, code, status, code, nil)
	} else {
		conn.SendResponse(req.Type, req.ID, 0, "success", doc)
	}
	conn.CompleteRequest(req.ID)
}
