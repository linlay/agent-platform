package bashast

import "testing"

func TestDetectEmbeddedScripts(t *testing.T) {
	result, scripts := ParseWithEmbeddedDetection(`python3 -c 'import os; os.system("evil")'; node -e 'require("child_process")'; jq '.data' file.json; jq 'system("evil")'`)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(scripts) != 3 {
		t.Fatalf("expected three embedded scripts, got %#v", scripts)
	}
	for _, script := range scripts {
		if !IsDangerousEmbeddedScript(script) {
			t.Fatalf("expected dangerous script %#v", script)
		}
	}
}

func TestDetectEmbeddedScriptsSafeJQ(t *testing.T) {
	result, scripts := ParseWithEmbeddedDetection(`jq '.data' file.json`)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(scripts) != 0 {
		t.Fatalf("expected no embedded scripts, got %#v", scripts)
	}
}

func TestDetectEmbeddedScriptsAwk(t *testing.T) {
	result, scripts := ParseWithEmbeddedDetection(`awk '{ system("evil") }' file`)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(scripts) != 1 || scripts[0].Language != "awk" {
		t.Fatalf("expected awk embedded script, got %#v", scripts)
	}
}
