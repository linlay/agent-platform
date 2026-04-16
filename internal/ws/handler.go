package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/config"

	gws "github.com/gorilla/websocket"
)

type Handler struct {
	cfg               config.WebSocketConfig
	heartbeatInterval time.Duration
	hub               *Hub
	authenticator     TokenAuthenticator
	upgrader          gws.Upgrader
	routes            map[string]RouteHandler
}

func NewHandler(cfg config.WebSocketConfig, heartbeatInterval time.Duration, hub *Hub, authenticator TokenAuthenticator) *Handler {
	return &Handler{
		cfg:               cfg,
		heartbeatInterval: heartbeatInterval,
		hub:               hub,
		authenticator:     authenticator,
		upgrader: gws.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		routes: map[string]RouteHandler{},
	}
}

func (h *Handler) RegisterRoute(frameType string, route RouteHandler) {
	if h == nil || strings.TrimSpace(frameType) == "" || route == nil {
		return
	}
	h.routes[frameType] = route
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || !h.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	token, subprotocol, tokenErr := extractToken(r)
	auth, err := h.authenticator.VerifyToken(r.Context(), token)
	if tokenErr != nil && err == nil {
		subprotocol = ""
	}
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	auth.Subprotocol = subprotocol
	responseHeader := http.Header{}
	if subprotocol != "" {
		responseHeader.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	socket, err := h.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return
	}
	conn := NewConn(socket, h.hub, h.cfg, h.heartbeatInterval, auth)
	conn.SendPush("connected", map[string]any{"sessionId": conn.SessionID()})
	conn.Run(func(ctx context.Context, conn *Conn, req RequestFrame) {
		h.routeRequest(ctx, conn, req)
	})
}

func (h *Handler) routeRequest(ctx context.Context, conn *Conn, req RequestFrame) {
	if req.Type == "auth.refresh" {
		payload, err := DecodePayload[struct {
			Token string `json:"token"`
		}](req)
		if err != nil || strings.TrimSpace(payload.Token) == "" {
			conn.SendError(req.ID, "invalid_request", 400, "token is required", nil)
			conn.CompleteRequest(req.ID)
			return
		}
		auth, verifyErr := h.authenticator.VerifyToken(ctx, strings.TrimSpace(payload.Token))
		if verifyErr != nil {
			conn.SendError(req.ID, "unauthorized", 401, "invalid token", nil)
			conn.CompleteRequest(req.ID)
			return
		}
		conn.UpdateAuth(auth)
		conn.SendResponse("auth.refresh", req.ID, 0, "success", map[string]any{"expiresAt": auth.ExpiresAt})
		conn.CompleteRequest(req.ID)
		return
	}
	route := h.routes[req.Type]
	if route == nil {
		conn.SendError(req.ID, "invalid_request", 400, "unknown type: "+req.Type, nil)
		conn.CompleteRequest(req.ID)
		return
	}
	route(ctx, conn, req)
}

func extractToken(r *http.Request) (string, string, error) {
	if r == nil {
		return "", "", errors.New("request is required")
	}
	for _, protocol := range gws.Subprotocols(r) {
		trimmed := strings.TrimSpace(protocol)
		if strings.HasPrefix(trimmed, "bearer.") {
			token := strings.TrimPrefix(trimmed, "bearer.")
			if token != "" {
				return token, trimmed, nil
			}
		}
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		return "", "", errors.New("missing token")
	}
	return token, "", nil
}

func MarshalPayload(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	data, _ := json.Marshal(value)
	return data
}
