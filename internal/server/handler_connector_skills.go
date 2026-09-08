package server

import (
	"errors"
	"net/http"
	"os"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
)

func (s *Server) handleConnectorSkills(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !connector.ValidID(id) {
		s.writeConnectorSkillError(w, connector.ErrInvalidSkillTarget)
		return
	}
	skills, err := catalog.ConnectorSkills(s.connectorSources(), id)
	if err != nil {
		s.writeConnectorSkillError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, map[string]any{"connectorId": id, "skills": skills}, nil)
}

func (s *Server) handleConnectorSkillDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := catalog.ReadConnectorSkill(s.connectorSources(), r.URL.Query().Get("id"), r.URL.Query().Get("name"))
	if err != nil {
		s.writeConnectorSkillError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, detail, nil)
}

func (s *Server) writeConnectorSkillError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusBadRequest, "invalid_connector_skill", "connector skill cannot be read"
	switch {
	case errors.Is(err, os.ErrNotExist):
		status, code, message = http.StatusNotFound, "connector_skill_not_found", "connector or skill not found"
	case errors.Is(err, connector.ErrSkillDocumentTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "connector_skill_too_large", err.Error()
	case errors.Is(err, connector.ErrInvalidSkillTarget):
		message = err.Error()
	}
	s.writeAgentHTTPResponse(w, nil, newAgentStatusError(status, code, message))
}
