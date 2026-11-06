package queryinput

type SendRequest struct {
	RequestID    string             `json:"requestId"`
	ChatID       string             `json:"chatId,omitempty"`
	AgentKey     string             `json:"agentKey,omitempty"`
	Primary      bool               `json:"primary,omitempty"`
	Message      string             `json:"message"`
	References   []Reference        `json:"references,omitempty"`
	PlanningMode *bool              `json:"planningMode,omitempty"`
	EditingMode  *bool              `json:"editingMode,omitempty"`
	Model        *QueryModelOptions `json:"model,omitempty"`
	AccessLevel  string             `json:"accessLevel,omitempty"`
}

func (r *SendRequest) UnmarshalJSON(data []byte) error {
	type plain SendRequest
	var value plain
	if err := DecodeStrictJSON(data, &value); err != nil {
		return err
	}
	*r = SendRequest(value)
	return nil
}
