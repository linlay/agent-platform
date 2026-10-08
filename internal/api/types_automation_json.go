package api

import (
	"bytes"
	"encoding/json"
)

func (r *UpdateAutomationRequest) UnmarshalJSON(data []byte) error {
	type plain UpdateAutomationRequest
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, decoded.RemainingRunsSet = fields["remainingRuns"]
	*r = UpdateAutomationRequest(decoded)
	return nil
}

func (r UpdateAutomationRequest) MarshalJSON() ([]byte, error) {
	type plain UpdateAutomationRequest
	if r.RemainingRunsSet && r.RemainingRuns == nil {
		return json.Marshal(struct {
			plain
			RemainingRuns *int `json:"remainingRuns"`
		}{plain: plain(r)})
	}
	return json.Marshal(plain(r))
}
