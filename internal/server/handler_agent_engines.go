package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	runtimeacp "agent-platform/internal/runtime/acp"
	"agent-platform/internal/terminal"
	"agent-platform/internal/ws"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"agent-platform/internal/agentengine"
	dto "agent-platform/internal/contracts/agentengine"
)

func isAgentEngineManagementRoute(path string) bool {
	if path == "/api/local/agent-plugins" || strings.HasPrefix(path, "/api/local/agent-plugins/") {
		return true
	}
	switch path {
	case "/api/desktop/agent-engines/status", "/api/desktop/agent-engines", "/api/desktop/agent-engines/snapshot", "/api/desktop/agent-engines/revoke":
		return true
	}
	return false
}

func engineError(w http.ResponseWriter, err error) {
	code := agentengine.Error("artifact_untrusted")
	errors.As(err, &code)
	status := http.StatusBadRequest
	switch code {
	case "management_unauthorized":
		status = http.StatusUnauthorized
	case "management_forbidden", "artifact_untrusted":
		status = http.StatusForbidden
	case "management_epoch_mismatch", "revision_stale", "revision_conflict", "engine_busy", "artifact_digest_mismatch", "plugin_exists":
		status = http.StatusConflict
	case "unsupported_deployment":
		status = http.StatusNotImplemented
	case "registry_not_ready", "engine_unavailable", "dependency_missing":
		status = http.StatusServiceUnavailable
	}
	body := dto.EngineError{Code: int64(status), Msg: string(code)}
	body.Data.ErrorCode = string(code)
	body.Data.ExecutionState = "not_started"
	writeJSON(w, status, body)
}
func (s *Server) handleAgentEngineManagement(w http.ResponseWriter, r *http.Request) {
	engines := s.deps.AgentEngines
	if engines == nil {
		engineError(w, agentengine.Error("unsupported_deployment"))
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		engineError(w, agentengine.Error("management_forbidden"))
		return
	}
	local := strings.HasPrefix(r.URL.Path, "/api/local/agent-plugins")
	if local != engines.IsStandalone() {
		engineError(w, agentengine.Error("unsupported_deployment"))
		return
	}
	operation := ""
	method := http.MethodPut
	switch r.URL.Path {
	case "/api/desktop/agent-engines/status":
		method = http.MethodGet
	case "/api/desktop/agent-engines":
		operation = "upsert"
	case "/api/desktop/agent-engines/snapshot":
		operation = "snapshot"
	case "/api/desktop/agent-engines/revoke":
		operation = "revoke"
		method = http.MethodPost
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "ZenMindEngine ") {
		engineError(w, agentengine.Error("management_unauthorized"))
		return
	}
	hostHeader := "X-ZenMind-Desktop-Host"
	if local {
		hostHeader = "X-ZenMind-Host"
	}
	if err := engines.Authorize(strings.TrimPrefix(authorization, "ZenMindEngine "), r.Header.Get(hostHeader), r.Header.Get("X-ZenMind-Engine-Epoch"), operation != "" || (local && r.Method != http.MethodGet)); err != nil {
		engineError(w, err)
		return
	}
	if local {
		s.handleLocalAgentPlugins(w, r)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if operation == "" {
		writeJSON(w, 200, map[string]any{"code": 200, "msg": "ok", "data": engines.Status()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		engineError(w, agentengine.Error("invalid_arguments"))
		return
	}
	ack, err := engines.Apply(operation, body)
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"code": 200, "msg": "ok", "data": ack})
}
func (s *Server) handleAgentEngineDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.deps.AgentEngines == nil || s.deps.AgentEngines.Status().HostID == "" {
		engineError(w, agentengine.Error("unsupported_deployment"))
		return
	}
	writeJSON(w, 200, map[string]any{"code": 200, "msg": "ok", "data": s.deps.AgentEngines.Directory()})
}

type EngineSessions interface {
	Preview(context.Context, string, bool) (dto.ConfigPreview, error)
	Inspect(string) (dto.PreparedSession, error)
	ValidateTarget(catalog.AgentDefinition) (string, string, error)
	Prepare(context.Context, string) (dto.PreparedSession, error)
	Configure(context.Context, dto.ConfigUpdate) (dto.PreparedSession, error)
}

// Configuration discovery is project scoped, not a Chat reservation or Query.
func (s *Server) handleAgentEnginePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.deps.EngineSessions == nil {
		engineSessionError(w, agentengine.Error("engine_unavailable"))
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if key == "" {
		engineSessionError(w, agentengine.Error("invalid_arguments"))
		return
	}
	result, err := s.deps.EngineSessions.Preview(r.Context(), key, r.Method == http.MethodPost)
	if err != nil {
		engineSessionError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, api.Success(result))
}

func (s *Server) handleAgentEngineSession(w http.ResponseWriter, r *http.Request) {
	method, contract := http.MethodPost, "SessionPrepare"
	if r.URL.Path == "/api/agent-engines/sessions/config" {
		method, contract = http.MethodPut, "ConfigUpdate"
		if r.Method == http.MethodGet {
			method = http.MethodGet
		}
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var prepare dto.SessionPrepare
	var update dto.ConfigUpdate
	var err error
	if r.Method == http.MethodGet {
		prepare.ChatID = r.URL.Query().Get("chatId")
		w.Header().Set("Cache-Control", "no-store")
	} else {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if readErr != nil || len(body) > 1<<20 {
			engineError(w, agentengine.Error("invalid_arguments"))
			return
		}
		if contract == "SessionPrepare" {
			err = agentengine.Decode(contract, body, &prepare)
		} else {
			err = agentengine.Decode(contract, body, &update)
			prepare.ChatID = update.ChatID
		}
		if err != nil {
			engineError(w, agentengine.Error("invalid_arguments"))
			return
		}
	}
	if !chat.ValidChatID(prepare.ChatID) {
		engineSessionError(w, agentengine.Error("invalid_arguments"))
		return
	}
	if s.deps.EngineSessions == nil {
		engineSessionError(w, agentengine.Error("engine_unavailable"))
		return
	}
	summary, err := s.deps.Chats.Summary(prepare.ChatID)
	if err == nil && summary == nil && prepare.AgentKey != "" && contract == "SessionPrepare" {
		def, ok := s.deps.Registry.AgentDefinition(prepare.AgentKey)
		if !ok {
			engineSessionError(w, agentengine.Error("engine_unavailable"))
			return
		}
		if _, _, err := s.deps.EngineSessions.ValidateTarget(def); err != nil {
			engineSessionError(w, err)
			return
		}
		source := chatSourceFromContext(r.Context())
		principal := PrincipalFromContext(r.Context())
		if principal != nil && principal.Subject != "" {
			source = api.ChatSourceQueryPrefix + principal.Subject
		} else if s.deps.Config.Auth.Enabled {
			writeRequestError(w, &requestError{Code: "chat_access_denied", Status: http.StatusForbidden})
			return
		}
		reserved, _, reserveErr := s.deps.Chats.EnsureChatWithSource(prepare.ChatID, prepare.AgentKey, "", source)
		summary, err = &reserved, reserveErr
	}
	if summary != nil && prepare.AgentKey != "" && summary.AgentKey != prepare.AgentKey {
		writeRequestError(w, &requestError{Code: "chat_access_denied", Status: http.StatusForbidden})
		return
	}
	if err != nil || summary == nil || !queryPrincipalCanReferenceChat(r.Context(), *summary) {
		writeRequestError(w, &requestError{Code: "chat_access_denied", Status: http.StatusForbidden})
		return
	}
	if s.deps.EngineSessions == nil {
		engineError(w, agentengine.Error("engine_unavailable"))
		return
	}
	var result dto.PreparedSession
	if r.Method == http.MethodGet {
		result, err = s.deps.EngineSessions.Inspect(prepare.ChatID)
	} else if contract == "SessionPrepare" {
		result, err = s.deps.EngineSessions.Prepare(r.Context(), prepare.ChatID)
	} else {
		result, err = s.deps.EngineSessions.Configure(r.Context(), update)
	}
	if err != nil {
		engineSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(result))
}
func engineSessionError(w http.ResponseWriter, err error) {
	code := agentengine.Error("rpc_connection_lost")
	errors.As(err, &code)
	if errors.Is(err, runtimeacp.ErrBusy) {
		code = "engine_busy"
	}
	status := http.StatusBadRequest
	switch code {
	case "session_configuration_stale", "session_restore_failed", "engine_busy":
		status = http.StatusConflict
	case "auth_required":
		status = http.StatusUnauthorized
	case "registry_not_ready", "engine_unavailable", "dependency_missing", "rpc_connection_lost":
		status = http.StatusServiceUnavailable
	}
	body := dto.EngineError{Code: int64(status), Msg: string(code)}
	body.Data.ErrorCode = string(code)
	body.Data.ExecutionState = "not_started"
	var call *runtimeacp.CallError
	if errors.As(err, &call) {
		body.Data.ExecutionState = call.ExecutionState
	}
	writeJSON(w, status, body)
}

// Authentication shares the terminal connection boundary. It never accepts
// launch fields or credentials from the renderer.
type EngineAuthentication interface {
	AuthStatus(string, string) (dto.AuthStatus, error)
	StartAuthentication(string, string, string) (dto.AuthStatus, error)
	CancelAuthentication(string, string) error
	AuthTerminal(string, string, string) (terminal.OpenResult, error)
}

func (s *Server) handleAgentEngineAuth(w http.ResponseWriter, r *http.Request) {
	owner := ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		owner = ws.ClientBoundaryKeyForIdentity(p.Subject, firstStringClaim(p.Claims, "deviceId", "device_id"))
	}
	if owner == "" {
		writeRequestError(w, &requestError{Code: "auth_access_denied", Status: http.StatusForbidden})
		return
	}
	method := http.MethodPost
	if r.URL.Path == "/api/agent-engines/auth/status" {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var start dto.AuthStart
	var cancel dto.AuthCancel
	if method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, (1<<16)+1))
		if err != nil || len(body) > 1<<16 {
			engineSessionError(w, agentengine.Error("invalid_arguments"))
			return
		}
		if r.URL.Path == "/api/agent-engines/auth/start" {
			err = agentengine.Decode("AuthStart", body, &start)
		} else {
			err = agentengine.Decode("AuthCancel", body, &cancel)
		}
		if err != nil {
			engineSessionError(w, agentengine.Error("invalid_arguments"))
			return
		}
	}
	auth, ok := s.deps.EngineSessions.(EngineAuthentication)
	if !ok {
		engineSessionError(w, agentengine.Error("engine_unavailable"))
		return
	}
	var result dto.AuthStatus
	var err error
	switch r.URL.Path {
	case "/api/agent-engines/auth/status":
		result, err = auth.AuthStatus(owner, r.URL.Query().Get("engineId"))
	case "/api/agent-engines/auth/start":
		result, err = auth.StartAuthentication(owner, start.EngineID, start.MethodID)
	case "/api/agent-engines/auth/cancel":
		err = auth.CancelAuthentication(owner, cancel.AuthOperationID)
	}
	if err != nil {
		engineSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(result))
}
