package server

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/config"
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
		SourceRef  string `json:"sourceRef,omitempty"`
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
	switch r.URL.Path {
	case "/api/chat/artifacts/list":
		if req.SourceRef != "" {
			writeRequestError(w, &requestError{Code: "invalid_arguments", Status: http.StatusBadRequest})
			return
		}
	case "/api/chat/artifacts/get":
		if req.SourceRef != "" {
			writeRequestError(w, &requestError{Code: "invalid_arguments", Status: http.StatusBadRequest})
			return
		}
	case "/api/chat/artifacts/read":
		if (req.ArtifactID == "") == (req.SourceRef == "") || (req.SourceRef != "" && req.RunID != "") {
			writeRequestError(w, &requestError{Code: "invalid_arguments", Status: http.StatusBadRequest})
			return
		}
	default:
		writeRequestError(w, &requestError{Code: "artifact_not_found", Status: http.StatusNotFound})
		return
	}
	desktopSourceRefRead := r.URL.Path == "/api/chat/artifacts/read" && req.SourceRef != "" && req.ArtifactID == "" &&
		s.deps.Config.RuntimeMode == config.RuntimeModeDesktop && p != nil && strings.TrimSpace(p.Subject) != "" &&
		stringClaim(p.Claims, "scope") == "app" && firstStringClaim(p.Claims, "deviceId", "device_id") != ""
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if err != nil || summary == nil || (!desktopSourceRefRead && !queryPrincipalCanReferenceChat(r.Context(), *summary)) {
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
		var f *os.File
		var item chatresource.Artifact
		var e error
		if req.SourceRef != "" {
			f, item, e = s.chatResources.OpenArtifactByRef(req.ChatID, req.SourceRef)
		} else {
			f, item, e = s.chatResources.OpenArtifact(req.ChatID, req.ArtifactID, req.RunID)
		}
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
	}
}
