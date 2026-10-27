package builtin

import (
	"testing"

	"agent-platform/internal/interaction"
)

func TestExecutorTemplateDefaults(t *testing.T) {
	if WorkspaceEditingDefault("EXECUTOR") {
		t.Fatal("executor edits the Workspace by default")
	}
	if !WorkspaceEditingDefault("GENERAL") {
		t.Fatal("general lost its editing default")
	}
	if !PlanningModeSupported("EXECUTOR") {
		t.Fatal("planning is a native capability of every native mode")
	}
	if _, ok := Lookup("EXECUTOR"); ok {
		t.Fatal("executor must run on the general path, without a mode descriptor")
	}
	defaults := interaction.Defaults("EXECUTOR")
	if defaults.ShowPlanningMode || defaults.ShowEditingMode || defaults.ShowTerminal {
		t.Fatalf("executor offers conversation entries by default: %#v", defaults)
	}
}
