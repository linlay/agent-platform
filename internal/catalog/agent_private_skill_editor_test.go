package catalog

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestEditableDefinitionWithSkillCreatesAndCanonicalizesSkillConfig(t *testing.T) {
	definition := map[string]any{"key": "demo"}
	updated := editableDefinitionWithSkill(definition, "cdp", true)
	if got := editableDefinitionSkills(updated); len(got) != 1 || got[0] != "cdp" {
		t.Fatalf("first private skill = %#v, want [cdp]", got)
	}

	definition["skillConfig"] = map[string]any{"skills": []any{"CDP", "other"}}
	updated = editableDefinitionWithSkill(definition, "cdp", true)
	if got := editableDefinitionSkills(updated); len(got) != 2 || got[0] != "cdp" || got[1] != "other" {
		t.Fatalf("case-insensitive private skill update = %#v, want [cdp other]", got)
	}
}

func TestAgentPrivateSkillMutationLockIsHeldUntilFinalized(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{
		AgentsDir:       filepath.Join(root, "agents"),
		SkillsCenterDir: filepath.Join(root, "skills-center"),
	}}}
	if _, err := registry.CreateEditableAgent("demo", map[string]any{
		"key":         "demo",
		"name":        "Demo",
		"mode":        "REACT",
		"modelConfig": map[string]any{"modelKey": "test-model"},
	}, nil, nil); err != nil {
		t.Fatalf("create Agent: %v", err)
	}
	archive := buildSkillImportZIP(t, []skillImportZIPEntry{{name: "SKILL.md", content: []byte("# Private\n")}})
	first, err := registry.BeginImportEditableAgentPrivateSkillArchive("demo", "first", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("begin first mutation: %v", err)
	}

	type mutationResult struct {
		mutation *EditableAgentPrivateSkillMutation
		err      error
	}
	secondDone := make(chan mutationResult, 1)
	go func() {
		mutation, err := registry.BeginImportEditableAgentPrivateSkillArchive("demo", "second", bytes.NewReader(archive), int64(len(archive)))
		secondDone <- mutationResult{mutation: mutation, err: err}
	}()
	select {
	case result := <-secondDone:
		t.Fatalf("second mutation completed before first was finalized: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}

	if err := registry.CommitEditableAgentPrivateSkillMutation(first); err != nil {
		t.Fatalf("commit first mutation: %v", err)
	}
	result := <-secondDone
	if result.err != nil {
		t.Fatalf("begin second mutation after commit: %v", result.err)
	}
	if err := registry.RollbackEditableAgentPrivateSkillMutation(result.mutation); err != nil {
		t.Fatalf("rollback second mutation: %v", err)
	}
}
