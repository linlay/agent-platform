package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	sessionbuild "agent-platform/internal/runtime/session"
	"agent-platform/internal/ws"
)

var errMissingKBaseAgent = errors.New("agentKey is required")
var errUnknownKBaseAgent = errors.New("agent not found")

func (s *Server) kbaseOptions(agentKey string) (api.KBaseOptionsResponse, int, error) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return api.KBaseOptionsResponse{}, 400, errMissingKBaseAgent
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return api.KBaseOptionsResponse{}, 404, errUnknownKBaseAgent
	}
	response := api.KBaseOptionsResponse{AgentKey: agentKey, Capability: sessionbuild.KBaseCapability(def), BoundLibraryID: def.KBaseConfig.LibraryID, Libraries: []api.KBaseOption{}}
	if !def.Interaction().MustUseKbases {
		response.Capability = api.KBaseCapability{Reason: "interactionConfig.mustUseKbases is disabled"}
	}
	if s.deps.KBases == nil {
		response.Capability = api.KBaseCapability{Reason: "knowledge library service unavailable"}
		return response, 200, nil
	}
	libraries, err := s.deps.KBases.LibraryOptions()
	if err != nil {
		return response, 503, err
	}
	response.Libraries = libraries
	return response, 200, nil
}

func (s *Server) handleKBaseOptions(w http.ResponseWriter, r *http.Request) {
	response, status, err := s.kbaseOptions(r.URL.Query().Get("agentKey"))
	if err != nil {
		writeJSON(w, status, api.Failure(status, err.Error()))
		return
	}
	writeJSON(w, status, api.Success(response))
}

func (s *Server) wsKBaseOptions(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	defer conn.CompleteRequest(req.ID)
	payload, err := ws.DecodePayload[struct {
		AgentKey string `json:"agentKey"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, err.Error(), nil)
		return
	}
	response, status, err := s.kbaseOptions(payload.AgentKey)
	if err != nil {
		code := "invalid_request"
		if status == 404 {
			code = "not_found"
		} else if status >= 500 {
			code = "unavailable"
		}
		conn.SendError(req.ID, code, status, err.Error(), nil)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
}
