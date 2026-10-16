package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/interaction"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestSteerReferencesPrepareWithoutMutatingAndUseFrozenPolicy(t *testing.T) {
	builder := New(Dependencies{Config: config.Config{Paths: config.PathsConfig{ChatsDir: filepath.Join(t.TempDir(), "chats")}}, KnowledgeLibrary: func(id string) (api.KBaseOption, error) {
		return api.KBaseOption{ID: id, Name: id, Readable: true}, nil
	}})
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", Connectors: []string{connector.KBaseControlConnectorID}}
	session, err := builder.BuildQuerySession(context.Background(), runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat", RunID: "run"}, chat.Summary{ChatID: "chat"}, def, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Catalog changes do not alter the policy captured by this Run.
	changed := interaction.Defaults(def.Mode)
	changed.Attachment.Kbases = false
	def.InteractionConfig = &changed
	for _, closed := range []bool{true, false} {
		control := contracts.NewRunControl(context.Background(), session.RunID)
		if closed {
			control.CloseSteers()
		}
		req, err := session.PrepareReferenceSteer(queryinput.SteerRequest{RunID: session.RunID, Message: "look up the selected source", References: queryinput.MakeReferences("kbase", []string{"docs"})})
		if err != nil {
			t.Fatal(err)
		}
		if len(session.ReferencesScope.References()) != 0 {
			t.Fatal("preparation granted access")
		}
		req.PreparedMessages = []map[string]any{{"role": "user", "content": "read"}}
		if accepted := control.EnqueueSteer(req); accepted == closed {
			t.Fatalf("closed=%v accepted=%v", closed, accepted)
		}
		if !closed && len(session.ReferencesScope.References()) != 1 {
			t.Fatal("accepted reference was not committed")
		}
		control.Finish()
	}
	if _, err := session.PrepareReferenceSteer(queryinput.SteerRequest{RunID: "run", References: queryinput.MakeReferences("kbase", []string{"docs"})}); err == nil {
		t.Fatal("accepted library-only empty steer")
	}
}

func TestRestoreRunReferencesRevalidatesOnlyThisRunsAcceptedSteers(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.EnsureChatWithSource("current", "general", "test", "query"); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run", "other"} {
		if err := store.OnRunStarted(chat.RunStart{ChatID: "current", RunID: runID, AgentKey: "general", StartedAtMillis: 1791777599999}); err != nil {
			t.Fatal(err)
		}
	}
	for i, entry := range []struct{ run, id string }{{"run", "valid"}, {"run", "deleted"}, {"other", "unrelated"}} {
		err := store.AppendSteerLine("current", chat.SteerLine{Type: "steer", ChatID: "current", RunID: entry.run, LiveSeq: int64(i + 1), UpdatedAt: 1791777600000 + int64(i), Steer: map[string]any{"chatId": "current", "runId": entry.run, "steerId": fmt.Sprintf("s-%d", i), "requestId": fmt.Sprintf("r-%d", i), "role": "user", "message": "read", "references": queryinput.MakeReferences("kbase", []string{entry.id})}})
		if err != nil {
			t.Fatal(err)
		}
	}
	builder := New(Dependencies{Chats: store, KnowledgeLibrary: func(id string) (api.KBaseOption, error) {
		if id == "deleted" {
			return api.KBaseOption{}, fmt.Errorf("missing")
		}
		return api.KBaseOption{ID: id, Readable: true}, nil
	}})
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", Connectors: []string{connector.KBaseControlConnectorID}}
	session := contracts.QuerySession{RunID: "run", ChatID: "current", AgentKey: "general", ReferencesScope: contracts.NewRunReferences(nil)}
	if err := builder.RestoreSteerReferences(&session, def); err != nil {
		t.Fatal(err)
	}
	if got := queryinput.ResourceIDs(session.ReferencesScope.References(), "kbase"); strings.Join(got, ",") != "valid" {
		t.Fatalf("restored %v", got)
	}
	refs, err := builder.RunRequestReferences("current", "run", queryinput.MakeReferences("skill", []string{"writing"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(queryinput.ResourceIDs(refs, "kbase"), ",") != "valid,deleted" {
		t.Fatalf("handoff must retain invalid pointers too: %v", refs)
	}
	if session.SkillScripts != nil {
		t.Fatal("reconstructed script credentials")
	}
}

func TestRemoteReferenceAdmissionRejectsLocalResourceIdentities(t *testing.T) {
	builder := New(Dependencies{})
	for _, def := range []catalog.AgentDefinition{
		{Mode: "CODER", Engine: catalog.AgentEngineACP},
		{Mode: "PROXY"}, {Mode: "CHANNEL"},
	} {
		for _, steer := range []bool{false, true} {
			for _, kind := range []string{"skill", "kbase", "chat"} {
				grant, err := builder.PrepareRunReferences(context.Background(), contracts.QuerySession{}, def, queryinput.MakeReferences(kind, []string{"local-resource"}), steer, true, steer)
				if err == nil || !strings.Contains(err.Error(), "unsupported") || len(grant.References) != 0 {
					t.Fatalf("mode=%s engine=%s steer=%v type=%s grant=%+v err=%v", def.Mode, def.Engine, steer, kind, grant, err)
				}
			}
		}
	}
}

func TestAgentEntryReferencesSkipInteractionPolicyButKeepFrozenSnapshot(t *testing.T) {
	builder := New(Dependencies{Config: config.Config{Paths: config.PathsConfig{ChatsDir: filepath.Join(t.TempDir(), "chats")}}, KnowledgeLibrary: func(id string) (api.KBaseOption, error) {
		return api.KBaseOption{ID: id, Name: id, Readable: true}, nil
	}})
	closed := interaction.Defaults("GENERAL")
	closed.Attachment.Kbases = false
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", Connectors: []string{connector.KBaseControlConnectorID}, InteractionConfig: &closed}
	refs := queryinput.MakeReferences("kbase", []string{"docs"})
	req := runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat", RunID: "run", Message: "read", References: refs}
	if _, err := builder.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "chat"}, def, Options{}); err == nil {
		t.Fatal("a user query bypassed the disabled entry")
	}
	session, err := builder.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "chat"}, def, Options{SkipInteractionPolicy: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.ReferencesScope.References()) != 1 || session.InteractionConfig.Attachment.Kbases {
		t.Fatalf("refs=%v frozen=%+v", session.ReferencesScope.References(), session.InteractionConfig)
	}
	steer := queryinput.SteerRequest{RunID: "run", Message: "read", References: refs}
	if _, err := session.PrepareReferenceSteer(steer); err == nil {
		t.Fatal("a user steer bypassed the frozen policy")
	}
	steer.Origin = &queryinput.Origin{Type: "agent"}
	if _, err := session.PrepareReferenceSteer(steer); err != nil {
		t.Fatal(err)
	}
}

type skillCenterOnlyCatalog struct {
	Catalog
	skills nestedSkillCatalog
}

func (c skillCenterOnlyCatalog) SkillIDs() []string { return c.skills.SkillIDs() }
func (c skillCenterOnlyCatalog) SkillDefinition(id string) (catalog.SkillDefinition, bool) {
	return c.skills.SkillDefinition(id)
}

func TestAgentEntrySkillReferenceInSandboxNeedsSkillsCenterOnlyForCenterSkills(t *testing.T) {
	writeSkill := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\ndescription: demo\n---\nBody"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	center := t.TempDir()
	writeSkill(filepath.Join(center, "shared"))
	runtime := filepath.Join(t.TempDir(), "ru-agents", "agent", "revision")
	writeSkill(filepath.Join(runtime, "skills", "own"))
	installRuntimeSkillFixture(t, runtime, "own")
	builder := New(Dependencies{
		Config:   config.Config{Paths: config.PathsConfig{SkillsCenterDir: center}},
		Registry: skillCenterOnlyCatalog{skills: nestedSkillCatalog{"shared": {ID: "shared"}}},
	})
	closed := interaction.Defaults("GENERAL")
	closed.Attachment.Skills = false
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", RuntimeDir: runtime, Skills: []string{"own"}, InteractionConfig: &closed, Runtime: map[string]any{"environmentId": "env"}}
	prepare := func(id string) error {
		_, err := builder.PrepareRunReferences(context.Background(), contracts.QuerySession{}, def, queryinput.MakeReferences("skill", []string{id}), false, false, true)
		return err
	}
	// The Agent's own skill is served from its runtime directory.
	if err := prepare("own"); err != nil {
		t.Fatal(err)
	}
	if err := prepare("shared"); err == nil || !strings.Contains(err.Error(), "skills-center") {
		t.Fatalf("err=%v", err)
	}
	def.Runtime["sandboxMounts"] = []any{map[string]any{"platform": "skills-center"}}
	if err := prepare("shared"); err != nil {
		t.Fatal(err)
	}
}
