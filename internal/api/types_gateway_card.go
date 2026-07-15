package api

// GatewayAgentCard is the deliberately small, gateway-facing discovery card.
// It is A2A-inspired, but it is not the complete A2A AgentCard schema.
type GatewayAgentCard struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Skills      []GatewayAgentCardFeature `json:"skills"`
	Tools       []GatewayAgentCardFeature `json:"tools"`
}

type GatewayAgentCardFeature struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

type GatewayAgentCardUpdatePayload struct {
	AgentKey  string           `json:"agentKey"`
	AgentCard GatewayAgentCard `json:"agentCard"`
}

type GatewayAgentCardAck struct {
	AgentKey string `json:"agentKey"`
	Accepted *bool  `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

type GatewayAgentCardReportStatus struct {
	Status     string `json:"status"`
	RequestID  string `json:"requestId,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	UpdatedAt  int64  `json:"updatedAt,omitempty"`
	AcceptedAt int64  `json:"acceptedAt,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
