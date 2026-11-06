package acp

import (
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/contracts/queryinput"
	"encoding/json"
	"fmt"
)

func validateQuestionSubmit(questions []any, req queryinput.SubmitRequest) error {
	if req.Param != nil || req.Params == nil {
		return fmt.Errorf("invalid_question_answer")
	}
	if len(req.Params) == 0 {
		return nil
	}
	definitions := map[string]map[string]any{}
	for _, raw := range questions {
		q, _ := raw.(map[string]any)
		id, _ := q["id"].(string)
		definitions[id] = q
	}
	seen := map[string]bool{}
	for _, raw := range req.Params {
		if dto.Validate("QuestionAnswer", raw) != nil {
			return fmt.Errorf("invalid_question_answer")
		}
		var value struct{ ID, Answer string }
		json.Unmarshal(raw, &value)
		q := definitions[value.ID]
		if q == nil || seen[value.ID] {
			return fmt.Errorf("invalid_question_answer")
		}
		seen[value.ID] = true
		if q["type"] == "select" {
			found := false
			options, _ := q["options"].([]any)
			for _, raw := range options {
				option, _ := raw.(map[string]any)
				if option["id"] == value.Answer {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("invalid_question_answer")
			}
		}
	}
	for id, q := range definitions {
		if q["required"] == true && !seen[id] {
			return fmt.Errorf("invalid_question_answer")
		}
	}
	return nil
}
