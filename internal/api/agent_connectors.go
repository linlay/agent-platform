package api

type AgentConnectorsResponse struct {
	AgentKey           string   `json:"agentKey"`
	ConnectorIDs       []string `json:"connectorIds"`
	ActiveConnectorIDs []string `json:"activeConnectorIds"`
	ReloadPending      bool     `json:"reloadPending"`
}

type SetAgentConnectorRequest struct {
	AgentKey    string `json:"agentKey"`
	ConnectorID string `json:"connectorId"`
	Enabled     *bool  `json:"enabled"`
}
