package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/interaction"
	"agent-platform/internal/knowledge/libraryinfo"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestKnowledgeSelectionUnboundAgentAndFrozenRun(t *testing.T) {
	a := api.KBaseOption{ID: "a", Name: "产品文档", Description: "发布规则", Readable: true, Collections: []libraryinfo.CollectionSummary{{Name: "docs", Description: "版本说明"}}}
	b := api.KBaseOption{ID: "b", Name: "业务库", Readable: true, Collections: []libraryinfo.CollectionSummary{}}
	lookup := map[string]api.KBaseOption{"a": a, "b": b}
	builder := New(Dependencies{Config: config.Config{Paths: config.PathsConfig{ChatsDir: filepath.Join(t.TempDir(), "chats")}}, KnowledgeLibrary: func(id string) (api.KBaseOption, error) {
		if value, ok := lookup[id]; ok {
			return value, nil
		}
		return api.KBaseOption{}, errors.New("missing")
	}})
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", Tools: []string{"kbase_list", "kbase_search"}, Connectors: []string{connector.KBaseControlConnectorID}}
	request := runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat-1", RunID: "run-1", References: queryinput.MakeReferences("kbase", []string{" a ", "b", "a", " "})}
	result, err := builder.BuildQuerySession(context.Background(), request, chat.Summary{ChatID: request.ChatID}, def, Options{})
	if err != nil {
		t.Fatal(err)
	}
	refs := result.RuntimeContext.References
	if strings.Join(queryinput.ResourceIDs(refs, "kbase"), ",") != "a,b" || len(result.CapabilityPrompts) != 0 || result.KBaseBindingLibraryID != "" {
		t.Fatalf("selection leaked into system context: %+v", result)
	}
	if refs[0].Name != a.Name || refs[0].Meta["description"] != a.Description || refs[1].ID != b.ID {
		t.Fatalf("reference metadata: %#v", refs)
	}
	request.References[0].ID = "changed"
	if refs[0].ID != "a" {
		t.Fatal("selection aliases caller input")
	}
	delete(lookup, "a")
	if _, err = builder.ResolveKbases(def, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("invalid library must reject whole request: %v", err)
	}
}

func TestKBaseConnectorMountWithoutLibraryContextAddsNoPrompt(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "PLAN-EXECUTE"} {
		t.Run(mode, func(t *testing.T) {
			builder := New(Dependencies{
				Config: config.Config{Paths: config.PathsConfig{ChatsDir: filepath.Join(t.TempDir(), "chats")}},
				KnowledgeLibrary: func(id string) (api.KBaseOption, error) {
					t.Fatalf("connector mounting must not discover libraries: %s", id)
					return api.KBaseOption{}, nil
				},
			})
			def := catalog.AgentDefinition{
				Key: "general", Mode: mode, Tools: []string{"kbase_list", "kbase_search"},
				Connectors: []string{connector.KBaseControlConnectorID},
				Workspace:  catalog.AgentWorkspaceConfig{Root: t.TempDir()},
			}
			request := runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat-1", RunID: "run-1"}
			session, err := builder.BuildQuerySession(context.Background(), request, chat.Summary{ChatID: request.ChatID}, def, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(session.CapabilityPrompts) != 0 || session.KBaseBindingLibraryID != "" {
				t.Fatalf("connector mount injected library context: prompts=%v libraries=%v binding=%q", session.CapabilityPrompts, session.RuntimeContext.References, session.KBaseBindingLibraryID)
			}
		})
	}
}

func TestKnowledgeSelectionGatesAndEmptySelection(t *testing.T) {
	builder := New(Dependencies{KnowledgeLibrary: func(id string) (api.KBaseOption, error) { return api.KBaseOption{ID: id, Readable: id != "bad"}, nil }})
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", Connectors: []string{connector.KBaseControlConnectorID}}
	c := interaction.DefaultsForEngine(def.Mode, false)
	c.Attachment.Kbases = false
	def.InteractionConfig = &c
	if _, err := builder.ResolveKbases(def, []string{" ", ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.ResolveKbases(def, []string{"a"}); err == nil {
		t.Fatal("disabled selection accepted")
	}
	c.Attachment.Kbases = true
	def.Connectors = nil
	if _, err := builder.ResolveKbases(def, []string{"a"}); err == nil {
		t.Fatal("selection implicitly mounted connector")
	}
	def.Connectors = []string{connector.KBaseControlConnectorID}
	for _, mode := range []string{"PROXY", "CHANNEL"} {
		def.Mode = mode
		if _, err := builder.ResolveKbases(def, []string{"a"}); err == nil {
			t.Fatalf("unsupported %s accepted", mode)
		}
	}
	def.Mode = "GENERAL"
	def.Engine = catalog.AgentEngineACP
	if _, err := builder.ResolveKbases(def, []string{"a"}); err == nil {
		t.Fatal("ACP selection accepted")
	}
	def.Engine = catalog.AgentEngineNative
	if _, err := builder.ResolveKbases(def, []string{"a", "bad"}); err == nil {
		t.Fatal("unreadable selection accepted")
	}
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if _, err := builder.ResolveKbases(def, ids); err == nil {
		t.Fatal("selection limit not enforced")
	}
}
