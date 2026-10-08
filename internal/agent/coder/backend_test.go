package coder

import "testing"

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

func TestCreateToolNamesRecommendations(t *testing.T) {
	native := CreateToolNames()
	if !containsTool(native, "artifact_publish") {
		t.Fatalf("native CODER default tools=%#v, want artifact_publish", native)
	}
	if containsTool(native, "regex") {
		t.Fatalf("regex must be supplied by configuration, got default tools=%#v", native)
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
