package platformcontrol

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/toolinput"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlActionableAdmission(t *testing.T) {
	h := &ToolHandler{}
	cases := []struct{ name, tool, body, field, actual, expected string }{
		{"string limit", "catalog_query", `{"action":"list","args":{"resourceType":"model","limit":"100"}}`, "args.limit", "string", "1–100"},
		{"fraction limit", "catalog_query", `{"action":"list","args":{"resourceType":"model","limit":1.5}}`, "args.limit", "number", "integer"},
		{"large limit", "catalog_query", `{"action":"list","args":{"resourceType":"model","limit":101}}`, "args.limit", "number", "1–100"},
		{"missing", "catalog_query", `{"action":"list"}`, "args.resourceType", "missing", "one of"},
		{"null", "catalog_query", `{"action":"list","args":null}`, "args", "null", "object"},
		{"nested", "catalog_manage", `{"action":"apply","args":{"resourceType":"skill","resourceKey":"x","content":"secret-content","preservePaths":[{}]}}`, "args.preservePaths[0]", "object", "string"},
		{"boolean", "chat_manage", `{"action":"setPinned","args":{"pinned":"true"}}`, "args.pinned", "string", "boolean"},
		{"enum", "chat_query", `{"action":"read","args":{"chatId":"x","view":"secret-value"}}`, "args.view", "string", "summary, messages"},
		{"action", "catalog_query", `{"action":"secret-value"}`, "action", "string", "list"},
		{"unknown", "catalog_query", `{"action":"list","args":{"resourceType":"model","secret-key":"secret-value"}}`, "args.<unknown>", "unknown field present", "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.body), &args); err != nil {
				t.Fatal(err)
			}
			r, err := h.Invoke(context.Background(), tc.tool, args, controlExecution())
			if err != nil || r.Error != "control_request_rejected" {
				t.Fatalf("%+v %v", r, err)
			}
			if r.Structured["field"] != tc.field || r.Structured["actual"] != tc.actual || !strings.Contains(r.Structured["expected"].(string), tc.expected) {
				t.Fatalf("%#v", r.Structured)
			}
			if !strings.Contains(r.Output, "fix_input") || strings.Contains(r.Output, "secret-") {
				t.Fatalf("unsafe/unhelpful: %s", r.Output)
			}
			if tc.name == "string limit" && !strings.Contains(r.Output, "limit:100") {
				t.Fatal(r.Output)
			}
		})
	}
	for _, limit := range []float64{1, 100} {
		_, _, err := h.admitted("catalog_query", map[string]any{"action": "list", "args": map[string]any{"resourceType": "model", "limit": limit}}, controlExecution())
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestReviewPreparationPreservesInputError(t *testing.T) {
	h := &ToolHandler{}
	_, err := h.PrepareToolApproval(context.Background(), "catalog_manage", map[string]any{"action": "apply", "args": map[string]any{"resourceType": "skill", "resourceKey": "x", "content": true}}, controlExecution())
	var input *toolinput.Error
	if !errors.As(err, &input) || input.Field != "args.content" {
		t.Fatalf("%v", err)
	}
}

func TestRuntimeComponentListsActualAvailableNames(t *testing.T) {
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(t.TempDir(), "agents"), TeamsDir: filepath.Join(t.TempDir(), "teams"), SkillsCenterDir: filepath.Join(t.TempDir(), "skills")}}
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := NewToolHandler(cfg, registry, nil)
	h.RuntimeSnapshot = func() map[string]any { return map[string]any{"platform": map[string]any{}} }
	r, err := h.Invoke(context.Background(), "platform_inspect", map[string]any{"action": "runtimeStatus", "args": map[string]any{"component": "secret-value"}}, controlExecution())
	if err != nil || r.Error != "control_failed" || r.Structured["field"] != "args.component" || strings.Contains(r.Output, "secret-value") || !strings.Contains(r.Output, "platform") {
		t.Fatalf("%#v %v", r, err)
	}
}
