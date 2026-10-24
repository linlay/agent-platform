package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	// operationPattern names a Platform read operation such as automation.list.
	operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
	paramPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	optionPattern    = regexp.MustCompile(`^-{1,2}[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

const (
	maxQueryArgs        = 64
	maxQueryNameRunes   = 256
	maxQueryLiteralSize = 1024
)

// Query declares one read operation of a display view. Every adapter shares
// the same Markdown syntax, host bridge and result shape; only the data source
// differs:
//   - native: a Platform operation registered for the declaring native connector;
//   - cli: the declaring package's own CLI, argv built from Args without a shell;
//   - mcp: a tool of one of the declaring package's own MCP components, called
//     with the fixed Arguments plus the validated params.
//
// Queries are declared read-only by the package author; the Platform cannot
// prove that a CLI or MCP tool has no side effects.
type Query struct {
	Description  string          `json:"description,omitempty"`
	Adapter      string          `json:"adapter"`
	Operation    string          `json:"operation,omitempty"`
	Args         []QueryArg      `json:"args,omitempty"`
	Component    string          `json:"component,omitempty"`
	ToolName     string          `json:"toolName,omitempty"`
	Arguments    map[string]any  `json:"arguments,omitempty"`
	ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`
}

// QueryArg is one CLI argv item: a literal string, or an object that inserts
// a param as a positional value ({param}), an option with a value
// ({opt, param}) or a flag present only when the param is true ({flag, param}).
// An omitted or null param drops the whole item.
type QueryArg struct {
	Literal string `json:"-"`
	Param   string `json:"param,omitempty"`
	Opt     string `json:"opt,omitempty"`
	Flag    string `json:"flag,omitempty"`
}

func (a QueryArg) MarshalJSON() ([]byte, error) {
	if a.Param == "" {
		return json.Marshal(a.Literal)
	}
	type object QueryArg
	return json.Marshal(object(a))
}

func (a *QueryArg) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		*a = QueryArg{}
		return json.Unmarshal(data, &a.Literal)
	}
	type object QueryArg
	var value object
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("query arg must be a string or {param, opt|flag}: %w", err)
	}
	if value.Param == "" {
		return fmt.Errorf("query arg object requires param")
	}
	*a = QueryArg(value)
	return nil
}

func validateQueries(key string, d Definition) error {
	if len(d.Queries) > 0 && !d.Supports("display") {
		return fmt.Errorf("%w: queries for %s require display usage", ErrInvalid, key)
	}
	for name, q := range d.Queries {
		if !idPattern.MatchString(name) {
			return fmt.Errorf("%w: query name %q of %s", ErrInvalid, name, key)
		}
		if utf8.RuneCountInString(strings.TrimSpace(q.Description)) > MaxDescriptionRunes {
			return fmt.Errorf("%w: query %s of %s description is too long", ErrInvalid, name, key)
		}
		if err := validateQueryAdapter(q); err != nil {
			return fmt.Errorf("%w: query %s of %s: %v", ErrInvalid, name, key, err)
		}
		if len(q.ParamsSchema) > 0 {
			if _, err := compileDataSchema(q.ParamsSchema); err != nil {
				return fmt.Errorf("%w: paramsSchema of query %s of %s: %v", ErrInvalid, name, key, err)
			}
		}
		if err := validateFixedArguments(q); err != nil {
			return fmt.Errorf("%w: query %s of %s: %v", ErrInvalid, name, key, err)
		}
	}
	return nil
}

func validateQueryAdapter(q Query) error {
	native := q.Operation != ""
	cli := q.Args != nil
	mcp := q.Component != "" || q.ToolName != "" || q.Arguments != nil
	switch q.Adapter {
	case "native":
		if !operationPattern.MatchString(q.Operation) || cli || mcp {
			return fmt.Errorf("adapter native requires only operation")
		}
	case "cli":
		if native || mcp || len(q.Args) == 0 || len(q.Args) > maxQueryArgs {
			return fmt.Errorf("adapter cli requires only args (1..%d items)", maxQueryArgs)
		}
		for _, arg := range q.Args {
			if err := validateQueryArg(arg); err != nil {
				return err
			}
		}
	case "mcp":
		if native || cli || strings.TrimSpace(q.Component) == "" || strings.TrimSpace(q.ToolName) == "" ||
			utf8.RuneCountInString(q.Component) > maxQueryNameRunes || utf8.RuneCountInString(q.ToolName) > maxQueryNameRunes {
			return fmt.Errorf("adapter mcp requires component and toolName, and optional arguments")
		}
	default:
		return fmt.Errorf("adapter must be native, cli or mcp")
	}
	return nil
}

func validateQueryArg(arg QueryArg) error {
	if arg.Param == "" {
		if arg.Literal == "" || len(arg.Literal) > maxQueryLiteralSize || strings.ContainsRune(arg.Literal, 0) {
			return fmt.Errorf("literal args must be non-empty strings without NUL")
		}
		return nil
	}
	if !paramPattern.MatchString(arg.Param) {
		return fmt.Errorf("arg param %q", arg.Param)
	}
	if arg.Opt != "" && arg.Flag != "" {
		return fmt.Errorf("arg %s uses both opt and flag", arg.Param)
	}
	if option := arg.Opt + arg.Flag; option != "" && !optionPattern.MatchString(option) {
		return fmt.Errorf("arg %s option %q must look like -x or --name", arg.Param, option)
	}
	return nil
}

// validateFixedArguments rejects MCP params that could replace a fixed
// argument; the same check runs again on each call.
func validateFixedArguments(q Query) error {
	if len(q.Arguments) == 0 || len(q.ParamsSchema) == 0 {
		return nil
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(q.ParamsSchema, &schema); err != nil {
		return nil
	}
	for name := range q.Arguments {
		if _, ok := schema.Properties[name]; ok {
			return fmt.Errorf("param %s would override a fixed argument", name)
		}
	}
	return nil
}
