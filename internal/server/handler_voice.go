package server

import (
	"net/http"
	"net/url"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/voice"
	"agent-platform/internal/ws"
	"github.com/gorilla/websocket"
)

func (s *Server) handleVoiceCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Voice.Capabilities()))
}

func (s *Server) voiceOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return s.deps.Config.CORS.Enabled && originAllowed(origin, s.deps.Config.CORS.AllowedOriginPatterns)
}

func (s *Server) handleVoiceWS(w http.ResponseWriter, r *http.Request) {
	if s.deps.Voice == nil || s.deps.Voice.Capabilities()["enabled"] != true {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(503, "voice unavailable"))
		return
	}
	header := http.Header{}
	if _, protocol := ws.BearerSubprotocol(r); protocol != "" {
		header.Set("Sec-WebSocket-Protocol", protocol)
	}
	upgrader := websocket.Upgrader{CheckOrigin: s.voiceOriginAllowed}
	conn, err := upgrader.Upgrade(w, r, header)
	if err != nil {
		return
	}
	s.deps.Voice.Serve(conn, voice.Caller{Identity: buildAuthIdentity(PrincipalFromContext(r.Context())), Locale: requestLocale(r, s.deps.Config.Prompts.Runtime.ResolveLocale("")), ResourceBaseURL: s.requestBaseURL(r)})
}
