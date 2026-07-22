package config

import "testing"

func TestLoadYAMLTreeStripsUTF8BOM(t *testing.T) {
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte("key: minimax\nbaseUrl: https://api.minimax.io\n")...)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root := tree.(map[string]any)
	if got := root["key"]; got != "minimax" {
		t.Fatalf("expected key minimax, got %#v", got)
	}
	if _, ok := root["\ufeffkey"]; ok {
		t.Fatalf("did not expect BOM-prefixed key, got %#v", root)
	}
}

func TestLoadYAMLTreePreservesQuotedNullLikeScalars(t *testing.T) {
	content := []byte(`
bareTilde: ~
quotedTilde: "~"
bareNull: null
quotedNull: "null"
`)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root := tree.(map[string]any)
	if got := root["bareTilde"]; got != nil {
		t.Fatalf("expected bare tilde to be nil, got %#v", got)
	}
	if got := root["quotedTilde"]; got != "~" {
		t.Fatalf("expected quoted tilde string, got %#v", got)
	}
	if got := root["bareNull"]; got != nil {
		t.Fatalf("expected bare null to be nil, got %#v", got)
	}
	if got := root["quotedNull"]; got != "null" {
		t.Fatalf("expected quoted null string, got %#v", got)
	}
}

func TestLoadYAMLTreePreservesQuotedNumericScalars(t *testing.T) {
	content := []byte(`
bareInt: 123
quotedInt: "123"
singleQuotedFloat: '1.5'
bareFloat: 1.5
rule: { bare: 42, quoted: "42" }
`)

	tree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load yaml tree: %v", err)
	}

	root := tree.(map[string]any)
	if got := root["bareInt"]; got != int64(123) {
		t.Fatalf("expected bare int to parse as int64, got %#v", got)
	}
	if got := root["quotedInt"]; got != "123" {
		t.Fatalf("expected quoted int to remain string, got %#v", got)
	}
	if got := root["singleQuotedFloat"]; got != "1.5" {
		t.Fatalf("expected quoted float to remain string, got %#v", got)
	}
	if got := root["bareFloat"]; got != 1.5 {
		t.Fatalf("expected bare float to parse as float64, got %#v", got)
	}
	rule, ok := root["rule"].(map[string]any)
	if !ok {
		t.Fatalf("expected flow map, got %#v", root["rule"])
	}
	if got := rule["bare"]; got != int64(42) {
		t.Fatalf("expected flow map bare value to parse as int64, got %#v", got)
	}
	if got := rule["quoted"]; got != "42" {
		t.Fatalf("expected flow map quoted value to remain string, got %#v", got)
	}
}

func TestLoadYAMLTreeDoubleQuotedEscapeDecodingIsOptIn(t *testing.T) {
	content := []byte(`
message: "line one\nline two\t\\n"
plain: line one\nline two
single: 'line one\nline two'
quotedHash: "before \"quote # kept\" after"
`)

	legacyTree, err := LoadYAMLTreeBytes(content)
	if err != nil {
		t.Fatalf("load legacy yaml tree: %v", err)
	}
	legacyRoot := legacyTree.(map[string]any)
	if got := legacyRoot["message"]; got != `line one\nline two\t\\n` {
		t.Fatalf("expected default loader to preserve escapes literally, got %#v", got)
	}

	decodedTree, err := LoadYAMLTreeBytesWithOptions(content, YAMLTreeOptions{DecodeDoubleQuotedEscapes: true})
	if err != nil {
		t.Fatalf("load opted-in yaml tree: %v", err)
	}
	decodedRoot := decodedTree.(map[string]any)
	if got, want := decodedRoot["message"], "line one\nline two\t\\n"; got != want {
		t.Fatalf("expected double-quoted escapes to decode once, want %q got %#v", want, got)
	}
	if got, want := decodedRoot["plain"], `line one\nline two`; got != want {
		t.Fatalf("expected plain scalar escapes to remain literal, want %q got %#v", want, got)
	}
	if got, want := decodedRoot["single"], `line one\nline two`; got != want {
		t.Fatalf("expected single-quoted escapes to remain literal, want %q got %#v", want, got)
	}
	if got, want := decodedRoot["quotedHash"], `before "quote # kept" after`; got != want {
		t.Fatalf("expected escaped quotes and hash to survive decoding, want %q got %#v", want, got)
	}
}

func TestLoadYAMLTreeCanPreserveDecodedScalarFromEnvironmentInterpolation(t *testing.T) {
	t.Setenv("YAML_TREE_LITERAL", "expanded")
	content := []byte("query:\n  message: \"${YAML_TREE_LITERAL}\"\n")

	decodedTree, err := LoadYAMLTreeBytesWithOptions(content, YAMLTreeOptions{
		DecodeDoubleQuotedEscapes:  true,
		PreserveDecodedScalarPaths: []string{"query.message"},
	})
	if err != nil {
		t.Fatalf("load opted-in yaml tree: %v", err)
	}
	query := decodedTree.(map[string]any)["query"].(map[string]any)
	if got, want := query["message"], "${YAML_TREE_LITERAL}"; got != want {
		t.Fatalf("expected preserved scalar to skip interpolation, want %q got %#v", want, got)
	}
}

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
