package adminsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
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
	s.LibraryDefinition = libs.BindingDefinition
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

func TestCatalogKnowledgeBindingRecheckedAfterStaging(t *testing.T) {
	s := controlFixture(t)
	libraries, err := kbases.New(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := libraries.Create(kbases.Input{Name: "Editable library", Collections: []kbases.Collection{{Name: "docs", SourcePath: t.TempDir(), Editable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	s.ValidateLibrary = libraries.ValidateBinding
	reads := 0
	s.LibraryDefinition = func(id string) (kbases.Definition, error) {
		reads++
		if reads == 3 {
			d.Collections[0].Editable = false
			if _, err := libraries.Edit(id, kbases.Input{Name: d.Name, BaseRevision: d.BaseRevision, Collections: d.Collections}); err != nil {
				t.Fatal(err)
			}
		}
		return libraries.BindingDefinition(id)
	}
	change := ControlChange{ControlTarget: ControlTarget{ResourceType: "agent", ResourceKey: "candidate"}, Action: "apply",
		Content: fmt.Sprintf("key: candidate\nmode: KBASE\nmodelConfig:\n  modelKey: test\nruntimeConfig:\n  workspaceRoot: %q\nkbaseConfig:\n  libraryId: %s\n", t.TempDir(), d.ID)}
	plan, err := s.Prepare(change, "caller")
	if err != nil || plan.KnowledgeBinding == nil || !plan.KnowledgeBinding.RequiresHuman {
		t.Fatalf("missing editable binding approval: %+v %v", plan, err)
	}
	_, err = s.Apply(context.Background(), change, "caller", plan.Digest)
	var failure *contracts.MutationError
	if !errors.As(err, &failure) || failure.State != "not_started" || !strings.Contains(err.Error(), "approval_stale") {
		t.Fatalf("library change after staging was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Config.Paths.AgentsDir, "candidate")); !os.IsNotExist(err) {
		t.Fatalf("stale library binding was published: %v", err)
	}
	current, err := s.Prepare(change, "caller")
	if err != nil || current.Digest == plan.Digest || current.KnowledgeBinding.RequiresHuman {
		t.Fatalf("fresh definition baseline was not used: %+v %v", current, err)
	}
	if _, err := s.Apply(context.Background(), change, "caller", current.Digest); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogKnowledgeBindingReviewCannotBeSkipped(t *testing.T) {
	s := controlFixture(t)
	s.ValidateLibrary = func(string) error { return nil }
	change := ControlChange{ControlTarget: ControlTarget{ResourceType: "agent", ResourceKey: "candidate"}, Action: "apply",
		Content: "key: candidate\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nkbaseConfig:\n  libraryId: library\n"}
	if _, err := s.Prepare(change, "caller"); err == nil || !strings.Contains(err.Error(), "binding review unavailable") {
		t.Fatalf("new library binding silently skipped grant review: %v", err)
	}
}

func TestCatalogKnowledgeBindingApprovalIgnoresIndexState(t *testing.T) {
	s := controlFixture(t)
	runtimeRoot := t.TempDir()
	libraries, err := kbases.New(context.Background(), t.TempDir(), runtimeRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := libraries.Create(kbases.Input{Name: "Library", Collections: []kbases.Collection{{Name: "docs", SourcePath: t.TempDir(), Editable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	s.ValidateLibrary, s.LibraryDefinition = libraries.ValidateBinding, libraries.BindingDefinition
	change := ControlChange{ControlTarget: ControlTarget{ResourceType: "agent", ResourceKey: "candidate"}, Action: "apply",
		Content: fmt.Sprintf("key: candidate\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nkbaseConfig:\n  libraryId: %s\n", d.ID)}
	before, err := s.Prepare(change, "caller")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, d.ID, "state.json"), []byte(`{"state":"indexing","indexedAt":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := s.Prepare(change, "caller")
	if err != nil || after.Digest != before.Digest {
		t.Fatalf("indexing state invalidated binding approval: %+v %v", after, err)
	}
}

func TestCatalogRepairingACPBindingRequiresHumanApproval(t *testing.T) {
	s := controlFixture(t)
	libraries, err := kbases.New(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := libraries.Create(kbases.Input{Name: "Library", Collections: []kbases.Collection{{Name: "docs", SourcePath: t.TempDir(), Editable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	s.ValidateLibrary, s.LibraryDefinition = libraries.ValidateBinding, libraries.BindingDefinition
	content := fmt.Sprintf("key: candidate\nmode: KBASE\nmodelConfig:\n  modelKey: test\nruntimeConfig:\n  workspaceRoot: %q\nkbaseConfig:\n  libraryId: %s\n", t.TempDir(), d.ID)
	putControlFile(t, filepath.Join(s.Config.Paths.AgentsDir, "candidate", "agent.yml"), content+"engine: acp\n")
	target := ControlTarget{ResourceType: "agent", ResourceKey: "candidate"}
	current, err := s.Read(target)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.Prepare(ControlChange{ControlTarget: target, Action: "apply", Content: content, BaseRevision: current.BaseRevision}, "caller")
	if err != nil || plan.KnowledgeBinding == nil || !plan.KnowledgeBinding.RequiresHuman {
		t.Fatalf("native KBASE activation bypassed binding review: %+v %v", plan, err)
	}
}

func TestCatalogBindingApprovalFreezesCanonicalDirectory(t *testing.T) {
	s := controlFixture(t)
	libraries, err := kbases.New(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := libraries.Create(kbases.Input{Name: "Library", Collections: []kbases.Collection{{Name: "docs", SourcePath: t.TempDir(), Editable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	s.ValidateLibrary, s.LibraryDefinition = libraries.ValidateBinding, libraries.BindingDefinition
	change := ControlChange{ControlTarget: ControlTarget{ResourceType: "agent", ResourceKey: "candidate"}, Action: "apply",
		Content: fmt.Sprintf("key: candidate\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nkbaseConfig:\n  libraryId: %s\n", d.ID)}
	old, err := s.Prepare(change, "caller")
	if err != nil {
		t.Fatal(err)
	}
	source := d.Collections[0].SourcePath
	if err := os.Rename(source, source+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), source); err != nil {
		t.Fatal(err)
	}
	current, err := s.Prepare(change, "caller")
	if err != nil || current.KnowledgeBinding.BaseRevision != old.KnowledgeBinding.BaseRevision || current.Digest == old.Digest {
		t.Fatalf("canonical redirect did not invalidate unchanged configuration revision: %+v %v", current, err)
	}
	if _, err := s.Apply(context.Background(), change, "caller", old.Digest); err == nil || !strings.Contains(err.Error(), "approval_stale") {
		t.Fatalf("redirected editable directory reused an old approval: %v", err)
	}
}
