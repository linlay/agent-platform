package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestMCPPrivateOwnersUseSeparateCredentialsSessionsAndEnablement(t *testing.T) {
	upstream := newSDKMCPTestServer(t, "read_document", nil)
	defer upstream.Close()
	var mu sync.Mutex
	received := map[string]int{}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := r.Header.Get("Authorization")
		if value != "Bearer alice-secret" && value != "Bearer bob-secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		mu.Lock()
		received[value]++
		mu.Unlock()
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "TOKEN", "required": true}}}},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": endpoint.URL, "headers": map[string]any{"Authorization": "Bearer ${TOKEN}"}}}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, owner := range []string{"alice", "bob"} {
		private := sources
		private.Owner = owner
		probe := NewClientWithGate(nil, endpoint.Client(), nil)
		defer probe.Close()
		manager := connectorauth.New(t.Context(), private, nil).WithCredentialValidator(probe.ValidateOwnerCredentials)
		if _, err := manager.SetToken(t.Context(), "demo", map[string]string{"TOKEN": owner + "-secret"}); err != nil {
			t.Fatal(err)
		}
		pkg, _ := private.Load("demo")
		yes := true
		if _, err := pkg.UpdateConnection(nil, &yes); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := NewRegistryWithSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Server("demo")
	if definition.Headers["Authorization"] != "Bearer ${TOKEN}" || definition.SetupError == "" {
		t.Fatal("global catalog resolved private account")
	}
	client := NewClientWithGate(registry, endpoint.Client(), NewAvailabilityGate())
	defer client.Close()
	if _, err := client.ListTools(context.Background(), "demo"); err == nil {
		t.Fatal("ownerless discovery used private credentials")
	}
	for _, owner := range []string{"alice", "bob"} {
		ctx := connector.WithOwner(t.Context(), owner)
		list, err := client.ListTools(ctx, "demo")
		if err != nil || len(list) != 1 {
			t.Fatalf("%s tools: %v %v", owner, list, err)
		}
		if _, err := client.CallTool(ctx, "demo", "read_document", nil, nil); err != nil {
			t.Fatalf("%s call: %v", owner, err)
		}
	}
	definitions, err := client.DiscoverOwnerTools(connector.WithOwner(t.Context(), "alice"), []string{"demo"})
	if err != nil || len(definitions) != 1 {
		t.Fatalf("private session discovery: %v %v", definitions, err)
	}
	public := NewToolSync(registry, client)
	if len(public.Definitions()) != 0 {
		t.Fatal("private discovery populated global tools")
	}
	alice, bob := client.ownerClients["alice"], client.ownerClients["bob"]
	if alice == bob || alice.slots["demo"].current == bob.slots["demo"].current || alice.gate == bob.gate {
		t.Fatal("owners shared sessions or availability")
	}
	private := sources
	private.Owner = "alice"
	pkg, _ := private.Load("demo")
	disabled := false
	if _, err := pkg.UpdateConnection(nil, &disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(connector.WithOwner(t.Context(), "alice"), "demo", "read_document", nil, nil); err == nil {
		t.Fatal("disabled owner called tool")
	}
	defs, err := client.DiscoverOwnerTools(connector.WithOwner(t.Context(), "alice"), []string{"demo"})
	if err != nil || len(defs) != 0 {
		t.Fatalf("disabled connector blocked chat or leaked tools: %v %v", defs, err)
	}
	if _, ok := alice.registry.Server("demo"); ok {
		t.Fatal("disabled owner's session retained")
	}
	if _, err := client.CallTool(connector.WithOwner(t.Context(), "bob"), "demo", "read_document", nil, nil); err != nil {
		t.Fatalf("disabling Alice affected Bob: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if received["Bearer alice-secret"] == 0 || received["Bearer bob-secret"] == 0 {
		t.Fatal("both owner credentials were not used")
	}
}

func TestMCPPrivateOwnerRuntimeEnvironmentUsesPrivateRoots(t *testing.T) {
	root := t.TempDir()
	pkg := writeOneIDFixture(t, root, map[string]any{"type": "stdio", "command": os.Args[0], "args": []string{"--version"}})
	// Stdio MCP without a CLI still needs per-owner HOME/XDG isolation.
	pkg.Owner = "alice"
	def, err := connectorServer(pkg, "main")
	if err != nil {
		t.Fatal(err)
	}
	if def.Env["HOME"] == "" || !strings.Contains(def.Env["HOME"], "users") {
		t.Fatal("wrong account home")
	}
}

func TestDisconnectCancelsOnlyMatchingOwnerInvocations(t *testing.T) {
	root := t.TempDir()
	pkg := writeOneIDFixture(t, root, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	for _, owner := range []string{"alice", "bob"} {
		p := pkg
		p.Owner = owner
		yes := true
		if _, err := p.UpdateConnection(&yes, &yes); err != nil {
			t.Fatal(err)
		}
	}
	registry := &Registry{servers: map[string]ServerDefinition{"demo": {Key: "demo", ConnectorID: "demo", ConnectorPackage: &pkg}}}
	client := NewClientWithGate(registry, nil, nil)
	defer client.Close()
	alice, releaseAlice, err := client.beginOwnerInvocation(connector.WithOwner(t.Context(), "alice"), "demo")
	if err != nil {
		t.Fatal(err)
	}
	bob, releaseBob, err := client.beginOwnerInvocation(connector.WithOwner(t.Context(), "bob"), "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBob()
	go func() { <-alice.Done(); releaseAlice() }()
	p := pkg
	p.Owner = "alice"
	no := false
	p.UpdateConnection(&no, &no)
	if err := client.DisconnectOwnerConnector(t.Context(), "alice", "demo"); err != nil {
		t.Fatal(err)
	}
	if alice.Err() == nil || bob.Err() != nil {
		t.Fatal("wrong owner cancellation", alice.Err(), bob.Err())
	}
	if _, _, err := client.beginOwnerInvocation(connector.WithOwner(t.Context(), "alice"), "demo"); err == nil {
		t.Fatal("disconnected owner restarted")
	}
}
