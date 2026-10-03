package connector

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestSelectionUsesManifestDeclarationsInEitherDirection(t *testing.T) {
	a := Package{Manifest: Manifest{ID: "package-a", MutuallyExclusiveWith: []string{"package-b", "missing"}}}
	b := Package{Manifest: Manifest{ID: "package-b"}}
	c := Package{Manifest: Manifest{ID: "package-c", MutuallyExclusiveWith: []string{"package-b"}}}
	for _, tc := range []struct {
		packages  []Package
		id        string
		conflicts []string
	}{
		{[]Package{a, b}, "package-b", []string{"package-a"}},
		{[]Package{b, a}, "package-a", []string{"package-b"}},
		{[]Package{a, c, b}, "package-b", []string{"package-a", "package-c"}},
	} {
		err := ValidateSelection(tc.packages)
		var conflict *SelectionConflictError
		if !errors.Is(err, ErrSelectionConflict) || !errors.As(err, &conflict) || conflict.ConnectorID != tc.id || !reflect.DeepEqual(conflict.ConflictingConnectorIDs, tc.conflicts) {
			t.Fatalf("conflict: %#v %v", conflict, err)
		}
	}
	for _, packages := range [][]Package{nil, {a}, {a, c}, {{Manifest: Manifest{ID: PlatformControlConnectorID}}, {Manifest: Manifest{ID: WebControlConnectorID}}}} {
		if err := ValidateSelection(packages); err != nil {
			t.Fatalf("undeclared conflict: %v", err)
		}
	}
}

func TestManifestValidatesExclusiveReferences(t *testing.T) {
	for _, ids := range [][]string{{"package-a"}, {"../escape"}, {"package-b", "package-b"}, {""}} {
		manifest := Manifest{ID: "package-a", Name: "A", Type: "mcp", Version: "1.0.0", AuthMode: AuthNoAuth, MutuallyExclusiveWith: ids}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateManifest(manifest.ID, data); err == nil {
			t.Fatalf("accepted invalid exclusions: %v", ids)
		}
	}
	manifest := Manifest{ID: "package-a", Name: "A", Type: "mcp", Version: "1.0.0", AuthMode: AuthNoAuth, MutuallyExclusiveWith: []string{"not-installed"}}
	data, _ := json.Marshal(manifest)
	if err := ValidateManifest(manifest.ID, data); err != nil {
		t.Fatal(err)
	}
}
