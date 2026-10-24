// Package viewquery runs the read queries a rendered display view declares.
//
// A query is a viewer action, like opening a management page: it is authorized
// for the viewing principal, never as an Agent tool call, and needs no Run
// execution context. Native, CLI and MCP queries share validation, result
// shape and limits; only the data source differs. The caller supplies the Run
// that published the view so Agent-scoped operations remain possible; current
// operations only record it.
package viewquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/view"
)

// MaxResultBytes bounds one serialized query result.
const MaxResultBytes = 1 << 20

const maxFailureRunes = 300

var (
	// ErrUnavailable reports an operation this Platform does not provide.
	ErrUnavailable = errors.New("view query unavailable")
	// ErrFailed wraps an operation's own failure; its message is user-safe.
	ErrFailed = errors.New("view query failed")
)

// Context identifies the viewer and the Run whose content published the view.
type Context struct {
	ChatID   string
	RunID    string
	AgentKey string
}

// Result is one query outcome; ExecutedAt is Unix milliseconds.
type Result struct {
	Data       json.RawMessage
	ExecutedAt int64
}

// Operation is one registered native read operation. Params already satisfy
// the view's paramsSchema; operations still validate their own fields.
type Operation func(ctx context.Context, qc Context, params map[string]any) (any, error)

// ConnectorInvoker runs an installed connector's CLI or MCP component. It
// checks installation, configuration, authorization and preparation itself.
type ConnectorInvoker interface {
	Invoke(ctx context.Context, scope connectorops.Scope, req connectorops.Request) (connectorops.Result, error)
}

type Service struct {
	Operations map[string]Operation
	Connectors ConnectorInvoker
	Now        func() time.Time
}

// Execute runs a query declared by a frozen snapshot document. CLI and MCP
// queries run against the currently installed connector: the snapshot freezes
// the declaration, not the connector.
func (s *Service) Execute(ctx context.Context, doc view.Document, name string, params json.RawMessage, qc Context) (Result, error) {
	if s == nil {
		return Result{}, ErrUnavailable
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	query, err := view.ValidateQuery(doc.Queries, name, params)
	if err != nil {
		if errors.Is(err, view.ErrInvalid) {
			return Result{}, fmt.Errorf("%w: %v", view.ErrNotFound, err)
		}
		return Result{}, err
	}
	var data json.RawMessage
	switch query.Adapter {
	case "native":
		data, err = s.native(ctx, doc, query, params, qc)
	case "cli", "mcp":
		data, err = s.connector(ctx, doc, query, params, qc)
	default:
		err = ErrUnavailable
	}
	if err != nil {
		return Result{}, err
	}
	if len(data) > MaxResultBytes {
		return Result{}, fmt.Errorf("%w: result exceeds %d bytes", ErrFailed, MaxResultBytes)
	}
	return Result{Data: data, ExecutedAt: s.now().UnixMilli()}, nil
}

func (s *Service) native(ctx context.Context, doc view.Document, query view.Query, params json.RawMessage, qc Context) (json.RawMessage, error) {
	// Snapshots are validated at publication, but the owner is checked again
	// so a document can never reach another connector's operation.
	owner, registered := connector.NativeViewOperationOwner(query.Operation)
	operation := s.Operations[query.Operation]
	if !registered || owner != doc.View.ConnectorID || operation == nil {
		return nil, ErrUnavailable
	}
	var values map[string]any
	if err := json.Unmarshal(params, &values); err != nil {
		return nil, fmt.Errorf("%w: params", view.ErrDataInvalid)
	}
	result, err := operation(ctx, qc, values)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFailed, err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("%w: result is not JSON", ErrFailed)
	}
	return data, nil
}

func (s *Service) connector(ctx context.Context, doc view.Document, query view.Query, params json.RawMessage, qc Context) (json.RawMessage, error) {
	if s.Connectors == nil || doc.View.ConnectorID == "" {
		return nil, ErrUnavailable
	}
	values, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	req := connectorops.Request{ConnectorID: doc.View.ConnectorID, Adapter: query.Adapter}
	if query.Adapter == "cli" {
		if req.Args, err = CLIArgs(query.Args, values); err != nil {
			return nil, err
		}
	} else {
		if req.Arguments, err = MCPArguments(query.Arguments, values); err != nil {
			return nil, err
		}
		req.Component, req.ToolName = query.Component, query.ToolName
	}
	subject := "view"
	if qc.ChatID != "" {
		subject += ":" + qc.ChatID
	}
	// The grant covers exactly the declaring connector and this adapter.
	scope := connectorops.Scope{
		Subject:   subject,
		Execution: []connectorops.Permission{{ConnectorID: req.ConnectorID, Adapter: req.Adapter}},
		Check:     func() error { return nil },
	}
	result, err := s.Connectors.Invoke(ctx, scope, req)
	if err != nil {
		return nil, err
	}
	if query.Adapter == "cli" {
		return cliOutput(result)
	}
	return mcpOutput(result.MCP)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// decodeParams keeps numbers as written so CLI argv receives "20", not "2e+01".
func decodeParams(params json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, fmt.Errorf("%w: params", view.ErrDataInvalid)
	}
	return values, nil
}

// CLIArgs builds argv from the declared items. Values are passed as separate
// argv entries, never through a shell. A missing or null param drops its item.
func CLIArgs(items []view.QueryArg, params map[string]any) ([]string, error) {
	argv := make([]string, 0, len(items))
	for _, item := range items {
		if item.Param == "" {
			argv = append(argv, item.Literal)
			continue
		}
		value, present := params[item.Param]
		if !present || value == nil {
			continue
		}
		if item.Flag != "" {
			enabled, ok := value.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: params.%s must be a boolean", view.ErrDataInvalid, item.Param)
			}
			if enabled {
				argv = append(argv, item.Flag)
			}
			continue
		}
		text, err := scalarText(item.Param, value)
		if err != nil {
			return nil, err
		}
		if item.Opt != "" {
			argv = append(argv, item.Opt, text)
			continue
		}
		// A positional value must not be read as an option.
		if strings.HasPrefix(text, "-") {
			return nil, fmt.Errorf("%w: params.%s must not start with '-'", view.ErrDataInvalid, item.Param)
		}
		argv = append(argv, text)
	}
	return argv, nil
}

func scalarText(name string, value any) (string, error) {
	switch v := value.(type) {
	case string:
		if strings.ContainsRune(v, 0) {
			return "", fmt.Errorf("%w: params.%s contains NUL", view.ErrDataInvalid, name)
		}
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("%w: params.%s must be a string, number or boolean", view.ErrDataInvalid, name)
}

// MCPArguments merges the fixed arguments with params; params cannot replace
// a fixed argument.
func MCPArguments(fixed, params map[string]any) (map[string]any, error) {
	arguments := make(map[string]any, len(fixed)+len(params))
	for key, value := range fixed {
		arguments[key] = value
	}
	for key, value := range params {
		if _, ok := fixed[key]; ok {
			return nil, fmt.Errorf("%w: params.%s is a fixed argument", view.ErrDataInvalid, key)
		}
		arguments[key] = value
	}
	return arguments, nil
}

// cliOutput requires a successful exit and JSON stdout.
func cliOutput(result connectorops.Result) (json.RawMessage, error) {
	if result.ExitCode != 0 {
		message := firstLine(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return nil, fmt.Errorf("%w: %s", ErrFailed, message)
	}
	output := bytes.TrimSpace([]byte(result.Stdout))
	if len(output) == 0 || !json.Valid(output) {
		return nil, fmt.Errorf("%w: CLI output is not JSON", ErrFailed)
	}
	return output, nil
}

// mcpOutput prefers structuredContent, then a text result that is JSON, and
// otherwise wraps the text as {"text": …}.
func mcpOutput(raw json.RawMessage) (json.RawMessage, error) {
	var result struct {
		Content           []struct{ Type, Text string } `json:"content"`
		StructuredContent json.RawMessage               `json:"structuredContent"`
		IsError           bool                          `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%w: MCP result is malformed", ErrFailed)
	}
	texts := []string{}
	for _, item := range result.Content {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if result.IsError {
		message := firstLine(text)
		if message == "" {
			message = "MCP tool reported an error"
		}
		return nil, fmt.Errorf("%w: %s", ErrFailed, message)
	}
	if structured := bytes.TrimSpace(result.StructuredContent); len(structured) > 0 && string(structured) != "null" {
		return structured, nil
	}
	if trimmed := bytes.TrimSpace([]byte(text)); len(texts) == 1 && json.Valid(trimmed) && len(trimmed) > 0 {
		return trimmed, nil
	}
	data, _ := json.Marshal(map[string]string{"text": text})
	return data, nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > maxFailureRunes {
		line = string([]rune(line)[:maxFailureRunes]) + "…"
	}
	return line
}
