package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
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

func (s *Server) viewService() *view.Service {
	stateRoot := s.deps.Config.Paths.EffectiveConnectorStateDir()
	return &view.Service{ResolveHeaders: func(id string, headers map[string]string) (map[string]string, error) {
		return connector.ResolveViewHeaders(stateRoot, id, headers)
	}}
}

func mountedViews(def catalog.AgentDefinition) ([]view.Mount, error) {
	var mounts []view.Mount
	for _, mount := range def.ConnectorMounts {
		pkg, err := connector.Load(filepath.Dir(mount.Dir), mount.ID)
		if err != nil {
			return nil, err
		}
		if len(pkg.Views) > 0 {
			mounts = append(mounts, pkg.ViewMount())
		}
	}
	return mounts, nil
}

func (s *Server) configureSessionViews(session *contracts.QuerySession, def catalog.AgentDefinition) error {
	mounts, err := mountedViews(def)
	if err != nil {
		return err
	}
	service := s.viewService()
	chatDir := session.ChatRoot
	if s.deps.Chats != nil {
		chatDir = s.deps.Chats.ChatDir(session.ChatID)
	}
	// The closure is created separately for each member session and uses its
	// frozen mounts, even when its public owner is a Team.
	var mu sync.Mutex
	cache := map[string]view.Reference{}
	session.ResolveView = func(ctx context.Context, ref view.Reference, usage string) (view.Reference, error) {
		mu.Lock()
		defer mu.Unlock()
		key := ref.ConnectorID + "\x00" + ref.Key + "\x00" + usage
		if existing, ok := cache[key]; ok {
			return existing, nil
		}
		doc, err := service.Resolve(ctx, mounts, ref, usage)
		if err != nil {
			return view.Reference{}, err
		}
		resolved, err := view.SaveSnapshot(chatDir, doc)
		if err == nil {
			cache[key] = resolved
		}
		return resolved, err
	}
	return nil
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
