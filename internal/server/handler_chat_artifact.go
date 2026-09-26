package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chatresource"
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleChatArtifact(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeRequestError(w, &requestError{Code: "method_not_allowed", Status: 405})
		return
	}
	p := PrincipalFromContext(r.Context())
	if p == nil || strings.TrimSpace(p.Subject) == "" {
		writeAuthError(w)
		return
	}
	var req struct {
		ChatID     string `json:"chatId"`
		RunID      string `json:"runId,omitempty"`
		ArtifactID string `json:"artifactId,omitempty"`
		Cursor     string `json:"cursor,omitempty"`
		Limit      int    `json:"limit,omitempty"`
	}
	if !decodeBoundedRequest(w, r, &req) {
		return
	}
	if s.deps.Chats == nil {
		writeRequestError(w, &requestError{Code: "chat_access_denied", Status: http.StatusForbidden})
		return
	}
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if err != nil || summary == nil || !queryPrincipalCanReferenceChat(r.Context(), *summary) {
		writeRequestError(w, &requestError{Code: "chat_access_denied", Status: http.StatusForbidden})
		return
	}
	fail := func(err error) {
		code, status := "artifact_not_found", 404
		if errors.Is(err, chatresource.ErrArtifactAmbiguous) {
			code, status = "artifact_ambiguous", 409
		}
		if errors.Is(err, chatresource.ErrArtifactChanged) {
			code, status = "artifact_changed", 409
		}
		writeRequestError(w, &requestError{Code: code, Status: status})
	}
	switch r.URL.Path {
	case "/api/chat/artifacts/list":
		offset := 0
		if req.Cursor != "" {
			offset, err = strconv.Atoi(req.Cursor)
		}
		if err != nil || offset < 0 {
			writeRequestError(w, &requestError{Code: "invalid_arguments", Status: 400})
			return
		}
		if req.Limit == 0 {
			req.Limit = 50
		}
		items, more, e := s.chatResources.ListArtifacts(req.ChatID, req.RunID, offset, req.Limit)
		if e != nil {
			fail(e)
			return
		}
		result := map[string]any{"items": items}
		if more {
			result["nextCursor"] = strconv.Itoa(offset + len(items))
		}
		writeJSON(w, 200, api.Success(result))
	case "/api/chat/artifacts/get":
		item, e := s.chatResources.GetArtifact(req.ChatID, req.ArtifactID, req.RunID)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, api.Success(item))
	case "/api/chat/artifacts/read":
		f, item, e := s.chatResources.OpenArtifact(req.ChatID, req.ArtifactID, req.RunID)
		if e != nil {
			fail(e)
			return
		}
		defer f.Close()
		stopRequest := context.AfterFunc(r.Context(), func() { f.Close() })
		defer stopRequest()
		info, e := f.Stat()
		if e != nil {
			fail(e)
			return
		}
		w.Header().Set("Content-Type", item.MIMEType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Name}))
		http.ServeContent(w, r, item.Name, info.ModTime(), f)
	default:
		fail(chatresource.ErrArtifactNotFound)
	}
}
