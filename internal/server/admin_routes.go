package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
)

// adminOnly 包裹 admin 路由的两个前置检查：
//  1. deps.GatewayAdmin 必须注入，否则返回 404（admin 能力关闭时对外不可见）
//  2. 请求必须来自 loopback，防止外网误触（desktop 和 platform 同机）
func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.GatewayAdmin == nil {
			http.NotFound(w, r)
			return
		}
		if !isLoopbackRequest(r) {
			writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "admin api requires loopback access"))
			return
		}
		next(w, r)
	}
}

// isLoopbackRequest 判断请求 RemoteAddr 是否来自本机 loopback。
// 兼容 IPv4 (127.0.0.0/8) 和 IPv6 (::1)。无 port / 解析失败时按非 loopback 处理。
func isLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// handleAdminGateways：GET 列表 / POST 注册。
func (s *Server) handleAdminGateways(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries := s.deps.GatewayAdmin.AdminList()
		// Token 不回显，即使 loopback 也遮挡（避免日志意外写出）
		safe := make([]GatewayAdminEntry, 0, len(entries))
		for _, e := range entries {
			e.Token = ""
			safe = append(safe, e)
		}
		writeJSON(w, http.StatusOK, api.Success(map[string]any{"gateways": safe}))
	case http.MethodPost:
		var req GatewayAdminEntry
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid json: "+err.Error()))
			return
		}
		if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.URL) == "" {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "id and url required"))
			return
		}
		if err := s.deps.GatewayAdmin.AdminRegister(req); err != nil {
			writeJSON(w, http.StatusConflict, api.Failure(http.StatusConflict, err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, api.Success(map[string]any{"id": req.ID, "status": "registered"}))
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

// handleAdminGatewayByID：DELETE /api/admin/gateways/:id
func (s *Server) handleAdminGatewayByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/gateways/")
	id = strings.TrimSpace(strings.TrimSuffix(id, "/"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "id required in path"))
		return
	}
	if err := s.deps.GatewayAdmin.AdminUnregister(id); err != nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(map[string]any{"id": id, "status": "unregistered"}))
}
