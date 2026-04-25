package tools

import (
	"testing"

	"agent-platform-runner-go/internal/contracts"
)

func TestBashYAMLParity(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("LoadEmbeddedToolDefinitions returned error: %v", err)
	}
	var hostDef, containerDef map[string]any
	for _, def := range defs {
		switch def.Name {
		case "bash":
			hostDef = contracts.CloneMap(def.Parameters)
		case "bash_sandbox":
			containerDef = contracts.CloneMap(def.Parameters)
		}
	}
	if hostDef == nil || containerDef == nil {
		t.Fatalf("expected both bash and bash_sandbox definitions, got host=%v container=%v", hostDef != nil, containerDef != nil)
	}

	hostProps := propertyKeys(hostDef)
	containerProps := propertyKeys(containerDef)
	assertKeySet(t, hostProps, []string{"command", "cwd", "description", "timeout_ms"})
	assertKeySet(t, containerProps, []string{"command", "cwd", "description", "env", "timeout_ms"})
	assertRequiredKeys(t, hostDef, []string{"command", "description"})
	assertRequiredKeys(t, containerDef, []string{"command", "description"})

	extra := difference(containerProps, hostProps)
	assertKeySet(t, extra, []string{"env"})
	if reverse := difference(hostProps, containerProps); len(reverse) != 0 {
		t.Fatalf("expected host props to be subset of container props, got extra=%v", reverse)
	}
}

func propertyKeys(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	keys := make([]string, 0, len(props))
	for key := range props {
		keys = append(keys, key)
	}
	return keys
}

func assertRequiredKeys(t *testing.T, schema map[string]any, want []string) {
	t.Helper()
	raw, _ := schema["required"].([]any)
	got := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok {
			got = append(got, text)
		}
	}
	assertKeySet(t, got, want)
}

func assertKeySet(t *testing.T, got []string, want []string) {
	t.Helper()
	gotMap := make(map[string]struct{}, len(got))
	for _, key := range got {
		gotMap[key] = struct{}{}
	}
	if len(gotMap) != len(want) {
		t.Fatalf("unexpected keys count: got=%v want=%v", got, want)
	}
	for _, key := range want {
		if _, ok := gotMap[key]; !ok {
			t.Fatalf("expected key %q in %v", key, got)
		}
	}
}

func difference(left []string, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, key := range right {
		rightSet[key] = struct{}{}
	}
	diff := make([]string, 0)
	for _, key := range left {
		if _, ok := rightSet[key]; !ok {
			diff = append(diff, key)
		}
	}
	return diff
}
