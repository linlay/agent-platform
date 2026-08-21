package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
)

type monitorRuntimeSummary struct {
	Mode            config.RuntimeMode `json:"mode"`
	ActionTransport string             `json:"actionTransport"`
}

type monitorOverview struct {
	GeneratedAt int64                 `json:"generatedAt"`
	Runtime     monitorRuntimeSummary `json:"runtime"`
	WS          ws.MonitorWSSummary   `json:"ws"`
}

func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	messageLimit, err := parseMonitorLimit(r, "messageLimit", 5, 1, 50)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	overview := s.monitorHub().MonitorOverview(messageLimit)
	writeJSON(w, http.StatusOK, api.Success(monitorOverview{
		GeneratedAt: overview.GeneratedAt,
		Runtime: monitorRuntimeSummary{
			Mode:            s.deps.Config.RuntimeMode,
			ActionTransport: "reverse-websocket",
		},
		WS: overview.WS,
	}))
}

func (s *Server) handleMonitorWSConnections(w http.ResponseWriter, r *http.Request) {
	limit, err := parseMonitorLimit(r, "limit", 100, 1, 500)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.monitorHub().MonitorConnections(limit, monitorFilterFromRequest(r))))
}

func (s *Server) handleMonitorWSMessages(w http.ResponseWriter, r *http.Request) {
	limit, err := parseMonitorLimit(r, "limit", 5, 1, 50)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.monitorHub().MonitorMessages(limit, monitorFilterFromRequest(r))))
}

func (s *Server) monitorHub() *ws.Hub {
	if s == nil {
		return nil
	}
	hub, _ := s.deps.Notifications.(*ws.Hub)
	return hub
}

func parseMonitorLimit(r *http.Request, name string, fallback int, min int, max int) (int, error) {
	if r == nil {
		return fallback, nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if limit < min || limit > max {
		return 0, fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return limit, nil
}

func monitorFilterFromRequest(r *http.Request) ws.MonitorFilter {
	if r == nil {
		return ws.MonitorFilter{}
	}
	query := r.URL.Query()
	deviceID := strings.TrimSpace(query.Get("deviceId"))
	if deviceID == "" {
		deviceID = strings.TrimSpace(query.Get("device_id"))
	}
	return ws.MonitorFilter{
		SessionID: strings.TrimSpace(query.Get("sessionId")),
		Source:    strings.TrimSpace(query.Get("source")),
		DeviceID:  deviceID,
	}
}
