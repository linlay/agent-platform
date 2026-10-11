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
	"agent-platform/internal/interaction"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestKnowledgeSelectionUnboundAgentAndFrozenRun(t *testing.T) {
	a := api.KBaseOption{ID: "a", Name: "产品文档", Description: "发布规则", Readable: true, Collections: []api.KBaseCollectionSummary{{Name: "docs", Description: "版本说明"}}}
	b := api.KBaseOption{ID: "b", Name: "业务库", Readable: true, Collections: []api.KBaseCollectionSummary{}}
	lookup := map[string]api.KBaseOption{"a": a, "b": b}
	builder := New(Dependencies{Config: config.Config{Paths: config.PathsConfig{ChatsDir: filepath.Join(t.TempDir(), "chats")}}, KnowledgeLibrary: func(id string) (api.KBaseOption, error) {
		if value, ok := lookup[id]; ok {
			return value, nil
		}
		return api.KBaseOption{}, errors.New("missing")
	}})
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", Tools: []string{"kbase_list", "kbase_search"}, Connectors: []string{connector.KBaseControlConnectorID}}
	request := runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat-1", RunID: "run-1", MustUseKbases: []string{" a ", "b", "a", " "}}
	result, err := builder.BuildQuerySession(context.Background(), request, chat.Summary{ChatID: request.ChatID}, def, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.MustUseKbases, ",") != "a,b" || len(result.KnowledgeLibraries) != 2 || result.KBaseBindingLibraryID != "" {
		t.Fatalf("selection: %+v", result)
	}
	prompt := strings.Join(result.CapabilityPrompts, "\n")
	for _, text := range []string{"产品文档", "发布规则", "版本说明", "libraryId", "each required library"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("missing %q: %s", text, prompt)
		}
	}
	if result.ScopedFilePolicy != nil {
		t.Fatal("temporary library selection granted KBASE write roots")
	}
	request.MustUseKbases[0] = "changed"
	if result.MustUseKbases[0] != "a" {
		t.Fatal("selection aliases caller input")
	}
	delete(lookup, "a")
	if _, err = builder.ResolveKbases(def, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("invalid library must reject whole request: %v", err)
	}
}

func TestKnowledgeSelectionGatesAndEmptySelection(t *testing.T) {
	builder := New(Dependencies{KnowledgeLibrary: func(id string) (api.KBaseOption, error) { return api.KBaseOption{ID: id, Readable: id != "bad"}, nil }})
	def := catalog.AgentDefinition{Key: "general", Mode: "GENERAL", Connectors: []string{connector.KBaseControlConnectorID}}
	c := interaction.Defaults(def.Mode)
	c.MustUseKbases = false
	def.InteractionConfig = &c
	if _, err := builder.ResolveKbases(def, []string{" ", ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.ResolveKbases(def, []string{"a"}); err == nil {
		t.Fatal("disabled selection accepted")
	}
	c.MustUseKbases = true
	def.Connectors = nil
	if _, err := builder.ResolveKbases(def, []string{"a"}); err == nil {
		t.Fatal("selection implicitly mounted connector")
	}
	def.Connectors = []string{connector.KBaseControlConnectorID}
	for _, mode := range []string{"TEAM", "PROXY", "CHANNEL"} {
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
