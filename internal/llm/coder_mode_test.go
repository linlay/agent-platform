package llm

import "testing"

// CODER is an ordinary built-in mode: it has no dedicated start path, and
// planning is applied before the mode-specific start for every native mode.
func TestResolveAgentModeCoderUsesBuiltinMainStage(t *testing.T) {
	mode, ok := resolveAgentMode("CODER").(builtinMode)
	if !ok || mode.stage != "coder" {
		t.Fatalf("expected CODER to resolve to its builtin main stage, got %#v", resolveAgentMode("CODER"))
	}
}
