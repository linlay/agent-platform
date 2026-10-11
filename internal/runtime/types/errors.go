package types

// RequestError preserves the application rejection code and public details.
// Transports map Status to their own error envelope.
type RequestError struct {
	Status  int
	Code    string
	Message string
	Data    any
	// OutcomeUnknown is internal transport metadata, never a public request
	// field. A control request may have reached the upstream without an ack.
	OutcomeUnknown bool `json:"-"`
}

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
