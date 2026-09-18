package referenceprompt

import (
	"agent-platform/internal/api"
	"strings"
	"testing"
)

func TestSelectionPromptCompactAndOptionalAnnotation(t *testing.T) {
	refs := []api.Reference{{ID: "long-uuid", Type: "selection", Name: "Selected text", Path: "/fake", MimeType: "text/plain", Text: "第一行\n第二行", Annotation: "精简\n保留含义", Meta: map[string]any{"sourceKind": "message"}}}
	got := FormatUserMessage("修改 #{long-uuid}", refs)
	want := "[References]\n- id: r1\n  type: selection\n  text: |\n    第一行\n    第二行\n  annotation: |\n    精简\n    保留含义\n\n[User message]\n修改 #{r1}"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if refs[0].ID != "long-uuid" {
		t.Fatal("mutated persisted ID")
	}
	refs[0].Annotation = ""
	if strings.Contains(FormatUserMessage("解释", refs), "annotation:") {
		t.Fatal("empty annotation emitted")
	}
}

func TestSelectionIDsAvoidFilesAndNonrecursiveReplacement(t *testing.T) {
	refs := []api.Reference{{ID: "r1", Type: "file", Path: "/real"}, {ID: "r3", Type: "selection", Text: "A"}, {ID: "uuid", Type: "selection", Text: "B"}}
	got := FormatUserMessage("#{r1} #{r3} #{uuid}", refs)
	if !strings.HasSuffix(got, "#{r1} #{r2} #{r3}") {
		t.Fatal(got)
	}
}

func TestSelectionAnnotationIndexIndependentOfReferenceID(t *testing.T) {
	index := 7
	got := FormatUserMessage("review", []api.Reference{{ID: "selection-uuid", Type: "selection", Text: "quote", AnnotationIndex: &index}})
	if !strings.Contains(got, "id: r1\n  type: selection\n  annotationIndex: 7\n  text: quote") || strings.Contains(got, "annotation:") {
		t.Fatal(got)
	}
}
