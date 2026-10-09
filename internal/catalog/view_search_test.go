package catalog

import (
	"agent-platform/internal/api"
	"testing"
)

func TestToolViewSearchUsesIdentityFieldsOnly(t *testing.T) {
	for _, meta := range []map[string]any{nil, {"view": map[string]any{"key": "review-card", "connectorId": "crm-views"}}} {
		tool := api.ToolDetailResponse{Name: "lookup", Meta: meta}
		for _, query := range []string{"nil", "map", "key"} {
			if matchesToolTag(tool, query) {
				t.Fatalf("synthetic query %q matched %#v", query, meta)
			}
		}
		for _, query := range []string{"review-card", "crm-views"} {
			if matchesToolTag(tool, query) != (meta != nil) {
				t.Fatalf("identity query %q: %#v", query, meta)
			}
		}
	}
}
