package viewquery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/view"
)

func automationsDocument() view.Document {
	return view.Document{
		View: view.Reference{Source: "connector", ConnectorID: connector.TaskControlConnectorID, Key: "automations"},
		Queries: map[string]view.Query{"list": {Adapter: "native", Operation: "automation.list",
			ParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","maximum":100}}}`)}},
	}
}

func TestExecuteRunsDeclaredOperationWithContext(t *testing.T) {
	var seen Context
	var seenParams map[string]any
	service := &Service{Operations: map[string]Operation{"automation.list": func(_ context.Context, qc Context, params map[string]any) (any, error) {
		seen, seenParams = qc, params
		return map[string]any{"items": []any{}}, nil
	}}, Now: func() time.Time { return time.UnixMilli(1700000000123) }}
	result, err := service.Execute(context.Background(), automationsDocument(), "list", json.RawMessage(`{"limit":5}`), Context{ChatID: "c", RunID: "r", AgentKey: "a"})
	if err != nil || string(result.Data) != `{"items":[]}` || result.ExecutedAt != 1700000000123 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if seen.AgentKey != "a" || seen.RunID != "r" || seenParams["limit"] != float64(5) {
		t.Fatalf("context %#v params %#v", seen, seenParams)
	}
}

func TestExecuteRejectsUndeclaredInvalidAndForeignQueries(t *testing.T) {
	ok := func(context.Context, Context, map[string]any) (any, error) { return map[string]any{}, nil }
	service := &Service{Operations: map[string]Operation{"automation.list": ok, "catalog.list": ok}}
	doc := automationsDocument()
	if _, err := service.Execute(context.Background(), doc, "get", nil, Context{}); !errors.Is(err, view.ErrNotFound) {
		t.Fatalf("undeclared query: %v", err)
	}
	if _, err := service.Execute(context.Background(), doc, "list", json.RawMessage(`{"limit":500}`), Context{}); !errors.Is(err, view.ErrDataInvalid) {
		t.Fatalf("schema violation: %v", err)
	}
	if _, err := service.Execute(context.Background(), doc, "list", json.RawMessage(`[1]`), Context{}); !errors.Is(err, view.ErrDataInvalid) {
		t.Fatalf("non-object params: %v", err)
	}
	foreign := automationsDocument()
	foreign.Queries["list"] = view.Query{Adapter: "native", Operation: "catalog.list"}
	if _, err := service.Execute(context.Background(), foreign, "list", nil, Context{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign operation: %v", err)
	}
	if _, err := (&Service{}).Execute(context.Background(), doc, "list", nil, Context{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing operation: %v", err)
	}
}

func TestExecuteWrapsOperationFailuresAndBoundsResults(t *testing.T) {
	service := &Service{Operations: map[string]Operation{"automation.list": func(_ context.Context, _ Context, params map[string]any) (any, error) {
		if params["limit"] == float64(1) {
			return nil, errors.New("invalid offset")
		}
		return strings.Repeat("x", MaxResultBytes), nil
	}}}
	if _, err := service.Execute(context.Background(), automationsDocument(), "list", json.RawMessage(`{"limit":1}`), Context{}); !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "invalid offset") {
		t.Fatalf("operation failure: %v", err)
	}
	if _, err := service.Execute(context.Background(), automationsDocument(), "list", nil, Context{}); !errors.Is(err, ErrFailed) {
		t.Fatalf("oversized result: %v", err)
	}
}
