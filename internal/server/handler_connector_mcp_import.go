package server

import (
	"errors"
	"io"
	"net/http"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts/queryinput"
)

const connectorMCPImportMaxBytes = 64 << 10

func (s *Server) handleConnectorMCPImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, connectorMCPImportMaxBytes)
	defer r.Body.Close()
	data, readErr := io.ReadAll(r.Body)
	var max *http.MaxBytesError
	if errors.As(readErr, &max) {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusRequestEntityTooLarge, "payload_too_large", "MCP import request exceeds 64 KiB"))
		return
	}
	// Preserve duplicate-key validation and require exact public field names.
	req := struct {
		ID          string             `json:"id"`
		Description string             `json:"description"`
		IconPNG     string             `json:"iconPng"`
		IconPreset  string             `json:"iconPreset"`
		Name        string             `json:"name"`
		TokenAuth   string             `json:"tokenAuth"`
		HeaderName  string             `json:"headerName"`
		MCPURL      string             `json:"mcpUrl"`
		AuthMode    connector.AuthMode `json:"authMode"`
	}{AuthMode: connector.AuthMCP}
	if readErr != nil || connector.DecodeJSON(data, &req) != nil || queryinput.DecodeStrictJSON(data, &req) != nil {
		s.writeConnectorError(w, errors.New("invalid MCP import request"))
		return
	}
	prepared, err := adminsource.PrepareMCPImport(r.Context(), s.connectorSources(), req.MCPURL, adminsource.MCPImportOptions{AuthMode: req.AuthMode, Name: req.Name, ID: req.ID, Description: req.Description, IconPNG: req.IconPNG, IconPreset: req.IconPreset, TokenAuth: req.TokenAuth, HeaderName: req.HeaderName})
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	defer prepared.Close()
	pkg, err := s.publishPreparedConnector(r.Context(), prepared, false)
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeConnectorInstall(w, pkg)
}
