package coder

import "testing"

func TestCoderModeGuardsKeepProxySeparate(t *testing.T) {
	if !IsMode(" coder ") {
		t.Fatalf("expected CODER mode match to be case-insensitive")
	}
	if IsMode("PROXY") {
		t.Fatalf("ordinary PROXY mode must not be treated as CODER")
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
