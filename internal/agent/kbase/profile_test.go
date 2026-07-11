package kbase

import (
	"reflect"
	"testing"
)

func TestResolveBoundaryPolicyOwnsToolsAndMemoryBoundary(t *testing.T) {
	policy := ResolveBoundaryPolicy([]string{"bash", ToolSearch, "memory_search"})
	if policy.MemoryEnabled {
		t.Fatal("KBASE boundary must disable memory")
	}
	if !reflect.DeepEqual(policy.ToolNames, []string{ToolSearch}) {
		t.Fatalf("filtered tools = %#v, want [%s]", policy.ToolNames, ToolSearch)
	}

	defaults := ResolveBoundaryPolicy([]string{"bash", "memory_search"})
	if !reflect.DeepEqual(defaults.ToolNames, DefaultToolNames()) {
		t.Fatalf("invalid-only tools must fall back to KBASE defaults: %#v", defaults.ToolNames)
	}
}
