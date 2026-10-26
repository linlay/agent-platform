package view

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestActionRequiresClosedSchemaAndOwnWriteAdapter(t *testing.T) {
	base := Action{Query: Query{Adapter: "cli", Args: []QueryArg{{Literal: "write"}}, ParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"fields":{"type":"object"}}}`)}, StdinParam: "fields"}
	for _, tc := range []struct {
		name   string
		change func(*Action)
		valid  bool
	}{
		{"valid", func(*Action) {}, true},
		{"native", func(a *Action) { a.Adapter = "native" }, false},
		{"no schema", func(a *Action) { a.ParamsSchema = nil }, false},
		{"open schema", func(a *Action) { a.ParamsSchema = json.RawMessage(`{"type":"object"}`) }, false},
		{"undeclared stdin", func(a *Action) { a.StdinParam = "filename" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := base
			tc.change(&action)
			err := validateActions("issue", Definition{Usage: []string{"display"}, Actions: map[string]Action{"write": action}})
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	if err := validateActions("issue", Definition{Usage: []string{"form"}, Actions: map[string]Action{"write": base}}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestActionDeclarationsAreIncludedInImmutableSnapshot(t *testing.T) {
	doc := Document{View: Reference{Source: "connector", ConnectorID: "crm", Key: "issue", Version: "1.0.0", Renderer: "html"}, HTML: "<p>Issue</p>", Actions: map[string]Action{"write": {Query: Query{Adapter: "cli", Args: []QueryArg{{Literal: "write"}}}}}}
	dir := t.TempDir()
	ref, err := SaveSnapshot(dir, doc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(dir, ref)
	if err != nil || got.Actions["write"].Adapter != "cli" {
		t.Fatal(got, err)
	}
	delete(doc.Actions, "write")
	changed, err := SaveSnapshot(dir, doc)
	if err != nil || changed.Hash == ref.Hash {
		t.Fatal(changed, err)
	}
}
