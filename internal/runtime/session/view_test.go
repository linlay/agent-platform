package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/view"
)

func writeViewConnector(t *testing.T, id, viewJSON string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	for name, data := range map[string]string{
		"connector.json":   `{"id":"` + id + `","name":"Views","version":"1.0.0","type":"view","authMode":"self"}`,
		"view.json":        viewJSON,
		"views/index.html": "<p>view</p>",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestViewCatalogPromptInjectionRules(t *testing.T) {
	mounts := []view.Mount{{ID: "crm", Views: map[string]view.Definition{
		"orders": {Title: "Orders", Description: "List orders.", Usage: []string{"display"}},
		"edit":   {Description: "Edit.", Usage: []string{"form"}},
	}}}
	config := contracts.ViewAppendConfig{InstructionsPrompt: "RULES", CatalogHeader: "HEADER"}
	native := catalog.AgentDefinition{Key: "demo", Mode: "GENERAL", Engine: catalog.AgentEngineNative}
	want := "RULES\n\nHEADER\n\n- crm/orders — List orders."
	if got := BuildViewCatalogPrompt(native, mounts, config); got != want {
		t.Fatalf("native prompt = %q", got)
	}
	if got := BuildViewCatalogPrompt(native, mounts, contracts.ViewAppendConfig{CatalogHeader: "HEADER"}); got != "" {
		t.Fatalf("missing rules still injected: %q", got)
	}
	if got := BuildViewCatalogPrompt(native, mounts[:0], config); got != "" {
		t.Fatalf("empty catalog injected: %q", got)
	}
	formOnly := []view.Mount{{ID: "crm", Views: map[string]view.Definition{"edit": {Description: "Edit.", Usage: []string{"form"}}}}}
	if got := BuildViewCatalogPrompt(native, formOnly, config); got != "" {
		t.Fatalf("form-only views injected: %q", got)
	}
	acp := native
	acp.Engine = catalog.AgentEngineACP
	if got := BuildViewCatalogPrompt(acp, mounts, config); got != "" {
		t.Fatalf("ACP injected: %q", got)
	}
}

func TestConfigureViewsBindsMarkdownWithFrozenMountsAndSchema(t *testing.T) {
	dir := writeViewConnector(t, "crm", `{"views":{"orders":{"description":"List orders.","renderer":"html","usage":["display"],"entry":"views/index.html","dataSchema":{"type":"object","required":["items"]}}}}`)
	def := catalog.AgentDefinition{Key: "demo", Mode: "GENERAL", Engine: catalog.AgentEngineNative, ConnectorMounts: []catalog.ConnectorMount{{ID: "crm", Dir: dir}}}
	session := contracts.QuerySession{ChatRoot: t.TempDir(), PromptAppend: contracts.PromptAppendConfig{View: contracts.ViewAppendConfig{InstructionsPrompt: "RULES", CatalogHeader: "HEADER"}}}
	if err := New(Dependencies{}).ConfigureViews(&session, def); err != nil {
		t.Fatal(err)
	}
	if session.ViewCatalogPrompt != "RULES\n\nHEADER\n\n- crm/orders — List orders." || len(session.ViewMounts) != 1 {
		t.Fatalf("catalog prompt: %q mounts %d", session.ViewCatalogPrompt, len(session.ViewMounts))
	}
	ref := view.Reference{Source: "connector", ConnectorID: "crm", Key: "orders"}
	bound, err := session.BindMarkdownView(context.Background(), ref, "", json.RawMessage(`{"items":[]}`))
	if err != nil || bound.Hash == "" {
		t.Fatalf("bind: %#v %v", bound, err)
	}
	if _, err := view.LoadSnapshot(session.ChatRoot, bound); err != nil {
		t.Fatalf("snapshot not frozen in Chat: %v", err)
	}
	invalid, err := session.BindMarkdownView(context.Background(), ref, "", json.RawMessage(`{}`))
	if !errors.Is(err, view.ErrDataInvalid) || invalid.Hash != bound.Hash {
		t.Fatalf("schema failure: %#v %v", invalid, err)
	}
	if _, err := session.BindMarkdownView(context.Background(), view.Reference{Source: "connector", ConnectorID: "other", Key: "orders"}, "", json.RawMessage(`{}`)); !errors.Is(err, view.ErrNotFound) {
		t.Fatalf("unmounted connector: %v", err)
	}
}
