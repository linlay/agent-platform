package queryinput

import "encoding/json"

// MailInput is trusted input provenance, never decoded from public Query or
// Steer requests. Attempt fences callbacks from an earlier delivery attempt.
type MailInput struct {
	ID      string `json:"mailId"`
	Kind    string `json:"mailKind"`
	Attempt int    `json:"mailAttempt"`
	Hop     int    `json:"mailHop"`
	Outcome string `json:"outcome,omitempty"`
}

func (m *MailInput) Payload(p map[string]any) {
	if m == nil {
		return
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
