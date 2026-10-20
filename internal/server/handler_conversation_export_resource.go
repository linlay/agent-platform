package server

import (
	"context"
	"mime"
	"net/http"
	"strings"

	"agent-platform/internal/chat"
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
	ref, err := conversationexport.NormalizeSnapshotSourceRef(req.SourceRef)
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
	file, item, err := conversationexport.OpenSnapshotResource(r.Context(), s.deps.Chats, req.ChatID, ref)
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
	defer file.Close()
	stop := context.AfterFunc(r.Context(), func() { file.Close() })
	defer stop()
	info, err := file.Stat()
	if err != nil {
		writeRequestError(w, &requestError{Code: "read_failed", Status: 500})
		return
	}
	w.Header().Set("Content-Type", item.MIMEType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Name}))
	http.ServeContent(w, r, item.Name, info.ModTime(), file)
}
