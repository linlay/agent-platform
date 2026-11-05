package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

func catalogConnector(t *testing.T, id, name, description string, translations map[string]connector.Translation) catalog.ConnectorMount {
	t.Helper()
	dir := writeViewConnector(t, id, `{"views":{"main":{"description":"Display","renderer":"html","usage":["display"],"entry":"views/index.html","inputSchema":{"type":"object"}}}}`)
	body, err := json.Marshal(connector.Manifest{ID: id, Name: name, Description: description, Version: "1.0.0", Type: "view", AuthMode: connector.AuthDelegated, I18N: translations})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connector.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	return catalog.ConnectorMount{ID: id, Dir: dir}
}

func TestConnectorCatalogUsesOnlyExecutorMountsAndLocalizedMetadata(t *testing.T) {
	mounts := []catalog.ConnectorMount{
		catalogConnector(t, "zeta", "No description", "", nil),
		catalogConnector(t, "alpha", "Alpha", "English purpose", map[string]connector.Translation{
			"zh-CN": {Name: "名称\n测试", Description: "用途\n  说明"},
		}),
	}
	global := config.PromptsConfig{Connector: config.PromptConnectorConfig{InstructionsPrompt: " RULES ", CatalogHeader: " HEADER "}}
	builder := New(Dependencies{Config: config.Config{Prompts: global, Paths: config.PathsConfig{ChatsDir: t.TempDir()}}})
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		t.Run(mode, func(t *testing.T) {
			def := catalog.AgentDefinition{Key: "executor", Mode: mode, Connectors: []string{"not-mounted"}, ConnectorMounts: mounts}
			appendConfig := BuildPromptAppendConfig(global, def)
			request := runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat", RunID: "run"}
			session, err := builder.BuildQuerySession(t.Context(), request, chat.Summary{ChatID: "chat"}, def, Options{Locale: "zh-CN"})
			got := session.ConnectorCatalogPrompt
			want := "RULES\n\nHEADER\n- alpha | 名称 测试 | 用途 说明\n- zeta | No description"
			if err != nil || got != want {
				t.Fatalf("prompt = %q, %v", got, err)
			}
			def.ConnectorMounts = mounts[:1] // A member has only its own mount.
			got, err = BuildConnectorCatalogPrompt(def, "en", appendConfig.Connector)
			if err != nil || strings.Contains(got, "alpha") || !strings.Contains(got, "zeta") {
				t.Fatalf("executor scope = %q, %v", got, err)
			}
			def.ConnectorMounts = mounts[1:]
			got, err = BuildConnectorCatalogPrompt(def, "en", appendConfig.Connector)
			if err != nil || !strings.Contains(got, "alpha | Alpha | English purpose") {
				t.Fatalf("English metadata = %q, %v", got, err)
			}
		})
	}
}

func TestConnectorCatalogOmissionAndInvalidFrozenPackage(t *testing.T) {
	settings := contracts.ConnectorAppendConfig{InstructionsPrompt: "RULES", CatalogHeader: "HEADER"}
	missing := catalog.AgentDefinition{ConnectorMounts: []catalog.ConnectorMount{{ID: "missing", Dir: filepath.Join(t.TempDir(), "missing")}}}
	for _, tc := range []struct {
		name     string
		def      catalog.AgentDefinition
		settings contracts.ConnectorAppendConfig
	}{
		{"unconfigured", missing, contracts.ConnectorAppendConfig{}},
		{"header-only", missing, contracts.ConnectorAppendConfig{CatalogHeader: "HEADER"}},
		{"instructions-only", missing, contracts.ConnectorAppendConfig{InstructionsPrompt: "RULES"}},
		{"empty-mounts", catalog.AgentDefinition{}, settings},
		{"acp", catalog.AgentDefinition{Engine: catalog.AgentEngineACP, ConnectorMounts: missing.ConnectorMounts}, settings},
		{"proxy", catalog.AgentDefinition{Mode: "PROXY", ConnectorMounts: missing.ConnectorMounts}, settings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := BuildConnectorCatalogPrompt(tc.def, "en", tc.settings); err != nil || got != "" {
				t.Fatalf("omitted prompt = %q, %v", got, err)
			}
		})
	}
	if _, err := BuildConnectorCatalogPrompt(missing, "en", settings); err == nil {
		t.Fatal("invalid frozen package accepted")
	}
}

func TestConnectorCatalogDropsWholeEntriesAtBudget(t *testing.T) {
	first := catalogConnector(t, "alpha", "Small", "Short purpose", nil)
	large := catalogConnector(t, "zeta", "Large", strings.Repeat("x", maxConnectorCatalogBytes), nil)
	got, err := BuildConnectorCatalogPrompt(catalog.AgentDefinition{ConnectorMounts: []catalog.ConnectorMount{large, first}}, "en", contracts.ConnectorAppendConfig{InstructionsPrompt: "RULES", CatalogHeader: "HEADER"})
	if err != nil || got != "RULES\n\nHEADER\n- alpha | Small | Short purpose" {
		t.Fatalf("bounded prompt = %q, %v", got, err)
	}
}
