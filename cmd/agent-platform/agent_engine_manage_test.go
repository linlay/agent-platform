package main

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"agent-platform/internal/agentengine"
	agentruntime "agent-platform/internal/runtime"
	"agent-platform/internal/server"
)

func TestEngineManagementCommandUsesLiveStandaloneDirectory(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	engines, err := agentengine.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engines.Close()
	if err = engines.Restore(); err != nil {
		t.Fatal(err)
	}
	handler, err := server.New(server.Dependencies{AgentEngines: engines, Runtime: agentruntime.NewService()})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	address, _ := url.Parse(httpServer.URL)
	if err = engines.PublishLocalManagement(address.Port()); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = runAgentEngineManagement([]string{"list", "--runtime-dir", root}, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"ready":true`)) {
		t.Fatalf("directory=%s", output.String())
	}
	access, err := agentengine.ReadLocalManagement(root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte(access.Secret)) {
		t.Fatal("secret leaked")
	}
	if err = runAgentEngineManagement([]string{"disable", "--runtime-dir", root, "--id", "absent"}, &output); err == nil {
		t.Fatal("missing plugin succeeded")
	}
}
