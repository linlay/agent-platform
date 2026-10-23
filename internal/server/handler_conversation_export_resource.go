package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/config"
	"agent-platform/internal/conversationexport"
)

func (s *Server) handleChatExportResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p := PrincipalFromContext(r.Context())
	if p == nil || strings.TrimSpace(p.Subject) == "" {
		writeAuthError(w)
		return
	}
	if s.deps.Config.RuntimeMode != config.RuntimeModeDesktop || stringClaim(p.Claims, "scope") != "app" || firstStringClaim(p.Claims, "deviceId", "device_id") == "" {
		writeRequestError(w, &requestError{Code: "forbidden", Status: http.StatusForbidden})
		return
	}
	var req struct {
		ChatID    string `json:"chatId"`
		SourceRef string `json:"sourceRef"`
	}
	if !decodeBoundedRequest(w, r, &req) {
		return
	}
	ref, err := chatresource.NormalizeSourceRef(req.SourceRef)
	if !chat.ValidChatID(req.ChatID) || err != nil || ref != req.SourceRef {
		writeRequestError(w, &requestError{Code: "invalid", Status: http.StatusBadRequest})
		return
	}
	if s.deps.Chats == nil {
		writeRequestError(w, &requestError{Code: "forbidden", Status: http.StatusForbidden})
		return
	}
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if err != nil || summary == nil {
		writeRequestError(w, &requestError{Code: "not_found", Status: http.StatusNotFound})
		return
	}
	file, item, err := s.chatResources.OpenExportResource(r.Context(), req.ChatID, ref)
	if err != nil {
		reason := conversationexport.SnapshotResourceReason(err)
		status := http.StatusInternalServerError
		switch reason {
		case "invalid":
			status = 400
		case "forbidden":
			status = 403
		case "not_found":
			status = 404
		case "changed":
			status = 409
		case "unsupported":
			status = 415
		}
		writeRequestError(w, &requestError{Code: reason, Status: status})
		return
	}
	if err := serveChatResource(w, r, file, item.Name, item.MIMEType); err != nil {
		writeRequestError(w, &requestError{Code: "read_failed", Status: http.StatusInternalServerError})
	}
}
