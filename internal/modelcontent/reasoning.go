package modelcontent

import "encoding/json"

// ReasoningPart is the persisted representation of provider reasoning. Encrypted
// text is opaque protocol state, never display text or a tool instruction.
type ReasoningPart struct {
	Type          string          `json:"type"`
	Text          string          `json:"text,omitempty"`
	EncryptedText string          `json:"encrypted_text,omitempty"`
	ID            string          `json:"id,omitempty"`
	Summary       json.RawMessage `json:"summary,omitempty"`
}

func EncryptedReasoningParts(value any) []ReasoningPart {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var parts []ReasoningPart
	if json.Unmarshal(data, &parts) != nil {
		return nil
	}
	var result []ReasoningPart
	for _, part := range parts {
		if part.Type == "encrypted_text" && part.EncryptedText != "" {
			result = append(result, part)
		}
	}
	return result
}

func ReasoningPartsValue(text string, encrypted []ReasoningPart) any {
	if len(encrypted) == 0 {
		return text
	}
	parts := make([]ReasoningPart, 0, len(encrypted)+1)
	if text != "" {
		parts = append(parts, ReasoningPart{Type: "text", Text: text})
	}
	parts = append(parts, encrypted...)
	data, _ := json.Marshal(parts)
	var value []any
	_ = json.Unmarshal(data, &value)
	return value
}

// MarshalJSON retains the historical text-part shape, while keeping ciphertext
// out of the text property even when the readable text is empty.
func (p ReasoningPart) MarshalJSON() ([]byte, error) {
	if p.Type != "encrypted_text" {
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{p.Type, p.Text})
	}
	type plain ReasoningPart
	p.Text = ""
	return json.Marshal(plain(p))
}
