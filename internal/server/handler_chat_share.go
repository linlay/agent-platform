package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

func (s *Server) handleChatShareCreate(w http.ResponseWriter, r *http.Request) {
	if s.conversationHTML == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "conversation HTML export is unavailable"))
		return
	}
	target, err := parseTunnelShareTarget(r.Header.Get(tunnelOriginHeader), r.Header.Get(tunnelAuthorizationHeader))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	var request struct {
		ChatID     string          `json:"chatId"`
		Expiration json.RawMessage `json:"expiration"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(request.ChatID) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid share request"))
		return
	}
	request.ChatID = strings.TrimSpace(request.ChatID)
	if !chat.ValidChatID(request.ChatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid chatId"))
		return
	}
	expiration, err := parseConversationShareExpiration(request.Expiration)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid share expiration"))
		return
	}
	snapshot, err := s.loadConversationSnapshot(request.ChatID, time.Now().UnixMilli())
	if err != nil {
		writeConversationExportError(w, err)
		return
	}
	html, err := s.conversationHTML.Render(snapshot, target.origin)
	if err != nil {
		writeConversationExportError(w, err)
		return
	}
	result, err := s.conversationShares.Create(r.Context(), target, request.ChatID, html, expiration)
	if err != nil {
		status, message := mapTunnelShareError(err)
		writeJSON(w, status, api.Failure(status, message))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(result))
}

func (s *Server) handleChatSharesList(w http.ResponseWriter, r *http.Request) {
	target, err := parseTunnelShareTarget(r.Header.Get(tunnelOriginHeader), r.Header.Get(tunnelAuthorizationHeader))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid chatId"))
		return
	}
	items, err := s.conversationShares.List(r.Context(), target, chatID)
	if err != nil {
		status, message := mapTunnelShareError(err)
		writeJSON(w, status, api.Failure(status, message))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(map[string]any{"items": items}))
}

func parseConversationShareExpiration(value json.RawMessage) (string, error) {
	if len(value) == 0 {
		return defaultShareExpiration, nil
	}
	var expiration string
	if err := json.Unmarshal(value, &expiration); err != nil || !validConversationShareExpiration(expiration) {
		return "", errors.New("invalid share expiration")
	}
	return expiration, nil
}

func (s *Server) handleChatShareRevoke(w http.ResponseWriter, r *http.Request) {
	target, err := parseTunnelShareTarget(r.Header.Get(tunnelOriginHeader), r.Header.Get(tunnelAuthorizationHeader))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	shareID := strings.TrimPrefix(r.URL.Path, "/api/chat/share/")
	if !validConversationShareID(shareID) || strings.Contains(shareID, "/") {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid shareId"))
		return
	}
	if err := s.conversationShares.Revoke(r.Context(), target, shareID); err != nil {
		status, message := mapTunnelShareError(err)
		writeJSON(w, status, api.Failure(status, message))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(map[string]string{"id": shareID}))
}
