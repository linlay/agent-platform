package server

import (
	"context"
	"net/http"
	"strings"

	"agent-platform/internal/i18n"
	"agent-platform/internal/ws"
)

func (s *Server) wsLocale(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		Locale string `json:"locale"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if requested := strings.TrimSpace(payload.Locale); requested != "" {
		locale, ok := i18n.NormalizeLocale(requested)
		if !ok {
			conn.SendError(req.ID, "invalid_locale", http.StatusBadRequest, "invalid locale", map[string]any{
				"locale":    requested,
				"supported": i18n.SupportedLocales(),
			})
			conn.CompleteRequest(req.ID)
			return
		}
		conn.SetLocale(locale)
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{
		"locale":   conn.Locale(),
		"scope":    "connection",
		"deviceId": conn.DeviceID(),
	})
	conn.CompleteRequest(req.ID)
}
