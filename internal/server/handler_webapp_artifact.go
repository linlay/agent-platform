package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/webapp"
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleWebappArtifact(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeWebappError(w, &connectorops.Error{Code: "method_not_allowed", Status: 405})
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer wap_") {
		writeWebappError(w, webapp.ErrDenied)
		return
	}
	scope, grantCtx, err := s.webappGrants.Scope(strings.TrimPrefix(authorization, "Bearer "))
	if err != nil {
		writeWebappError(w, err)
		return
	}
	var req struct {
		ChatID     string `json:"chatId"`
		RunID      string `json:"runId,omitempty"`
		ArtifactID string `json:"artifactId,omitempty"`
		Cursor     string `json:"cursor,omitempty"`
		Limit      int    `json:"limit,omitempty"`
	}
	if !decodeWebappRequest(w, r, &req) {
		return
	}
	if !scope.Chats[req.ChatID] || scope.Check() != nil {
		writeWebappError(w, webapp.ErrDenied)
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
		writeWebappError(w, &connectorops.Error{Code: code, Status: status})
	}
	switch r.URL.Path {
	case "/api/webapp/artifact/list":
		offset := 0
		if req.Cursor != "" {
			offset, err = strconv.Atoi(req.Cursor)
		}
		if err != nil || offset < 0 {
			writeWebappError(w, &connectorops.Error{Code: "invalid_arguments", Status: 400})
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
	case "/api/webapp/artifact/get":
		item, e := s.chatResources.GetArtifact(req.ChatID, req.ArtifactID, req.RunID)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, api.Success(item))
	case "/api/webapp/artifact/read":
		f, item, e := s.chatResources.OpenArtifact(req.ChatID, req.ArtifactID, req.RunID)
		if e != nil {
			fail(e)
			return
		}
		defer f.Close()
		stopGrant := context.AfterFunc(grantCtx, func() { f.Close() })
		defer stopGrant()
		stopRequest := context.AfterFunc(r.Context(), func() { f.Close() })
		defer stopRequest()
		if scope.Check() != nil {
			writeWebappError(w, webapp.ErrDenied)
			return
		}
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
