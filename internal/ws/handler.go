package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/i18n"
	"agent-platform/internal/timecontract"

	gws "github.com/gorilla/websocket"
)

type Handler struct {
	cfg               config.WebSocketConfig
	heartbeatInterval time.Duration
	hub               *Hub
	authenticator     TokenAuthenticator
	defaultLocale     string
	upgrader          gws.Upgrader
	routes            map[string]RouteHandler
	dispatch          RouteHandler
}

func NewHandler(cfg config.WebSocketConfig, heartbeatInterval time.Duration, hub *Hub, authenticator TokenAuthenticator) *Handler {
	return &Handler{
		cfg:               cfg,
		heartbeatInterval: heartbeatInterval,
		hub:               hub,
		authenticator:     authenticator,
		defaultLocale:     i18n.DefaultLocale,
		upgrader: gws.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		routes: map[string]RouteHandler{},
	}
}

func (h *Handler) SetDefaultLocale(locale string) {
	if h == nil {
		return
	}
	h.defaultLocale = i18n.ResolveLocale(locale)
}

func (h *Handler) RegisterRoute(frameType string, route RouteHandler) {
	if h == nil || strings.TrimSpace(frameType) == "" || route == nil {
		return
	}
	h.routes[frameType] = route
}

func (h *Handler) SetDispatch(dispatch RouteHandler) {
	if h == nil {
		return
	}
	h.dispatch = dispatch
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.NotFound(w, r)
		return
	}
	token, subprotocol, tokenErr := extractToken(r)
	auth, err := h.authenticator.VerifyToken(r.Context(), token)
	if tokenErr != nil && err == nil {
		subprotocol = ""
	}
	if err != nil {
		if timecontract.IsViolation(err) {
			writeHTTPTimeContractViolation(w, err)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(auth.DeviceID) == "" {
		auth.DeviceID = wsAuthDeviceIDFromRequest(r)
	}
	auth.Subprotocol = subprotocol
	responseHeader := http.Header{}
	if subprotocol != "" {
		responseHeader.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	socket, err := h.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		log.Printf("websocket upgrade failed: path=%s has_subprotocol=%t err=%v", r.URL.Path, subprotocol != "", err)
		return
	}
	conn := NewConn(socket, h.hub, h.cfg, h.heartbeatInterval, auth)
	conn.SetLocale(wsLocaleFromRequest(r, h.defaultLocale))
	conn.SetRequestBaseURL(wsRequestBaseURL(r))
	conn.SetClientInfo(r.RemoteAddr, r.UserAgent())
	conn.SetClientMetadata(wsClientMetadataFromRequest(r, auth))
	dispatch := h.Dispatch
	if h.dispatch != nil {
		dispatch = h.dispatch
	}
	conn.Run(dispatch)
}

func (h *Handler) Dispatch(ctx context.Context, conn *Conn, req RequestFrame) {
	if h == nil {
		return
	}
	h.routeRequest(ctx, conn, req)
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
			if timecontract.IsViolation(verifyErr) {
				conn.SendError(req.ID, "time_contract_violation", http.StatusUnprocessableEntity, "time contract violation", timecontract.ErrorData(verifyErr))
				conn.CompleteRequest(req.ID)
				return
			}
			conn.SendError(req.ID, "unauthorized", 401, "invalid token", nil)
			conn.CompleteRequest(req.ID)
			return
		}
		data := map[string]any{}
		if auth.ExpiresAt > 0 {
			if contractErr := timecontract.ValidateEpochMillis(auth.ExpiresAt, "expiresAt", "ws.auth.refresh"); contractErr != nil {
				conn.SendError(req.ID, "time_contract_violation", http.StatusUnprocessableEntity, "time contract violation", timecontract.ErrorData(contractErr))
				conn.CompleteRequest(req.ID)
				return
			}
			data["expiresAt"] = auth.ExpiresAt
		}
		// Do not mutate connection auth before every outward time point is
		// proven valid. A failed refresh must leave the previous session intact.
		conn.UpdateAuth(auth)
		conn.SendResponse("auth.refresh", req.ID, 0, "success", data)
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

func writeHTTPTimeContractViolation(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": "time_contract_violation",
		"msg":  "time contract violation",
		"data": timecontract.ErrorData(err),
	})
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

func wsRequestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return strings.TrimRight(proto+"://"+host, "/")
}

func wsLocaleFromRequest(r *http.Request, defaultLocale string) string {
	if r == nil {
		return i18n.ResolveLocale(defaultLocale)
	}
	return i18n.LocaleFromHTTP(
		r.URL.Query().Get("locale"),
		r.Header.Get("X-Locale"),
		r.Header.Get("Accept-Language"),
		defaultLocale,
	)
}

func wsAuthDeviceIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	query := r.URL.Query()
	deviceID := strings.TrimSpace(query.Get("deviceId"))
	if deviceID == "" {
		deviceID = strings.TrimSpace(query.Get("device_id"))
	}
	return monitorNormalizeDeviceID(deviceID)
}

func wsClientMetadataFromRequest(r *http.Request, auth AuthSession) (string, string) {
	deviceID := auth.DeviceID
	source := ""
	if r != nil {
		query := r.URL.Query()
		source = query.Get("source")
		if queryDeviceID := strings.TrimSpace(query.Get("deviceId")); queryDeviceID != "" {
			deviceID = queryDeviceID
		} else if queryDeviceID := strings.TrimSpace(query.Get("device_id")); queryDeviceID != "" {
			deviceID = queryDeviceID
		}
	}
	return monitorNormalizeSource(source), monitorNormalizeDeviceID(deviceID)
}

func MarshalPayload(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	data, _ := json.Marshal(value)
	return data
}
