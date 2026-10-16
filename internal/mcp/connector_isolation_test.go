package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

func TestConnectorErrorsDoNotBlockRegistryStartupOrReload(t *testing.T) {
	for _, authMode := range []string{"none", "mcp"} {
		t.Run(authMode, func(t *testing.T) {
			sources := connector.Sources{ExternalRoot: t.TempDir()}
			writeIsolationConnector(t, sources.ExternalRoot, "healthy", "none", `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)
			badDir := writeIsolationConnector(t, sources.ExternalRoot, "broken", authMode, `{"mcpServers":{"a-valid":{"type":"streamableHttp","url":"https://example.test/mcp"},"z-invalid":{"type":"streamableHttp","url":"invalid-url"}}}`)
			var warnings bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&warnings)
			t.Cleanup(func() { log.SetOutput(previous) })
			registry, err := NewAgentRegistry(sources)
			if err != nil {
				t.Fatalf("bad connector blocked startup: %v", err)
			}
			provider := &fixtureAgentConnectors{mounts: []connector.AgentRuntime{
				{AgentKey: "good-agent", ID: "healthy", Dir: filepath.Join(sources.ExternalRoot, "healthy")},
				{AgentKey: "bad-agent", ID: "broken", Dir: badDir},
			}}
			if err := registry.BindAgents(provider); err != nil {
				t.Fatalf("bad connector blocked Agent binding: %v", err)
			}
			assertHealthyIsolationServer(t, registry)
			reloader := NewRegistryReloader(registry, nil)
			if err := reloader.ValidateSources(); err != nil {
				t.Fatalf("bad connector blocked reload validation: %v", err)
			}
			if err := reloader.Reload(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertHealthyIsolationServer(t, registry)
			if !strings.Contains(warnings.String(), "warning:") || !strings.Contains(warnings.String(), "broken") {
				t.Fatalf("missing connector warning: %s", warnings.String())
			}
			pkg, err := sources.Load("broken")
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateConnectorPackage(pkg); err == nil {
				t.Fatal("authored definition validation must stay strict")
			}
			public, err := NewRegistryWithSources(sources)
			if err != nil || len(public.Servers()) != 1 || public.Servers()[0].ConnectorID != "healthy" {
				t.Fatalf("public registry isolation: %v %v", public, err)
			}
			// Fixing the source makes it available on the next reload.
			if err := os.WriteFile(filepath.Join(badDir, "mcp.json"), []byte(`{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := reloader.Reload(context.Background()); err != nil || len(registry.Servers()) != 2 {
				t.Fatalf("fixed connector did not recover: %v", err)
			}
		})
	}
}

func TestConnectorReloadRemovesOnlyBrokenPackageRoutes(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir()}
	writeIsolationConnector(t, sources.ExternalRoot, "healthy", "none", `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)
	dir := writeIsolationConnector(t, sources.ExternalRoot, "broken", "none", `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)
	registry, err := NewRegistryWithSources(sources)
	if err != nil || len(registry.Servers()) != 2 {
		t.Fatalf("initial registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connector.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	assertHealthyIsolationServer(t, registry)
}

func TestConflictingConnectorDoesNotBlockUnrelatedRoutes(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir()}
	writeIsolationConnector(t, sources.ExternalRoot, "alpha", "none", `{"mcpServers":{"extra":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)
	writeIsolationConnector(t, sources.ExternalRoot, "alpha.extra", "none", `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)
	writeIsolationConnector(t, sources.ExternalRoot, "healthy", "none", `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)
	registry, err := NewRegistryWithSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	servers := registry.Servers()
	if len(servers) != 2 || servers[0].ConnectorID != "alpha" || servers[1].ConnectorID != "healthy" {
		t.Fatalf("conflicting package affected unrelated routes: %#v", servers)
	}
	packages, err := sources.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateConnectorPackages(packages); err == nil {
		t.Fatal("imports must still reject conflicting source keys")
	}
}

func TestAgentRegistryKeepsFrozenPathsWhenSourceIsInvalid(t *testing.T) {
	root := t.TempDir()
	sources := connector.Sources{ExternalRoot: filepath.Join(root, "connectors-center")}
	source := filepath.Join(sources.ExternalRoot, "healthy")
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"main": map[string]any{
		"type": "stdio", "command": filepath.Join(source, "bin", "demo"),
		"platform": map[string]any{"workingDirectory": source},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	writeIsolationConnector(t, sources.ExternalRoot, "healthy", "none", string(config))
	target := filepath.Join(root, "ru-agents", "agent", "connectors")
	if _, err := sources.Materialize(target, []string{"healthy"}); err != nil {
		t.Fatal(err)
	}
	mount, err := connector.ReadMount(target, "healthy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "connector.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewAgentRegistry(sources)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.BindAgents(&fixtureAgentConnectors{mounts: []connector.AgentRuntime{{AgentKey: "agent", ID: "healthy", Dir: mount.Dir, Digest: mount.Digest}}}); err != nil {
		t.Fatal(err)
	}
	assertHealthyIsolationServer(t, registry)
	server := registry.Servers()[0]
	if server.Command != filepath.Join(mount.Dir, "bin", "demo") || server.WorkingDir != mount.Dir {
		t.Fatalf("frozen mount still accesses invalid source: command=%s cwd=%s", server.Command, server.WorkingDir)
	}
}

func writeIsolationConnector(t *testing.T, root, id, authMode, mcpJSON string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"connector.json": `{"id":"` + id + `","name":"Fixture","version":"1.0.0","type":"mcp","authMode":"` + authMode + `"}`,
		"mcp.json":       mcpJSON,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func assertHealthyIsolationServer(t *testing.T, registry *Registry) {
	t.Helper()
	if servers := registry.Servers(); len(servers) != 1 || servers[0].ConnectorID != "healthy" {
		t.Fatalf("healthy route missing or partial broken routes published: %#v", servers)
	}
}
