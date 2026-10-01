package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
)

func newAgentCreationFixture(t *testing.T, configure func(*config.Config)) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.GeneralSettings.DefaultAgent = config.CoderDefaultAgentConfig{ModelKey: "mock-model"}
			cfg.CoderSettings.DefaultAgent = config.CoderDefaultAgentConfig{ModelKey: "mock-model"}
			cfg.AgentCreation = config.AgentCreationConfig{
				Types: map[string]config.AgentCreationTypeConfig{
					"general": {BaseToolsSet: true, BaseTools: []string{"datetime"}, DefaultGroups: []string{"docs", "broken"}},
				},
				Groups: []config.AgentCreationGroupConfig{
					{Key: "docs", Name: map[string]string{"zh-cn": "文档", "en-us": "Docs"}, Skills: []string{"mock-skill"}, Tools: []string{"datetime", "bash"}},
					{Key: "broken", Name: map[string]string{"": "Broken"}, Skills: []string{"no-such-skill"}, Connectors: []string{"no.such.connector"}},
				},
			}
			if configure != nil {
				configure(cfg)
			}
		},
	})
}

func createAgentForTest(t *testing.T, server *Server, payload map[string]any) (int, api.ApiResponse[api.AgentDetailResponse], string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
	var response api.ApiResponse[api.AgentDetailResponse]
	_ = json.Unmarshal(rec.Body.Bytes(), &response)
	return rec.Code, response, rec.Body.String()
}

func definitionList(definition map[string]any, section string, field string) []string {
	node, _ := definition[section].(map[string]any)
	items, _ := node[field].([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestAgentCreationOptionsDescribeTypesAndGroupAvailability(t *testing.T) {
	fixture := newAgentCreationFixture(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agents/creation-options", nil)
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("creation options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentCreationOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	options := response.Data
	keys := make([]string, 0, len(options.Types))
	byKey := map[string]api.AgentCreationTypeOption{}
	for _, item := range options.Types {
		keys = append(keys, item.Key)
		byKey[item.Key] = item
	}
	if strings.Join(keys, ",") != "general,coder,kbase,acp" {
		t.Fatalf("type order = %v", keys)
	}
	general := byKey["general"]
	if general.Mode != "GENERAL" || general.Engine != "native" || general.Label != "通用智能体" || !general.SupportsGroups || !general.WorkspaceRequired {
		t.Fatalf("unexpected general type: %#v", general)
	}
	if general.DefaultModelKey != "mock-model" || !general.DefaultModelAvailable {
		t.Fatalf("general default model must be reported as available: %#v", general)
	}
	// An unavailable group is never preselected.
	if !slices.Equal(general.DefaultGroups, []string{"docs"}) || !slices.Equal(general.BaseTools, []string{"datetime"}) {
		t.Fatalf("general defaults = groups %v tools %v", general.DefaultGroups, general.BaseTools)
	}
	if kbase := byKey["kbase"]; kbase.DefaultModelKey != "" || kbase.DefaultModelAvailable || !slices.Contains(kbase.BaseTools, "file_read") {
		t.Fatalf("kbase without a default model must require a choice: %#v", kbase)
	}
	if !slices.Contains(byKey["coder"].BaseTools, "bash") {
		t.Fatalf("coder base tools must show its built-in defaults: %#v", byKey["coder"].BaseTools)
	}
	acp := byKey["acp"]
	if acp.Mode != "CODER" || acp.Engine != "acp" || acp.SupportsGroups || acp.GroupsUnsupportedReason == "" || acp.Available || acp.UnavailableReason == "" {
		t.Fatalf("ACP without bridges must be unavailable and never take groups: %#v", acp)
	}
	if len(options.Groups) != 2 || options.Groups[0].Key != "docs" || options.Groups[0].Name != "文档" || !options.Groups[0].Available {
		t.Fatalf("unexpected first group: %#v", options.Groups)
	}
	if len(options.Groups[0].Skills) != 1 || options.Groups[0].Skills[0].Key != "mock-skill" || options.Groups[0].Skills[0].Name == "" {
		t.Fatalf("group members must be visible: %#v", options.Groups[0])
	}
	broken := options.Groups[1]
	if broken.Available || !strings.Contains(broken.UnavailableReason, "skill no-such-skill") || !strings.Contains(broken.UnavailableReason, "connector no.such.connector") {
		t.Fatalf("missing members must make the group unavailable with a reason: %#v", broken)
	}
	if len(options.Models) == 0 {
		t.Fatalf("model options are required to change the default model")
	}
}

func TestAgentCreationOptionsListConfiguredACPBridges(t *testing.T) {
	fixture := newAgentCreationFixture(t, func(cfg *config.Config) {
		cfg.CoderSettings.ACPBridges = map[string]config.CoderACPBridgeConfig{
			"codex":  {BaseURL: "http://127.0.0.1:1", AuthToken: "secret-token"},
			"claude": {BaseURL: "http://127.0.0.1:2"},
		}
	})
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/creation-options", nil))
	var response api.ApiResponse[api.AgentCreationOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	acp := response.Data.Types[3]
	if !acp.Available || len(acp.ACPBridges) != 2 || acp.ACPBridges[0].ID != "claude" || acp.ACPBridges[1].ID != "codex" {
		t.Fatalf("unexpected ACP bridges: %#v", acp)
	}
	if strings.Contains(rec.Body.String(), "secret-token") || strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Fatalf("bridge connection details must not be exposed: %s", rec.Body.String())
	}
}

func TestAgentCreateExpandsCapabilityGroups(t *testing.T) {
	fixture := newAgentCreationFixture(t, nil)
	workspace := t.TempDir()
	code, response, body := createAgentForTest(t, fixture.server, map[string]any{
		"definition": map[string]any{
			"mode":          "GENERAL",
			"runtimeConfig": map[string]any{"workspaceRoot": workspace},
		},
		"capabilityGroups": []string{"docs", "docs"},
	})
	if code != http.StatusOK {
		t.Fatalf("create returned %d: %s", code, body)
	}
	created := response.Data
	if !strings.HasPrefix(created.Key, "general-") || created.Mode != "GENERAL" {
		t.Fatalf("a general project without a key gets a generated one: %q %q", created.Key, created.Mode)
	}
	// Base tools first, then group tools, each written once.
	if got := definitionList(created.Definition, "toolConfig", "tools"); !slices.Equal(got, []string{"datetime", "bash"}) {
		t.Fatalf("written tools = %v", got)
	}
	if got := definitionList(created.Definition, "skillConfig", "skills"); !slices.Equal(got, []string{"mock-skill"}) {
		t.Fatalf("written skills = %v", got)
	}
	if _, stored := created.Definition["capabilityGroups"]; stored {
		t.Fatalf("groups are a creation template and must not be stored: %#v", created.Definition)
	}
	modelConfig, _ := created.Definition["modelConfig"].(map[string]any)
	if modelConfig["modelKey"] != "mock-model" {
		t.Fatalf("type default model not applied: %#v", modelConfig)
	}

	// Deselecting every group still writes the base tools and nothing else.
	code, response, body = createAgentForTest(t, fixture.server, map[string]any{
		"key": "general-empty",
		"definition": map[string]any{
			"key": "general-empty", "mode": "GENERAL",
			"runtimeConfig": map[string]any{"workspaceRoot": workspace},
		},
		"capabilityGroups": []string{},
	})
	if code != http.StatusOK {
		t.Fatalf("create without groups returned %d: %s", code, body)
	}
	if response.Data.Key != "general-empty" {
		t.Fatalf("a caller-chosen key must be kept: %q", response.Data.Key)
	}
	if got := definitionList(response.Data.Definition, "toolConfig", "tools"); !slices.Equal(got, []string{"datetime"}) {
		t.Fatalf("base-only tools = %v", got)
	}
	if _, hasSkills := response.Data.Definition["skillConfig"]; hasSkills {
		t.Fatalf("no group means no skills: %#v", response.Data.Definition)
	}
}

func TestAgentCreateWithoutCapabilityGroupsFieldIsUnchanged(t *testing.T) {
	fixture := newAgentCreationFixture(t, nil)
	code, response, body := createAgentForTest(t, fixture.server, map[string]any{
		"key":        "legacy-general",
		"definition": map[string]any{"key": "legacy-general", "mode": "GENERAL"},
	})
	if code != http.StatusOK {
		t.Fatalf("legacy create returned %d: %s", code, body)
	}
	for _, section := range []string{"toolConfig", "skillConfig", "connectorConfig", "runtimeConfig"} {
		if _, exists := response.Data.Definition[section]; exists {
			t.Fatalf("a request without capabilityGroups must not be expanded, got %s in %#v", section, response.Data.Definition)
		}
	}
}

func TestAgentCreateCoderGroupsKeepBuiltInTools(t *testing.T) {
	fixture := newAgentCreationFixture(t, nil)
	code, response, body := createAgentForTest(t, fixture.server, map[string]any{
		"definition": map[string]any{
			"mode":          "CODER",
			"runtimeConfig": map[string]any{"workspaceRoot": t.TempDir()},
		},
		"capabilityGroups": []string{"docs"},
	})
	if code != http.StatusOK {
		t.Fatalf("coder create returned %d: %s", code, body)
	}
	tools := definitionList(response.Data.Definition, "toolConfig", "tools")
	for _, tool := range []string{"bash", "file_read", "file_edit", "artifact_publish", "datetime"} {
		if !slices.Contains(tools, tool) {
			t.Fatalf("a group must not narrow CODER's tools; missing %s in %v", tool, tools)
		}
	}
}

func TestAgentCreateRejectsTemplatesThatCannotRun(t *testing.T) {
	fixture := newAgentCreationFixture(t, func(cfg *config.Config) {
		cfg.CoderSettings.ACPBridges = map[string]config.CoderACPBridgeConfig{"codex": {BaseURL: "http://127.0.0.1:1"}}
	})
	workspace := t.TempDir()
	general := func(runtimeConfig map[string]any, extra map[string]any) map[string]any {
		definition := map[string]any{"mode": "GENERAL"}
		if runtimeConfig != nil {
			definition["runtimeConfig"] = runtimeConfig
		}
		for key, value := range extra {
			definition[key] = value
		}
		return definition
	}
	withWorkspace := map[string]any{"workspaceRoot": workspace}
	for name, tc := range map[string]struct {
		definition map[string]any
		groups     []string
		code       string
	}{
		"unknown group":     {general(withWorkspace, nil), []string{"nope"}, "unknown_capability_group"},
		"unavailable group": {general(withWorkspace, nil), []string{"broken"}, "capability_group_unavailable"},
		"no directory":      {general(nil, nil), []string{}, "workspace_required"},
		"host root":         {general(map[string]any{"workspaceRoot": "@root"}, nil), []string{}, "workspace_required"},
		"unknown model":     {general(withWorkspace, map[string]any{"modelConfig": map[string]any{"modelKey": "ghost-model"}}), []string{}, "model_unavailable"},
		"kbase no model":    {map[string]any{"mode": "KBASE", "runtimeConfig": withWorkspace}, []string{}, "model_required"},
		"acp with groups":   {map[string]any{"engine": "acp", "runtimeConfig": map[string]any{"workspaceRoot": workspace, "acpBridgeId": "codex"}}, []string{"docs"}, "capability_groups_unsupported"},
		"proxy":             {map[string]any{"mode": "PROXY", "runtimeConfig": withWorkspace}, []string{}, "capability_groups_unsupported"},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(fixture.server.deps.Registry.Agents("all"))
			code, _, body := createAgentForTest(t, fixture.server, map[string]any{"definition": tc.definition, "capabilityGroups": tc.groups})
			if code != http.StatusBadRequest || !strings.Contains(body, `"code":"`+tc.code+`"`) {
				t.Fatalf("expected 400 %s, got %d: %s", tc.code, code, body)
			}
			if after := len(fixture.server.deps.Registry.Agents("all")); after != before {
				t.Fatalf("a rejected request must not create an agent: %d -> %d", before, after)
			}
		})
	}

	// The same ACP request without groups is accepted and needs no model.
	code, response, body := createAgentForTest(t, fixture.server, map[string]any{
		"definition":       map[string]any{"engine": "acp", "runtimeConfig": map[string]any{"workspaceRoot": workspace, "acpBridgeId": "codex"}},
		"capabilityGroups": []string{},
	})
	if code != http.StatusOK || response.Data.Engine != "acp" || response.Data.Mode != "CODER" {
		t.Fatalf("ACP project create returned %d: %s", code, body)
	}
	if _, declared := response.Data.Definition["toolConfig"]; declared {
		t.Fatalf("ACP must not receive platform tools: %#v", response.Data.Definition)
	}
}
