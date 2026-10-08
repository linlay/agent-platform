package knowledge

// RuntimeState preserves the public sidecar JSON contract for the managed CLI.
type RuntimeState struct {
	Engine          string `json:"engine,omitempty"`
	Available       bool   `json:"available"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	EngineVersion   string `json:"engineVersion,omitempty"`
	LanceDBVersion  string `json:"lancedbVersion,omitempty"`
	LastError       string `json:"lastError,omitempty"`
}
