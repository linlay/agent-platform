package api

// GatewayAgentRegistration is the v1 channel-export registration contract.
// It deliberately contains discovery and routing fields only; skills, tools,
// prompts, workspace details, and runtime configuration are not exported.
type GatewayAgentRegistration struct {
	AgentKey     string   `json:"agentKey"`
	Name         string   `json:"name"`
	Role         string   `json:"role,omitempty"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities"`
}

type GatewayAgentRegistrationSupport struct {
	Version                     string   `json:"version"`
	MaxAgentsPerPlatformChannel int      `json:"maxAgentsPerPlatformChannel"`
	SupportedCapabilities       []string `json:"supportedCapabilities"`
}

type GatewayAgentConnectedData struct {
	SessionID         string                          `json:"sessionId"`
	PlatformKey       string                          `json:"platformKey"`
	CredentialType    string                          `json:"credentialType,omitempty"`
	RegistrationMode  string                          `json:"registrationMode"`
	Principal         string                          `json:"principal,omitempty"`
	Status            string                          `json:"status,omitempty"`
	AgentRegistration GatewayAgentRegistrationSupport `json:"agentRegistration"`
	Timestamp         int64                           `json:"timestamp,omitempty"`
}

type GatewayAgentRegisterResult struct {
	Accepted       *bool    `json:"accepted"`
	AgentKey       string   `json:"agentKey"`
	RegistrationID string   `json:"registrationId"`
	Status         string   `json:"status"`
	Capabilities   []string `json:"capabilities"`
	RegisteredAt   int64    `json:"registeredAt"`
	UpdatedAt      int64    `json:"updatedAt"`
	ErrorCode      string   `json:"errorCode,omitempty"`
	Message        string   `json:"message,omitempty"`
}

type GatewayAgentUnregisterPayload struct {
	AgentKey string `json:"agentKey"`
}

type GatewayAgentUnregisterResult struct {
	Accepted         *bool  `json:"accepted"`
	AgentKey         string `json:"agentKey"`
	Status           string `json:"status"`
	DrainingRunCount int    `json:"drainingRunCount,omitempty"`
	ErrorCode        string `json:"errorCode,omitempty"`
	Message          string `json:"message,omitempty"`
}

type GatewayRegisteredAgent struct {
	GatewayAgentRegistration
	OwnedByCurrentSession bool  `json:"ownedByCurrentSession"`
	RegisteredAt          int64 `json:"registeredAt"`
	UpdatedAt             int64 `json:"updatedAt"`
}

type GatewayAgentListResult struct {
	PlatformKey              string                   `json:"platformKey"`
	Count                    int                      `json:"count"`
	CurrentSessionOwnedCount int                      `json:"currentSessionOwnedCount"`
	Agents                   []GatewayRegisteredAgent `json:"agents"`
}

type GatewayAgentCardReportStatus struct {
	Status     string `json:"status"`
	RequestID  string `json:"requestId,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	UpdatedAt  int64  `json:"updatedAt,omitempty"`
	AcceptedAt int64  `json:"acceptedAt,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
