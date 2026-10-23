package platformcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"agent-platform/internal/automation"
	"agent-platform/internal/resources"
	"agent-platform/internal/view"
	"agent-platform/internal/viewquery"
)

func TestAutomationViewPaginationWithPublishedSchema(t *testing.T) {
	data, err := resources.ConnectorFS.ReadFile("connectors/builtin.task-control/view.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Views map[string]struct {
			Queries map[string]view.Query `json:"queries"`
		} `json:"views"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	h := (&ToolHandler{}).ConfigureAutomation(&automation.Service{Registry: automation.NewRegistry(t.TempDir()), DefaultZoneID: "UTC"})
	service := &viewquery.Service{Operations: h.ViewQueryOperations()}
	doc := view.Document{View: view.Reference{ConnectorID: "builtin.task-control", Key: "automations"}, Queries: manifest.Views["automations"].Queries}
	for _, offset := range []int{0, 100, 101, 1000} {
		params := json.RawMessage(fmt.Sprintf(`{"limit":20,"offset":%d}`, offset))
		result, err := service.Execute(context.Background(), doc, "list", params, viewquery.Context{})
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		var page struct {
			Total      int  `json:"total"`
			HasMore    bool `json:"hasMore"`
			NextOffset int  `json:"nextOffset"`
		}
		if err := json.Unmarshal(result.(json.RawMessage), &page); err != nil || page.Total != 0 || page.HasMore || page.NextOffset != 0 {
			t.Fatalf("unexpected empty page: %s (%v)", result, err)
		}
	}
}

func TestPaginationAdmissionSeparatesLimitAndOffset(t *testing.T) {
	h := &ToolHandler{}
	for _, tc := range []struct {
		tool, action string
		extra        map[string]any
	}{
		{"automation_query", "list", nil},
		{"automation_query", "executions", map[string]any{"id": "task"}},
		{"kbase_library_query", "list", nil},
	} {
		for _, body := range []string{`{"limit":20,"offset":0}`, `{"limit":20,"offset":101}`, `{"offset":-1}`, `{"offset":1.5}`, `{"offset":"0"}`, `{"limit":0}`, `{"limit":101}`} {
			var params map[string]any
			if err := json.Unmarshal([]byte(body), &params); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.extra {
				params[k] = v
			}
			_, _, err := h.admitted(tc.tool, map[string]any{"action": tc.action, "args": params}, controlExecution())
			wantValid := body == `{"limit":20,"offset":0}` || body == `{"limit":20,"offset":101}`
			if (err == nil) != wantValid {
				t.Fatalf("%s.%s %s: %v", tc.tool, tc.action, body, err)
			}
		}
	}
}

func TestViewQueryOperationsKeepToolValidationWithoutAdmission(t *testing.T) {
	operations := (&ToolHandler{}).ViewQueryOperations()
	ctx, qc := context.Background(), viewquery.Context{ChatID: "c", RunID: "r"}
	// No ExecutionContext exists: list reads run without tool admission.
	result, err := operations["catalog.list"](ctx, qc, map[string]any{"resourceType": "agent"})
	if err != nil || result.(map[string]any)["total"] != 0 {
		t.Fatalf("catalog list: %#v %v", result, err)
	}
	for _, params := range []map[string]any{
		{"resourceType": "model"},
		{"resourceType": "agent", "status": "broken"},
		{"resourceType": "agent", "extra": true},
	} {
		if _, err := operations["catalog.list"](ctx, qc, params); err == nil {
			t.Fatalf("catalog params accepted: %#v", params)
		}
	}
	if _, err := operations["automation.list"](ctx, qc, map[string]any{"limit": float64(500)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("automation limit: %v", err)
	}
	if _, err := operations["automation.list"](ctx, qc, map[string]any{}); err == nil || !strings.Contains(err.Error(), "automation service unavailable") {
		t.Fatalf("automation service: %v", err)
	}
}
