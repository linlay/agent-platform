package api

type AgentConnectorsResponse struct {
	AgentKey      string   `json:"agentKey"`
	ConnectorIDs  []string `json:"connectorIds"`
	ReloadPending bool     `json:"reloadPending"`
}

type ConnectorOption struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Description           string   `json:"description,omitempty"`
	IconURL               string   `json:"iconUrl,omitempty"`
	MutuallyExclusiveWith []string `json:"mutuallyExclusiveWith,omitempty"`
}

type ConnectorOptionsResponse struct {
	Connectors []ConnectorOption `json:"connectors"`
}

type AdminAgentConnectorsResponse struct {
	AgentKey             string   `json:"agentKey"`
	ConnectorIDs         []string `json:"connectorIds"`
	PresetConnectorIDs   []string `json:"presetConnectorIds"`
	DeclaredConnectorIDs []string `json:"declaredConnectorIds"`
	ActiveConnectorIDs   []string `json:"activeConnectorIds"`
	ReloadPending        bool     `json:"reloadPending"`
}

type SetAgentConnectorRequest struct {
	AgentKey    string `json:"agentKey"`
	ConnectorID string `json:"connectorId"`
	Enabled     *bool  `json:"enabled"`
}
