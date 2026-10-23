// Package viewquery runs the read queries a rendered display view declares.
//
// A query is a viewer action, like opening a management page: it is authorized
// for the viewing principal, never as an Agent tool call, and needs no Run
// execution context. The caller supplies the Run that published the view so
// Agent-scoped operations remain possible; current operations are
// deployment-level listings and only record it.
package viewquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"agent-platform/internal/connector"
	"agent-platform/internal/view"
)

// MaxResultBytes bounds one serialized query result.
const MaxResultBytes = 1 << 20

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

// Operation is one registered read operation. Params already satisfy the
// view's paramsSchema; operations still validate their own fields.
type Operation func(ctx context.Context, qc Context, params map[string]any) (any, error)

type Service struct {
	Operations map[string]Operation
}

// Execute runs a query declared by a frozen snapshot document.
func (s *Service) Execute(ctx context.Context, doc view.Document, name string, params json.RawMessage, qc Context) (any, error) {
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	query, err := view.ValidateQuery(doc.Queries, name, params)
	if err != nil {
		if errors.Is(err, view.ErrInvalid) {
			return nil, fmt.Errorf("%w: %v", view.ErrNotFound, err)
		}
		return nil, err
	}
	// Snapshots are validated at publication, but the owner is checked again
	// so a document can never reach another connector's operation.
	owner, registered := connector.NativeViewOperationOwner(query.Operation)
	if query.Adapter != "native" || !registered || owner != doc.View.ConnectorID {
		return nil, ErrUnavailable
	}
	operation := s.operation(query.Operation)
	if operation == nil {
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
	if err != nil || len(data) > MaxResultBytes {
		return nil, fmt.Errorf("%w: result exceeds %d bytes", ErrFailed, MaxResultBytes)
	}
	return json.RawMessage(data), nil
}

func (s *Service) operation(name string) Operation {
	if s == nil {
		return nil
	}
	return s.Operations[name]
}
