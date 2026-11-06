package queryinput

import "encoding/json"

// MailInput is trusted input provenance, never decoded from public Query or
// Steer requests. Attempt fences callbacks from an earlier delivery attempt.
type MailInput struct {
	NewChat    bool   `json:"mailNewChat"`
	OriginType string `json:"mailOrigin,omitempty"`
	Header     string `json:"mailHeader,omitempty"`
	ID         string `json:"mailId"`
	Kind       string `json:"mailKind"`
	Attempt    int    `json:"mailAttempt"`
	Hop        int    `json:"mailHop"`
	Outcome    string `json:"outcome,omitempty"`
}

func (m *MailInput) Payload(p map[string]any) {
	if m == nil {
		return
	}
	p["mailOrigin"] = m.OriginType
	p["mailHeader"] = m.Header
	if m.OriginType == "agent" {
		p["mailNewChat"] = m.NewChat
	}
	p["mailId"], p["mailKind"], p["mailAttempt"], p["mailHop"] = m.ID, m.Kind, m.Attempt, m.Hop
	if m.Outcome != "" {
		p["outcome"] = m.Outcome
	}
}

func MailFromPayload(p map[string]any) *MailInput {
	id, _ := p["mailId"].(string)
	if id == "" {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	var m MailInput
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}
