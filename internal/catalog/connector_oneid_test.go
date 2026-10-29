package catalog

import (
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestMountedOneIDCLIKeepsCredentialBindingWithoutTemplates(t *testing.T) {
	for _, tc := range []struct {
		name, mode, kind, cli string
		wantBinding           bool
		wantTemplate          string
	}{
		{name: "no templates", mode: "oneid", kind: "cli", cli: `{}`, wantBinding: true},
		{name: "public env only", mode: "oneid", kind: "cli", cli: `{"env":{"PUBLIC_VALUE":"public"}}`, wantBinding: true},
		{name: "credential template", mode: "oneid", kind: "cli", cli: `{"env":{"SERVICE_TOKEN":"${AP_ID_TOKEN}"}}`, wantBinding: true, wantTemplate: "${AP_ID_TOKEN}"},
		{name: "MCP only", mode: "oneid", kind: "mcp"},
		{name: "self CLI", mode: "self", kind: "cli", cli: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := config.Config{Paths: config.PathsConfig{
				AgentsDir: filepath.Join(root, "agents"), ConnectorsCenterDir: filepath.Join(root, "connectors"),
				RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills-center"),
			}}
			dir := filepath.Join(cfg.Paths.ConnectorsCenterDir, "fixture")
			writeRuntimeAssemblerFile(t, filepath.Join(dir, "connector.json"), `{"id":"fixture","name":"Fixture","version":"1.0.0","type":"`+tc.kind+`","authMode":"`+tc.mode+`"}`)
			if tc.cli != "" {
				writeRuntimeAssemblerFile(t, filepath.Join(dir, "cli.json"), tc.cli)
			} else {
				writeRuntimeAssemblerFile(t, filepath.Join(dir, "mcp.json"), `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)
			}
			writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "demo", "agent.yml"), "key: demo\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors: [fixture]\n")
			registry, err := newVersionTestRegistry(t, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			def, ok := registry.AgentDefinition("demo")
			if !ok {
				t.Fatalf("agent unavailable: %#v", registry.adminAgents)
			}
			if !tc.wantBinding {
				if len(def.ConnectorCredentials) != 0 {
					t.Fatalf("unexpected CLI credential binding: %#v", def.ConnectorCredentials)
				}
				return
			}
			if len(def.ConnectorCredentials) != 1 {
				t.Fatalf("credential bindings = %#v", def.ConnectorCredentials)
			}
			binding := def.ConnectorCredentials[0]
			if binding.ID != "fixture" || binding.Mode != connector.AuthOneID || binding.Env["SERVICE_TOKEN"] != tc.wantTemplate {
				t.Fatalf("incorrect credential binding: %#v", binding)
			}
			if tc.wantTemplate == "" && len(binding.Env) != 0 {
				t.Fatalf("public env became credential templates: %#v", binding.Env)
			}
		})
	}
}
