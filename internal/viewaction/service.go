// Package viewaction executes declared viewer writes through connectorops.
// It owns live declaration checks, exact scopes, durable invocation identity
// and audit metadata. Transport handlers supply the authenticated Chat owner.
package viewaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/view"
	"agent-platform/internal/viewquery"
)

var (
	ErrUnavailable   = errors.New("view action unavailable")
	ErrDenied        = errors.New("view action denied")
	ErrStale         = errors.New("view action declaration changed")
	requestIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{8,128}$`)
)

type Context struct{ ChatID, RunID, AgentKey, Subject string }
type Result struct {
	Data         json.RawMessage `json:"data"`
	ExecutedAt   int64           `json:"executedAt"`
	InvocationID string          `json:"invocationId"`
	// Failure outcomes remain data so templates can preserve unknown/partial.
	ExitCode int `json:"exitCode"`
}
type Service struct {
	Sources    connector.Sources
	Connectors viewquery.ConnectorInvoker
	Authorize  func(context.Context, Context, string) error
}

func (s *Service) Execute(ctx context.Context, doc view.Document, name string, params json.RawMessage, requestID string, actor Context) (Result, error) {
	if s == nil || s.Connectors == nil || s.Authorize == nil {
		return Result{}, ErrUnavailable
	}
	if !requestIDPattern.MatchString(requestID) || actor.ChatID == "" || actor.RunID == "" || actor.Subject == "" {
		return Result{}, fmt.Errorf("%w: requestId and viewer context required", view.ErrDataInvalid)
	}
	action, err := view.ValidateAction(doc.Actions, name, params)
	if err != nil {
		return Result{}, err
	}
	// Do not let historical HTML retain revoked or changed write capabilities.
	expected := writeContract(action)
	check := func() error {
		if err := s.Authorize(ctx, actor, doc.View.ConnectorID); err != nil {
			return ErrDenied
		}
		pkg, err := s.Sources.Load(doc.View.ConnectorID)
		if err != nil {
			return ErrUnavailable
		}
		current, ok := pkg.Views[doc.View.Key].Actions[name]
		if !ok || pkg.Version != doc.View.Version || writeContract(current) != expected {
			return ErrStale
		}
		return nil
	}
	if err := check(); err != nil {
		return Result{}, err
	}
	values := map[string]any{}
	if err := json.Unmarshal(params, &values); err != nil {
		return Result{}, view.ErrDataInvalid
	}
	req := connectorops.Request{ConnectorID: doc.View.ConnectorID, Adapter: action.Adapter, IdempotencyKey: requestID}
	if action.Adapter == "cli" {
		req.Args, err = view.CLIArgs(action.Args, values)
		if action.StdinParam != "" {
			value, ok := values[action.StdinParam].(map[string]any)
			if !ok {
				return Result{}, view.ErrDataInvalid
			}
			raw, encodeErr := json.Marshal(value)
			if encodeErr != nil {
				return Result{}, view.ErrDataInvalid
			}
			req.Stdin = string(raw)
		}
	} else {
		req.Component, req.ToolName = action.Component, action.ToolName
		req.Arguments, err = viewquery.MCPArguments(action.Arguments, values)
	}
	if err != nil {
		return Result{}, err
	}
	scope := connectorops.Scope{Subject: "view:" + actor.Subject, IdempotencyNamespace: "view:" + actor.ChatID + ":" + doc.View.Key + ":" + name,
		Execution: []connectorops.Permission{{ConnectorID: req.ConnectorID, Adapter: req.Adapter}}, Check: check,
		Audit: map[string]any{"source": "view", "subject": actor.Subject, "chatId": actor.ChatID, "runId": actor.RunID, "agentKey": actor.AgentKey, "view": doc.View, "action": name, "params": values, "requestId": requestID, "startedAt": time.Now().UnixMilli()},
	}
	result, err := s.Connectors.Invoke(ctx, scope, req)
	if err != nil {
		return Result{}, err
	}
	raw := json.RawMessage(strings.TrimSpace(result.Stdout))
	if action.Adapter == "mcp" {
		raw = result.MCP
	}
	// A non-JSON response after dispatch cannot establish whether a write applied.
	if !json.Valid(raw) {
		return Result{}, &connectorops.Error{Code: "invocation_outcome_unknown", Status: 409}
	}
	return Result{Data: raw, ExecutedAt: time.Now().UnixMilli(), InvocationID: result.InvocationID, ExitCode: result.ExitCode}, nil
}

// writeContract is what a viewer write depends on. Intercept only routes an
// Agent's Bash call to a form, so toggling it leaves rendered pages writable.
func writeContract(action view.Action) string {
	action.Intercept = false
	wire, _ := json.Marshal(action)
	return string(wire)
}
