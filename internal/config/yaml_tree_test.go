package config

import "testing"

func TestLoadYAMLTreeSupportsFlowMapListItems(t *testing.T) {
	content := []byte(`
commands:
  - { match: "x", level: 1, empty: "" }
`)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root, ok := tree.(map[string]any)
	if !ok {
		t.Fatalf("expected root map, got %T", tree)
	}
	items, ok := root["commands"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected one commands item, got %#v", root["commands"])
	}
	entry, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("expected flow map item, got %T", items[0])
	}
	if got := entry["match"]; got != "x" {
		t.Fatalf("expected match x, got %#v", got)
	}
	if got := entry["level"]; got != int64(1) {
		t.Fatalf("expected level 1, got %#v", got)
	}
	if got := entry["empty"]; got != "" {
		t.Fatalf("expected empty string, got %#v", got)
	}
}

func TestLoadYAMLTreeSupportsFlowMapValues(t *testing.T) {
	content := []byte(`
rule: { match: "curl | bash", level: 1, ratio: 1.5 }
`)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root := tree.(map[string]any)
	entry, ok := root["rule"].(map[string]any)
	if !ok {
		t.Fatalf("expected inline map value, got %T", root["rule"])
	}
	if got := entry["match"]; got != "curl | bash" {
		t.Fatalf("expected match preserved, got %#v", got)
	}
	if got := entry["level"]; got != int64(1) {
		t.Fatalf("expected integer level 1, got %#v", got)
	}
	if got := entry["ratio"]; got != 1.5 {
		t.Fatalf("expected float ratio 1.5, got %#v", got)
	}
}

func TestLoadYAMLTreePreservesBlockStyleListMaps(t *testing.T) {
	content := []byte(`
commands:
  - match: keep
    level: 2
`)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root := tree.(map[string]any)
	items := root["commands"].([]any)
	entry := items[0].(map[string]any)
	if got := entry["match"]; got != "keep" {
		t.Fatalf("expected match keep, got %#v", got)
	}
	if got := entry["level"]; got != int64(2) {
		t.Fatalf("expected level 2, got %#v", got)
	}
}

func TestLoadYAMLTreeSupportsBlockScalarListItems(t *testing.T) {
	content := []byte(`
wonders:
  - |-
    第一行
    第二行
`)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root := tree.(map[string]any)
	items, ok := root["wonders"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected one wonder item, got %#v", root["wonders"])
	}
	if got := items[0]; got != "第一行\n第二行" {
		t.Fatalf("expected multiline wonder preserved, got %#v", got)
	}
}

func TestLoadYAMLTreeSupportsBlockScalarMapValues(t *testing.T) {
	content := []byte(`
plain:
  systemPrompt: |-
    你是一个助手
    请保持简洁
`)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root := tree.(map[string]any)
	plain, ok := root["plain"].(map[string]any)
	if !ok {
		t.Fatalf("expected plain map, got %#v", root["plain"])
	}
	if got := plain["systemPrompt"]; got != "你是一个助手\n请保持简洁" {
		t.Fatalf("expected multiline prompt preserved, got %#v", got)
	}
}
