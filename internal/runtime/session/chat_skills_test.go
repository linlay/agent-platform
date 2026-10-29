package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/interaction"
	runtimetypes "agent-platform/internal/runtime/types"
)

func chatSkillFixture(t *testing.T) (*Builder, *chat.FileStore, catalog.AgentDefinition, string) {
	t.Helper()
	root := t.TempDir()
	center := filepath.Join(root, "skills-center")
	skill := filepath.Join(center, "calculator")
	if err := os.MkdirAll(filepath.Join(skill, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":              "---\nname: calculator\ndescription: calculate\n---\nRun scripts/calculate.mjs",
		"scripts/calculate.mjs": "console.log(42)\n",
	} {
		if err := os.WriteFile(filepath.Join(skill, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := chat.NewFileStoreAtStartup(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.EnsureChat("current", "agent", "calculate"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendQueryLine("current", chat.QueryLine{Type: "query", ChatID: "current", RunID: "first", AgentKey: "agent",
		UpdatedAt: 1791777600000, Query: map[string]any{"message": "calculate"}, References: queryinput.MakeReferences("skill", []string{"calculator"})}); err != nil {
		t.Fatal(err)
	}
	b := New(Dependencies{Chats: store, Config: config.Config{Paths: config.PathsConfig{ChatsDir: filepath.Join(root, "chats"), SkillsCenterDir: center}},
		Registry: skillCenterOnlyCatalog{skills: nestedSkillCatalog{"calculator": {ID: "calculator"}}}})
	return b, store, catalog.AgentDefinition{Key: "agent", Mode: "GENERAL"}, filepath.Join(skill, "scripts", "calculate.mjs")
}

func TestNewRunInheritsChatSkillGrantsWithoutReferencesOrReadonly(t *testing.T) {
	b, _, def, file := chatSkillFixture(t)
	req := runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "current", RunID: "second", Message: "calculate again"}
	s, err := b.BuildQuerySession(t.Context(), req, chat.Summary{ChatID: "current"}, def, Options{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := contracts.ExecutionContext{Session: s}
	if !s.SkillScripts.Matches(ctx.ScriptOwner(), file, "", false) {
		t.Fatal("new Run missed previously selected skill credential")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(file)))
	if err != nil {
		t.Fatal(err)
	}
	roots := s.ReferenceAccessRoots()
	if !containsPath(roots.ReadRoots, canonical) || containsPath(roots.ReadonlyRoots, canonical) {
		t.Fatalf("inherited roots: %+v", roots)
	}
	if len(req.References) != 0 || len(s.RuntimeContext.References) != 0 || len(s.ReferencesScope.References()) != 0 {
		t.Fatal("inherited skill became a current reference")
	}
	for _, m := range s.CurrentMessages {
		if strings.Contains(fmt.Sprint(m["content"]), "[References]") {
			t.Fatalf("inheritance changed current user input: %+v", s.CurrentMessages)
		}
	}
	if strings.Contains(s.SkillCatalogPrompt, "calculator") {
		t.Fatal("inheritance changed system skill catalog")
	}
	// Current explicit references retain their usual readonly/use semantics.
	req.RunID = "explicit"
	req.References = queryinput.MakeReferences("skill", []string{"calculator"})
	explicit, err := b.BuildQuerySession(t.Context(), req, chat.Summary{ChatID: "current"}, def, Options{IncludeHistory: true})
	if err != nil || !containsPath(explicit.ReferenceAccessRoots().ReadonlyRoots, canonical) || len(explicit.ReferencesScope.References()) != 1 {
		t.Fatalf("explicit reference changed: roots=%+v err=%v", explicit.ReferenceAccessRoots(), err)
	}
	// Edits invalidate this Run; the next Run snapshots the current version.
	if err := os.WriteFile(file, []byte("console.log(43)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.SkillScripts.Matches(ctx.ScriptOwner(), file, "", false) {
		t.Fatal("modified inherited script retained its credential")
	}
	req.RunID, req.References = "third", nil
	next, err := b.BuildQuerySession(t.Context(), req, chat.Summary{ChatID: "current"}, def, Options{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if !next.SkillScripts.Matches((&contracts.ExecutionContext{Session: next}).ScriptOwner(), file, "", false) {
		t.Fatal("new Run did not snapshot updated skill")
	}
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func TestChatSkillInheritanceSkipsUnavailableOrDisallowedSkills(t *testing.T) {
	for _, scenario := range []string{"deleted", "disabled-user-entry", "sandbox-unmounted"} {
		t.Run(scenario, func(t *testing.T) {
			b, _, def, file := chatSkillFixture(t)
			options := Options{IncludeHistory: true}
			if scenario == "deleted" {
				if err := os.Remove(filepath.Join(filepath.Dir(filepath.Dir(file)), "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			} else {
				policy := interaction.DefaultsForEngine("GENERAL", false)
				policy.Attachment.Skills = false
				def.InteractionConfig = &policy
			}
			if scenario == "sandbox-unmounted" {
				def.Runtime = map[string]any{"environmentId": "env"}
				options.SkipInteractionPolicy = true
				// Test the same helper with a frozen sandbox session; the shared
				// PrepareRunReferences gate rejects an absent /skills-center mount.
				s := contracts.QuerySession{AgentKey: def.Key, ChatID: "current", RunID: "second", AgentHasRuntimeSandbox: true,
					ReferencesScope: contracts.NewRunReferences(nil)}
				b.inheritChatSkills(t.Context(), s, runtimetypes.QueryCommand{ChatID: "current", RunID: "second"}, def, options)
				if len(s.ReferenceAccessRoots().ReadRoots) != 0 {
					t.Fatal("unmounted sandbox inherited skill access")
				}
				return
			}
			s, err := b.BuildQuerySession(t.Context(), runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "current", RunID: "second", Message: "continue"},
				chat.Summary{ChatID: "current"}, def, options)
			if err != nil {
				t.Fatal(err)
			}
			if s.SkillScripts.Matches((&contracts.ExecutionContext{Session: s}).ScriptOwner(), file, "", false) || len(s.ReferencesScope.AccessRoots().ReadRoots) != 0 {
				t.Fatal("unavailable skill gained access")
			}
		})
	}
}

type countedSkillHistory struct {
	chat.Store
	reads int
}

func (s *countedSkillHistory) PriorSkillReferenceIDs(string, string, int) ([]string, error) {
	s.reads++
	return nil, fmt.Errorf("history unavailable")
}

func TestChatSkillInheritanceEntryBoundariesAndReadErrors(t *testing.T) {
	for _, scenario := range []string{"new-chat", "child", "team-member", "side", "acp", "proxy", "channel", "recovered", "read-error"} {
		t.Run(scenario, func(t *testing.T) {
			reader := &countedSkillHistory{}
			b := New(Dependencies{Chats: reader})
			def := catalog.AgentDefinition{Key: "agent", Mode: "GENERAL"}
			req := runtimetypes.QueryCommand{ChatID: "chat", RunID: "run"}
			options := Options{IncludeHistory: true}
			switch scenario {
			case "new-chat":
				options.IncludeHistory = false
			case "child":
				options.SubTaskID = "child"
			case "team-member":
				options.TeamHistoryAgentKey = "member"
			case "side":
				req.Lane = "btw"
			case "acp":
				def.Engine = catalog.AgentEngineACP
			case "proxy", "channel":
				def.Mode = strings.ToUpper(scenario)
			case "recovered":
				options.DisableSkillScriptGrants = true
			}
			b.inheritChatSkills(context.Background(), contracts.QuerySession{}, req, def, options)
			if (reader.reads == 1) != (scenario == "read-error") {
				t.Fatalf("history reads = %d", reader.reads)
			}
		})
	}
}
