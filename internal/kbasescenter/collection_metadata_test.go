package kbasescenter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectionMetadataPreservesReadyIndexAndRunSnapshot(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, s, d.ID, "ready")
	old, err := s.RunCollections(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.readState(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.Collections[0].Editable = true
	d.Collections[0].Description = "规范\n多行说明: \"text\""
	d, err = s.Edit(d.ID, Input{Name: d.Name, Collections: d.Collections})
	if err != nil || d.State != "ready" || d.IndexedAt != ready.IndexedAt {
		t.Fatalf("metadata rebuilt index: %+v %v", d, err)
	}
	if got := s.fingerprint(d.Collections); got != state.AppliedFingerprint {
		t.Fatalf("fingerprint changed %s != %s", got, state.AppliedFingerprint)
	}
	current, err := s.RunCollections(d.ID)
	if err != nil || !current[0].Editable || current[0].Description != d.Collections[0].Description {
		t.Fatalf("snapshot: %+v %v", current, err)
	}
	if old[0].Editable || old[0].Description != "" {
		t.Fatal("old snapshot mutated")
	}
	d.Collections[0].Include = []string{"**/*.txt"}
	d, err = s.Edit(d.ID, Input{Name: d.Name, Collections: d.Collections})
	if err != nil || d.IndexedAt != 0 {
		t.Fatalf("source edit left old scope readable: %+v %v", d, err)
	}
}

func TestCollectionMetadataStrictParsing(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	file := filepath.Join(s.root, d.ID, "library.yml")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"    editable: \"true\"\n", "    description: true\n", "    editable: true\n    editable: false\n", "    futureField: true\n", "models:\n  graphExtraction:\n    modelKey: future\n"} {
		if err := os.WriteFile(file, append(append([]byte{}, raw...), []byte(suffix)...), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.loadConfiguration(d.ID, false); err == nil {
			t.Fatalf("accepted invalid YAML %s", suffix)
		}
	}
	d.Collections[0].Description = strings.Repeat("x", 4001)
	if _, err := s.Edit(d.ID, Input{Name: d.Name, Collections: d.Collections}); err == nil {
		t.Fatal("unbounded collection description")
	}
}
