package toolpolicy

import (
	"testing"

	"agent-platform/internal/api"
)

func TestAllowsReadOnlyUsesBuiltinAllowlistAndExplicitMetadata(t *testing.T) {
	tests := []struct {
		name  string
		def   api.ToolDetailResponse
		found bool
		want  bool
	}{
		{name: "builtin read", def: api.ToolDetailResponse{Name: "file_read", Meta: map[string]any{"kind": "backend", "sourceCategory": "platform"}}, found: true, want: true},
		{name: "external cannot impersonate builtin", def: api.ToolDetailResponse{Name: "file_read", Meta: map[string]any{"kind": "external", "sourceCategory": "external"}}, found: true, want: false},
		{name: "builtin write", def: api.ToolDetailResponse{Name: "file_write", Meta: map[string]any{"kind": "backend", "sourceCategory": "platform"}}, found: true, want: false},
		{name: "explicit external read", def: api.ToolDetailResponse{Name: "remote_lookup", Meta: map[string]any{"kind": "external", "readOnly": true}}, found: true, want: true},
		{name: "kbase read uses metadata", def: api.ToolDetailResponse{Name: "kbase_search", Meta: map[string]any{"kind": "backend", "sourceCategory": "platform", "readOnly": true}}, found: true, want: true},
		{name: "kbase refresh uses metadata", def: api.ToolDetailResponse{Name: "kbase_refresh", Meta: map[string]any{"kind": "backend", "sourceCategory": "platform", "readOnly": false}}, found: true, want: false},
		{name: "known write cannot opt in", def: api.ToolDetailResponse{Name: "file_write", Meta: map[string]any{"kind": "external", "readOnly": true}}, found: true, want: false},
		{name: "frontend remains denied", def: api.ToolDetailResponse{Name: "read_form", Meta: map[string]any{"kind": "frontend", "readOnly": true}}, found: true, want: false},
		{name: "unknown", found: false, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowsReadOnly(tc.def, tc.found); got != tc.want {
				t.Fatalf("AllowsReadOnly()=%t want %t for %#v", got, tc.want, tc.def)
			}
		})
	}
}

func TestDisabledResultHasStableBTWError(t *testing.T) {
	result := DisabledResult("file_write")
	if result.Error != DisabledErrorCode || result.ExitCode != -1 {
		t.Fatalf("unexpected disabled result %#v", result)
	}
	if result.Structured["toolName"] != "file_write" || result.Structured["policy"] != "read_only" {
		t.Fatalf("unexpected disabled payload %#v", result.Structured)
	}
}
