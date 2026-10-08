package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/ws"
)

func (s *Server) wsConnectors(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	var payload struct {
		AgentKey string `json:"agentKey"`
	}
	if _, err := decodeConnectorUsagePayload(req, &payload); err != nil {
		s.sendAgentWSError(conn, req, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, err := s.listSelectableConnectors(payload.AgentKey, conn.Locale())
	s.sendAgentWSResponse(conn, req, response, err)
}

func (s *Server) wsAgentConnectors(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	var payload api.SetAgentConnectorRequest
	fields, err := decodeConnectorUsagePayload(req, &payload)
	if err != nil {
		s.sendAgentWSError(conn, req, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	// Presence selects mutation, so null or incomplete writes cannot become reads.
	_, connectorID := fields["connectorId"]
	_, enabled := fields["enabled"]
	state, err := s.agentConnectorSelection(ctx, payload, connectorID || enabled)
	if err != nil {
		s.sendAgentWSError(conn, req, err)
		return
	}
	s.sendAgentWSResponse(conn, req, agentConnectorUsageResponse(state), nil)
}

func decodeConnectorUsagePayload(req ws.RequestFrame, target any) (map[string]json.RawMessage, error) {
	payload := req.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("payload must be an object")
	}
	// encoding/json accepts case-insensitive struct field names. Wire fields are
	// canonical camelCase, including when identifying reads versus mutations.
	for key := range fields {
		if key != "agentKey" && key != "connectorId" && key != "enabled" {
			return nil, errors.New("unknown payload field")
		}
	}
	return fields, nil
}
