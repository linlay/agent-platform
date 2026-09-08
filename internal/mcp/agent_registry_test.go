package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

type fixtureAgentConnectors struct{ mounts []connector.AgentRuntime }

func (f *fixtureAgentConnectors) ConnectorRuntimes() []connector.AgentRuntime { return f.mounts }

func TestAgentMCPInstancesUseOwnBinariesSessionsAndToolRoutes(t *testing.T) {
	root := t.TempDir()
	sources := connector.Sources{ExternalRoot: filepath.Join(root, "connectors-center"), StateRoot: filepath.Join(root, ".state", "connectors")}
	source := filepath.Join(sources.ExternalRoot, "demo")
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	binaryName := filepath.Base(os.Args[0])
	if err := os.WriteFile(filepath.Join(source, "bin", binaryName), data, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"none"}`
	component, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"server": map[string]any{"type": "stdio", "command": binaryName, "args": []string{"-test.run=^TestMCPStdioHelperProcess$"}, "env": map[string]string{"AP_MCP_STDIO_HELPER": "1", "AP_MCP_STDIO_TOOL": "same_tool"}}}})
	for name, bytes := range map[string][]byte{"connector.json": []byte(manifest), "mcp.json": component} {
		if err := os.WriteFile(filepath.Join(source, name), bytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := NewAgentRegistry(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Servers()) != 0 {
		t.Fatal("unmounted connector started an instance")
	}
	provider := &fixtureAgentConnectors{}
	for _, agent := range []string{"first", "second"} {
		target := filepath.Join(root, "ru-agents", agent, "connectors")
		if _, err := sources.Materialize(target, []string{"demo"}); err != nil {
			t.Fatal(err)
		}
		provider.mounts = append(provider.mounts, connector.AgentRuntime{AgentKey: agent, ID: "demo", Dir: filepath.Join(target, "demo")})
	}
	if err := registry.BindAgents(provider); err != nil {
		t.Fatal(err)
	}
	client := NewClientWithGate(registry, nil, nil)
	defer client.Close()
	syncer := NewToolSync(registry, client)
	defs, err := syncer.Load(context.Background())
	if err != nil || len(defs) != 2 {
		t.Fatalf("scoped tools: %#v %v", defs, err)
	}
	if defs[0].Name == defs[1].Name {
		t.Fatal("two Agent tool routes collided")
	}
	pids := map[string]int{}
	for _, mount := range provider.mounts {
		key := connector.AgentServerKey(mount.AgentKey, connector.ServerKey("demo", "server"))
		server, ok := registry.Server(key)
		if !ok || server.Command != filepath.Join(mount.Dir, "bin", binaryName) || server.ConnectorAuthRoot != "" {
			t.Fatalf("wrong instance: %#v", server)
		}
		if names := syncer.ToolNamesForServers([]string{key}); len(names) != 1 {
			t.Fatalf("wrong Agent tool selection: %v", names)
		}
		result, err := client.CallTool(context.Background(), key, "same_tool", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := mcpResultPID(result)
		if err != nil {
			t.Fatal(err)
		}
		pids[mount.AgentKey] = pid
	}
	if pids["first"] == pids["second"] {
		t.Fatal("Agents shared a stdio process")
	}
	provider.mounts = provider.mounts[1:]
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	client.Reconcile()
	syncer.ReconcileRegistry()
	if len(syncer.Definitions()) != 1 {
		t.Fatal("detached Agent retained its tool snapshot")
	}
	key := connector.AgentServerKey("second", connector.ServerKey("demo", "server"))
	result, err := client.CallTool(context.Background(), key, "same_tool", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := mcpResultPID(result)
	if err != nil || pid != pids["second"] {
		t.Fatal("detaching one Agent replaced another process")
	}
}

func TestAgentMCPReadsSharedTokenWithoutWritingItIntoRuntime(t *testing.T) {
	root := t.TempDir()
	sources := connector.Sources{ExternalRoot: filepath.Join(root, "connectors-center"), StateRoot: filepath.Join(root, ".state", "connectors")}
	source := filepath.Join(sources.ExternalRoot, "demo")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"token","token_schema":{"fields":[{"name":"TOKEN","label":"Test token","required":true}]}}`,
		"mcp.json":       `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp","headers":{"Authorization":"Bearer ${TOKEN}"}}}}`,
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	credential, err := connector.CredentialsPath(sources.StateRoot, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(credential), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential, []byte(`{"TOKEN":"private-test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewAgentRegistry(sources)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fixtureAgentConnectors{}
	for _, agent := range []string{"first", "second"} {
		target := filepath.Join(root, "ru-agents", agent, "connectors")
		if _, err := sources.Materialize(target, []string{"demo"}); err != nil {
			t.Fatal(err)
		}
		provider.mounts = append(provider.mounts, connector.AgentRuntime{AgentKey: agent, ID: "demo", Dir: filepath.Join(target, "demo")})
		data, err := os.ReadFile(filepath.Join(target, "demo", "mcp.json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private-test-token") || !strings.Contains(string(data), "${TOKEN}") {
			t.Fatal("runtime template contains a credential")
		}
		if _, err := os.Stat(filepath.Join(target, "demo", "credentials.json")); !os.IsNotExist(err) {
			t.Fatal("credential copied into runtime")
		}
	}
	if err := registry.BindAgents(provider); err != nil {
		t.Fatal(err)
	}
	for _, server := range registry.Servers() {
		if server.Headers["Authorization"] != "Bearer private-test-token" {
			t.Fatal("central token not resolved for Agent instance")
		}
	}
}
