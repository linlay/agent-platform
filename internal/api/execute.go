package api

import "encoding/json"

// ExecuteRequest is the body of POST /api/execute, or its "request" part when
// the call carries files as multipart/form-data.
type ExecuteRequest struct {
	RequestID string `json:"requestId,omitempty"`
	AgentKey  string `json:"agentKey"`
	Message   string `json:"message"`
	// Input is task data of any JSON shape. {"$file": "<part>"} names a file
	// part of the same multipart request.
	Input json.RawMessage `json:"input,omitempty"`
	// OutputSchema is the JSON Schema of the success output; its root is an
	// object.
	OutputSchema map[string]any `json:"outputSchema"`
	AccessLevel  string         `json:"accessLevel,omitempty"`
}
