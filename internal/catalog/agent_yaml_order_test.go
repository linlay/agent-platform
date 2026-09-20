package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestAgentYAMLSemanticOrderAndStableModelSave(t *testing.T) {
	// Deliberately mix source order, list order, and unrelated nested keys.
	source := `wonders:
  - second
  - first
customZ:
  reasoning:
    enabled: true
    effort: HIGH
contextConfig:
  agents:
    - z
    - a
  tags:
    - session
    - system
modelConfig:
  reasoning:
    effort: HIGH
    enabled: true
  modelKey: old-model
role: assistant
mode: REACT
name: Example
key: example
customA:
  - name: item
    key: id
  -
    - z
    - a
`
	tree, err := config.LoadYAMLTreeBytes([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	definition := tree.(map[string]any)
	want := `key: example
name: Example
mode: REACT
role: assistant
modelConfig:
  modelKey: old-model
  reasoning:
    enabled: true
    effort: HIGH
contextConfig:
  tags:
    - session
    - system
  agents:
    - z
    - a
wonders:
  - second
  - first
customA:
  -
    key: id
    name: item
  -
    - z
    - a
customZ:
  reasoning:
    effort: HIGH
    enabled: true
`
	path := filepath.Join(t.TempDir(), "agent.yml")
	target := EditableAgentSource{Kind: "file", Path: path}
	save := func() string {
		t.Helper()
		if err := persistEditableAgent(target, definition, nil, nil, true); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := config.LoadYAMLTreeBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(parsed, definition) {
			t.Fatalf("save changed values: %#v", parsed)
		}
		definition = parsed.(map[string]any)
		return string(data)
	}
	if got := save(); got != want {
		t.Fatalf("unexpected YAML order:\n%s", got)
	}
	if got := save(); got != want {
		t.Fatalf("second save changed YAML:\n%s", got)
	}
	definition["modelConfig"].(map[string]any)["modelKey"] = "new-model"
	want = strings.Replace(want, "modelKey: old-model", "modelKey: new-model", 1)
	if got := save(); got != want {
		t.Fatalf("model-only save changed unrelated content:\n%s", got)
	}
}
