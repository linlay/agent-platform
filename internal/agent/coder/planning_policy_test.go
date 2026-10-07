package coder

import (
	"reflect"
	"testing"

	"agent-platform/internal/contracts"
)

func TestCoderModeGuardsKeepProxySeparate(t *testing.T) {
	if !IsMode(" coder ") {
		t.Fatalf("expected CODER mode match to be case-insensitive")
	}
	if IsMode("PROXY") {
		t.Fatalf("ordinary PROXY mode must not be treated as CODER")
	}
	if !IsNativeBackend("CODER", "") {
		t.Fatalf("expected CODER without acpBridgeId to be native backend")
	}
	if IsNativeBackend("CODER", "codex") {
		t.Fatalf("expected CODER with acpBridgeId not to be native backend")
	}
	if !IsACPBackend("CODER", "codex") {
		t.Fatalf("expected CODER with acpBridgeId to be ACP backend")
	}
	if IsACPBackend("PROXY", "codex") {
		t.Fatalf("ordinary PROXY must not become ACP CODER")
	}
}

func TestCoderRuntimeToolNamesOnlyNativeExecuteAddsPlanTaskTools(t *testing.T) {
	base := []string{"bash", "file_read", contracts.PlanAddTasksToolName}
	wantNative := []string{
		"bash",
		"file_read",
		contracts.PlanAddTasksToolName,
		contracts.PlanGetTasksToolName,
		contracts.PlanUpdateTaskToolName,
	}
	if got := RuntimeToolNamesForAgent("CODER", "", MainStage, base); !reflect.DeepEqual(got, wantNative) {
		t.Fatalf("native CODER execute tools=%#v want %#v", got, wantNative)
	}
	if got := RuntimeToolNamesForAgent("CODER", "", "planning", base); !reflect.DeepEqual(got, base) {
		t.Fatalf("native CODER planning tools=%#v want %#v", got, base)
	}
	if got := RuntimeToolNamesForAgent("CODER", "codex", MainStage, base); !reflect.DeepEqual(got, base) {
		t.Fatalf("ACP CODER execute tools=%#v want %#v", got, base)
	}
	if got := RuntimeToolNamesForAgent("PROXY", "", MainStage, base); !reflect.DeepEqual(got, base) {
		t.Fatalf("ordinary PROXY execute tools=%#v want %#v", got, base)
	}
}

func TestDefaultToolNamesForBackendExposePlatformToolsOnlyToNativeCoder(t *testing.T) {
	native := DefaultToolNamesForBackend("")
	if !containsTool(native, "artifact_publish") {
		t.Fatalf("native CODER default tools=%#v, want artifact_publish", native)
	}
	if containsTool(native, "regex") {
		t.Fatalf("regex must be supplied by configuration, got default tools=%#v", native)
	}
	if acp := DefaultToolNamesForBackend("codex"); len(acp) != 0 {
		t.Fatalf("ACP CODER default tools=%#v, want none", acp)
	}
}

func containsTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}
