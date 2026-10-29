package view

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Action is an explicit viewer write operation, separate from read queries.
// StdinParam optionally encodes one validated object as CLI stdin. No browser
// supplied file paths, shell strings, URLs or environment are accepted.
//
// Intercept also routes an Agent's direct Bash call of the same command to
// the view's form: the params read back from argv are reviewed there, and
// the command is rebuilt from the submitted params.
type Action struct {
	Query
	StdinParam string `json:"stdinParam,omitempty"`
	Intercept  bool   `json:"intercept,omitempty"`
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
		if action.Intercept {
			if err := validateIntercept(d, action); err != nil {
				return fmt.Errorf("%w: intercept action %s: %v", ErrInvalid, name, err)
			}
		}
		queries[name] = action.Query
	}
	return validateQueries(key, Definition{Usage: d.Usage, Queries: queries})
}

// A Bash call has no stdin to review, and argv must parse back unambiguously:
// the subcommand path comes first, positional params after it.
func validateIntercept(d Definition, action Action) error {
	if action.Adapter != "cli" || action.StdinParam != "" || !d.Supports("form") {
		return fmt.Errorf("requires cli, no stdinParam and a view with form usage")
	}
	if len(InterceptPath(action.Args)) == 0 {
		return fmt.Errorf("args need a subcommand literal")
	}
	positional := false
	for _, arg := range action.Args {
		switch {
		case arg.Param != "" && arg.Opt == "" && arg.Flag == "":
			positional = true
		case arg.Param == "" && positional && InterceptPath([]QueryArg{arg}) != nil:
			return fmt.Errorf("subcommand literal %q follows a positional param", arg.Literal)
		}
	}
	return nil
}

// validateIntercepts keeps each intercepted subcommand path unique within one
// connector, so a call reaches exactly one review.
func validateIntercepts(definitions map[string]Definition) error {
	seen := map[string]string{}
	for key, d := range definitions {
		for name, action := range d.Actions {
			if !action.Intercept {
				continue
			}
			path := strings.Join(InterceptPath(action.Args), " ")
			if other, ok := seen[path]; ok {
				return fmt.Errorf("%w: intercept path %q is declared by %s and %s/%s", ErrInvalid, path, other, key, name)
			}
			seen[path] = key + "/" + name
		}
	}
	return nil
}

// InterceptAction is the form review an Agent's direct call of argv belongs
// to: the declaring view and action, with params read back from argv.
type InterceptAction struct {
	ViewKey string
	Name    string
	Action  Action
	Params  map[string]any
	// Err is set when argv reaches the path but cannot be read back or fails
	// the schema; such a call must not run unreviewed. Missing required
	// params are not an error: the person completes them in the form.
	Err error
}

// FindIntercept returns the intercept action of the connector's views whose
// subcommand path appears in argv, or nil when the call is not intercepted.
// The longest path wins; two different paths of the same length both
// matching is an error rather than a guess.
func FindIntercept(definitions map[string]Definition, argv []string) *InterceptAction {
	var found *InterceptAction
	longest, ambiguous := 0, false
	for key, d := range definitions {
		for name, action := range d.Actions {
			path := InterceptPath(action.Args)
			if !action.Intercept || !ContainsPath(argv, path) || len(path) < longest {
				continue
			}
			ambiguous = len(path) == longest
			longest = len(path)
			found = &InterceptAction{ViewKey: key, Name: name, Action: action}
		}
	}
	if found == nil {
		return nil
	}
	if ambiguous {
		found.Err = fmt.Errorf("%w: the command matches more than one reviewed action", ErrDataInvalid)
		return found
	}
	found.Params, found.Err = ParseCLIArgs(found.Action.Args, found.Action.ParamsSchema, argv)
	if found.Err == nil {
		draft := found.Action
		draft.ParamsSchema = withoutRequired(draft.ParamsSchema)
		found.Err = ValidateParams(draft, found.Params)
	}
	return found
}

// withoutRequired drops the top-level required list, so a review can start
// from a partial call; the submitted params meet the full schema.
func withoutRequired(schema json.RawMessage) json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(schema, &object) != nil {
		return schema
	}
	delete(object, "required")
	raw, err := json.Marshal(object)
	if err != nil {
		return schema
	}
	return raw
}

// ValidateParams checks params against the action schema.
func ValidateParams(action Action, params map[string]any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("%w: params", ErrDataInvalid)
	}
	_, err = ValidateAction(map[string]Action{"action": action}, "action", raw)
	return err
}

func ValidateAction(actions map[string]Action, name string, params json.RawMessage) (Action, error) {
	action, ok := actions[name]
	if !ok {
		return Action{}, fmt.Errorf("%w: action is not declared", ErrInvalid)
	}
	_, err := ValidateQuery(map[string]Query{name: action.Query}, name, params)
	return action, err
}
