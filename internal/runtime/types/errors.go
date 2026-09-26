package types

// RequestError preserves the application rejection code and public details.
// Transports map Status to their own error envelope.
type RequestError struct {
	Status  int
	Code    string
	Message string
	Data    any
}

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
