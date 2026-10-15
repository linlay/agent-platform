package api

type AgentConnectorsResponse struct {
	AgentKey      string   `json:"agentKey"`
	ConnectorIDs  []string `json:"connectorIds"`
	ReloadPending bool     `json:"reloadPending"`
}

type ConnectorOption struct {
	ID                    string               `json:"id"`
	Name                  string               `json:"name"`
	Description           string               `json:"description,omitempty"`
	IconURL               string               `json:"iconUrl,omitempty"`
	MutuallyExclusiveWith []string             `json:"mutuallyExclusiveWith,omitempty"`
	Readiness             string               `json:"readiness"`
	MCP                   []ConnectorMCPStatus `json:"mcp,omitempty"`
}

// Public local runtime snapshots; no credential/session or management metadata.
type ConnectorMCPStatus struct {
	AgentKey  string `json:"agentKey,omitempty"`
	ServerKey string `json:"serverKey"`
	Status    string `json:"status"`
	ToolCount int    `json:"toolCount"`
}

type ConnectorOptionsResponse struct {
	Connectors    []ConnectorOption `json:"connectors"`
	AgentKey      string            `json:"agentKey,omitempty"`
	ReloadPending *bool             `json:"reloadPending,omitempty"`
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
