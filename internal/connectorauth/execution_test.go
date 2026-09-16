package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateConnectorGateRecognizesOnlyInvocationAndRejectsCompound(t *testing.T) {
	root := t.TempDir()
	writeCLIPackage(t, root, "demo", simpleCLI())
	os.MkdirAll(filepath.Join(root, "demo", "bin"), 0755)
	os.WriteFile(filepath.Join(root, "demo", "bin", "demo.js"), []byte(""), 0644)
	manager := New(context.Background(), connector.Sources{ExternalRoot: root}, nil).ForOwner("user:alice")
	for _, command := range []string{"demo read", "node " + root + "/demo/bin/demo.js"} {
		if _, err := manager.ResolveBashEnvironment(context.Background(), []string{"demo"}, command); err == nil || !strings.Contains(err.Error(), "connector_disabled") {
			t.Fatal(command, err)
		}
	}
	if env, err := manager.ResolveBashEnvironment(context.Background(), []string{"demo"}, "echo "+root+"/demo/bin/demo.js"); err != nil || len(env) != 0 {
		t.Fatal("non-invocation received private environment", env, err)
	}
	if _, err := manager.ResolveBashEnvironment(context.Background(), []string{"demo"}, "demo read &"); err == nil || !strings.Contains(err.Error(), "attached") {
		t.Fatal("detached CLI accepted", err)
	}
	if _, err := manager.ResolveBashEnvironment(context.Background(), []string{"demo"}, "demo read && env"); err == nil || !strings.Contains(err.Error(), "separate") {
		t.Fatal("compound could inherit private credentials", err)
	}
}
