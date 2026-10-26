package view

import (
	"encoding/json"
	"fmt"
)

// Action is an explicit viewer write operation, separate from read queries.
// StdinParam optionally encodes one validated object as CLI stdin. No browser
// supplied file paths, shell strings, URLs or environment are accepted.
type Action struct {
	Query
	StdinParam string `json:"stdinParam,omitempty"`
}

func validateActions(key string, d Definition) error {
	if len(d.Actions) == 0 {
		return nil
	}
	queries := make(map[string]Query, len(d.Actions))
	for name, action := range d.Actions {
		if action.Adapter != "cli" && action.Adapter != "mcp" {
			return fmt.Errorf("%w: action %s requires cli or mcp", ErrInvalid, name)
		}
		if len(action.ParamsSchema) == 0 {
			return fmt.Errorf("%w: action %s requires paramsSchema", ErrInvalid, name)
		}
		var schema struct {
			Type                 string                     `json:"type"`
			AdditionalProperties *bool                      `json:"additionalProperties"`
			Properties           map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(action.ParamsSchema, &schema) != nil || schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			return fmt.Errorf("%w: action %s requires a closed object schema", ErrInvalid, name)
		}
		if action.StdinParam != "" {
			var property struct {
				Type string `json:"type"`
			}
			if action.Adapter != "cli" || !paramPattern.MatchString(action.StdinParam) || json.Unmarshal(schema.Properties[action.StdinParam], &property) != nil || property.Type != "object" {
				return fmt.Errorf("%w: invalid stdinParam of action %s", ErrInvalid, name)
			}
		}
		queries[name] = action.Query
	}
	return validateQueries(key, Definition{Usage: d.Usage, Queries: queries})
}

func ValidateAction(actions map[string]Action, name string, params json.RawMessage) (Action, error) {
	action, ok := actions[name]
	if !ok {
		return Action{}, fmt.Errorf("%w: action is not declared", ErrInvalid)
	}
	_, err := ValidateQuery(map[string]Query{name: action.Query}, name, params)
	return action, err
}
