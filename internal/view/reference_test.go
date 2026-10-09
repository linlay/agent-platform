package view

import (
	"errors"
	"testing"
)

func TestReferenceSourcesAndConfigBoundary(t *testing.T) {
	for _, value := range []map[string]any{
		{"source": "builtin", "key": "question"},
		{"key": "question", "renderer": "native"},
		{"connectorId": "forms", "key": "edit", "hash": "bad"},
		{"key": "unknown-builtin"},
	} {
		if _, err := ParseConfigReference(value); err == nil {
			t.Fatalf("accepted config %#v", value)
		}
	}
	for _, value := range []map[string]any{{"key": "question"}, {"connectorId": "forms", "key": "edit"}} {
		ref, err := ParseConfigReference(value)
		if err != nil {
			t.Fatal(err)
		}
		want := "builtin"
		if ref.ConnectorID != "" {
			want = "connector"
		}
		if ref.Map()["source"] != want {
			t.Fatalf("reference %#v", ref)
		}
	}
	for _, key := range []string{"question", "approval", "planning", "confirm_dialog"} {
		ref, err := ResolveBuiltin(key)
		if err != nil || ref.Renderer != "native" {
			t.Fatalf("%s: %#v %v", key, ref, err)
		}
		if _, err := BuiltinDocument(key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("native document %s: %v", key, err)
		}
	}
	for _, key := range []string{"viewportType", "viewportKey"} {
		if RejectLegacy(map[string]any{key: nil}) == nil {
			t.Fatalf("accepted removed field %s", key)
		}
	}
}
