package platformcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/automation"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
	"agent-platform/internal/resources"
	"agent-platform/internal/view"
	"agent-platform/internal/viewquery"
)

func TestIndependentCatalogViewsConstrainResourceAndPreservePagination(t *testing.T) {
	root := t.TempDir()
	discoveryWrite(t, filepath.Join(root, "providers/demo.yml"), "key: demo\napiKey: secret-credential\nbaseUrl: https://secret.invalid\n")
	for _, key := range []string{"a", "b"} {
		discoveryWrite(t, filepath.Join(root, "models", key+".yml"), "key: "+key+"\nprovider: demo\nmodel: upstream\n")
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	h := NewToolHandler(config.Config{}, nil, nil).ConfigureControl(nil, nil, registry)
	data, err := resources.ConnectorFS.ReadFile("connectors/builtin.platform-control/view.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest view.Config
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	service := &viewquery.Service{Operations: h.ViewQueryOperations()}
	for key, typ := range map[string]string{"agents": "agent", "connectors": "connector", "providers": "provider", "models": "model", "skills": "skill", "tools": "tool", "mcp": "mcp"} {
		t.Run(key, func(t *testing.T) {
			definition, ok := manifest.Views[key]
			if !ok {
				t.Fatalf("missing view %s", key)
			}
			doc := view.Document{View: view.Reference{ConnectorID: "builtin.platform-control", Key: key}, Queries: definition.Queries}
			for _, other := range []string{"agent", "connector", "provider", "model", "skill", "tool", "mcp"} {
				if other == typ {
					continue
				}
				if _, err := service.Execute(context.Background(), doc, "list", json.RawMessage(fmt.Sprintf(`{"resourceType":%q}`, other)), viewquery.Context{}); err == nil {
					t.Fatalf("%s accepted resourceType %s", key, other)
				}
			}
			if typ != "provider" && typ != "model" {
				return
			}
			result, err := service.Execute(context.Background(), doc, "list", json.RawMessage(fmt.Sprintf(`{"resourceType":%q,"limit":1}`, typ)), viewquery.Context{})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "secret") {
				t.Fatalf("private provider fields leaked: %s", raw)
			}
			var page map[string]any
			if err := json.Unmarshal(raw, &page); err != nil {
				t.Fatal(err)
			}
			items := page["items"].([]any)
			if len(items) != 1 || items[0].(map[string]any)["definition"] == nil {
				t.Fatalf("missing display fields: %#v", page)
			}
			if typ == "model" {
				if page["hasMore"] != true {
					t.Fatal("missing next page", page)
				}
				params, _ := json.Marshal(map[string]any{"resourceType": typ, "limit": 1, "cursor": page["nextCursor"]})
				next, err := service.Execute(context.Background(), doc, "list", params, viewquery.Context{})
				if err != nil {
					t.Fatal(err)
				}
				var nextPage map[string]any
				if err := json.Unmarshal(next.(json.RawMessage), &nextPage); err != nil {
					t.Fatal(err)
				}
				if nextPage["items"].([]any)[0].(map[string]any)["resourceKey"] == items[0].(map[string]any)["resourceKey"] {
					t.Fatal("cursor repeated first page")
				}
			}
		})
	}
}

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
		{"resourceType": "unknown"},
		{"resourceType": "team"},
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
