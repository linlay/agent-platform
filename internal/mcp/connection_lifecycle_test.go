package mcp

import (
	"agent-platform/internal/connector"
	"context"
	"path/filepath"
	"testing"
)

func TestCachedMCPClientChecksConnectionPreferenceOnEveryCall(t *testing.T) {
	server := newSDKMCPTestServer(t, "read", nil)
	defer server.Close()
	root := t.TempDir()
	if err := writeConnectorFixture(filepath.Join(root, "server.yml"), []byte("key: demo\nbaseUrl: "+server.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClientWithGate(registry, server.Client(), nil)
	defer client.Close()
	ctx := context.Background()
	if _, err := client.CallTool(ctx, "demo", "read", nil, nil); err != nil {
		t.Fatal(err)
	}
	pkg, err := (connector.Sources{ExternalRoot: root}).Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	no := false
	if _, err := pkg.UpdateConnection(nil, &no); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, "demo", "read", nil, nil); err == nil {
		t.Fatal("cached MCP session bypassed disable")
	}
	yes := true
	if _, err := pkg.UpdateConnection(nil, &yes); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, "demo", "read", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.UpdateConnection(&no, &no); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, "demo", "read", nil, nil); err == nil {
		t.Fatal("cached MCP session bypassed disconnect")
	}
	if err := client.DisconnectConnector(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	slot, err := client.slot("demo")
	if err != nil {
		t.Fatal(err)
	}
	slot.mu.Lock()
	cached := slot.current
	slot.mu.Unlock()
	if cached != nil {
		t.Fatal("disconnect retained cached authenticated session")
	}

}
