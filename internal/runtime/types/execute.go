package types

import "encoding/json"

// ExecuteCommand is one /api/execute call: a task message, its data and the
// caller's output format, run once in a new or caller-selected Chat with an
// accepted result_submit.
type ExecuteCommand struct {
	// OriginalQuery is the caller's request JSON, persisted verbatim.
	OriginalQuery json.RawMessage

	RequestID    string
	ChatID       string
	References   []Reference
	AgentKey     string
	Message      string
	AccessLevel  string
	Input        json.RawMessage
	OutputSchema map[string]any

	// FileParts names the files the transport received beside the request.
	// Input refers to one with {"$file": "<part>"}; every part must be used.
	FileParts []string
	// StoreFiles stores those files in the new Chat the way uploads are stored
	// and returns the @chat path of each part. Runtime calls it once, after
	// the request is valid and before the Run starts.
	StoreFiles func(chatID string) (map[string]string, error)

	// OnPrepared reserves the generated identity in a trusted caller's ownership
	// ledger before registration or model execution. A failure aborts admission.
	// This hook is internal; transports never decode it from request JSON.
	OnPrepared func(RunRef) error `json:"-"`

	Locale          string
	ResourceBaseURL string
	ChatSource      string
	Caller          Caller
}

// ExecuteResult is the accepted result_submit of the Run.
type ExecuteResult struct {
	ChatID string         `json:"chatId"`
	Status string         `json:"status"`
	Output map[string]any `json:"output,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

// A valid empty object is still a required success output; omitempty would lose it.
func (r ExecuteResult) MarshalJSON() ([]byte, error) {
	if r.Status == "success" {
		return json.Marshal(struct {
			ChatID string         `json:"chatId"`
			Status string         `json:"status"`
			Output map[string]any `json:"output"`
		}{r.ChatID, r.Status, r.Output})
	}
	type result ExecuteResult
	return json.Marshal(result(r))
}
