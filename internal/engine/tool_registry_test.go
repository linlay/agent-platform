package engine

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform-runner-go/internal/api"
)

func TestLoadRuntimeToolDefinitionsParsesFrontendAndBackendDescriptors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "confirm_dialog.yaml"), []byte(
		"name: confirm_dialog\n"+
			"label: Confirm\n"+
			"description: runtime frontend\n"+
			"type: frontend\n"+
			"toolType: html\n"+
			"viewportKey: confirm_dialog\n"+
			"inputSchema:\n"+
			"  type: object\n",
	), 0o644); err != nil {
		t.Fatalf("write frontend tool: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "_datetime_.yml"), []byte(
		"name: _datetime_\n"+
			"label: DateTime\n"+
			"description: metadata overlay\n"+
			"type: function\n"+
			"afterCallHint: use datetime\n"+
			"inputSchema:\n"+
			"  type: object\n",
	), 0o644); err != nil {
		t.Fatalf("write backend tool: %v", err)
	}

	defs, err := LoadRuntimeToolDefinitions(root)
	if err != nil {
		t.Fatalf("load runtime tools: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("expected two runtime defs, got %#v", defs)
	}
}

func TestMergeToolDefinitionsUsesLocalPriorityAndBackendMetadataOverlay(t *testing.T) {
	base := []api.ToolDetailResponse{
		{
			Key:         "_datetime_",
			Name:        "_datetime_",
			Description: "native backend description",
			Meta:        map[string]any{"kind": "backend", "sourceType": "local"},
		},
	}
	runtime := []api.ToolDetailResponse{
		{
			Key:           "_datetime_",
			Name:          "_datetime_",
			Label:         "DateTime",
			Description:   "overlay description",
			AfterCallHint: "use city datetime",
			Parameters:    map[string]any{"type": "object"},
			Meta:          map[string]any{"kind": "backend", "sourceType": "agent-local"},
		},
	}
	mcp := []api.ToolDetailResponse{
		{
			Key:         "_datetime_",
			Name:        "_datetime_",
			Description: "remote datetime",
			Meta:        map[string]any{"kind": "mcp"},
		},
	}

	defs := MergeToolDefinitions(base, runtime, mcp)
	if len(defs) != 1 {
		t.Fatalf("expected one merged definition, got %#v", defs)
	}
	if defs[0].Description != "native backend description" {
		t.Fatalf("expected native backend description to win, got %#v", defs[0])
	}
	if defs[0].AfterCallHint != "use city datetime" {
		t.Fatalf("expected runtime afterCallHint overlay, got %#v", defs[0])
	}
	if defs[0].Label != "DateTime" {
		t.Fatalf("expected runtime label overlay, got %#v", defs[0])
	}
}
