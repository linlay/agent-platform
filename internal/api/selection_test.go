package api

import "testing"

func TestNormalizeSelectionReference(t *testing.T) {
	ref, err := NormalizeSelectionReference(Reference{ID: "stable", Text: "original", Annotation: "comment", Path: "/fake", Meta: map[string]any{"text": "legacy"}})
	if err != nil || ref.Text != "original" || ref.Annotation != "comment" || ref.ID != "stable" || ref.Path != "" || ref.Meta != nil {
		t.Fatalf("%+v %v", ref, err)
	}
	if _, err := NormalizeSelectionReference(Reference{Meta: map[string]any{"text": "old format"}}); err == nil {
		t.Fatal("accepted removed meta.text format")
	}
	if _, err := NormalizeSelectionReference(Reference{Text: " ", Annotation: "comment"}); err == nil {
		t.Fatal("accepted empty quote")
	}
}

func TestSelectionAnnotationIndexValidationAndSnapshot(t *testing.T) {
	index := 3
	ref, err := NormalizeSelectionReference(Reference{Text: "quote", AnnotationIndex: &index})
	if err != nil {
		t.Fatal(err)
	}
	index = 4
	if *ref.AnnotationIndex != 3 {
		t.Fatal("index was not frozen")
	}
	for _, invalid := range []int{0, -1} {
		if _, err := NormalizeSelectionReference(Reference{Text: "quote", AnnotationIndex: &invalid}); err == nil {
			t.Fatal("invalid index accepted")
		}
	}
}
