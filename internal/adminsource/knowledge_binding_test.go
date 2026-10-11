package adminsource

import (
	"context"
	"fmt"
	"testing"

	"agent-platform/internal/kbases"
)

func TestCatalogKnowledgeBindingRevalidatedBeforePublication(t *testing.T) {
	s := controlFixture(t)
	libs, err := kbases.New(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := libs.Create(kbases.Input{Name: "Library", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.ValidateLibrary = libs.ValidateBinding
	change := ControlChange{ControlTarget: ControlTarget{ResourceType: "agent", ResourceKey: "knowledge-agent"}, Action: "apply",
		Content: fmt.Sprintf("key: knowledge-agent\nmode: KBASE\nmodelConfig:\n  modelKey: test\nruntimeConfig:\n  workspaceRoot: %s\nkbaseConfig:\n  libraryId: %s\n", t.TempDir(), d.ID)}
	plan, err := s.Prepare(change, "caller")
	if err != nil {
		t.Fatal(err)
	}
	if err := libs.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), change, "caller", plan.Digest); err == nil {
		t.Fatal("published a binding deleted during approval")
	}
	definition := map[string]any{"mode": "GENERAL"}
	if err := ValidateKnowledgeBinding(definition, nil); err != nil {
		t.Fatal("optional capability required a library service", err)
	}
}
