package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func TestConnectorContainerPathUsesOnlyContainerLocations(t *testing.T) {
	ctx := &contracts.ExecutionContext{Session: contracts.QuerySession{ConnectorBinDirs: []string{filepath.Join(t.TempDir(), "builtin.dbx", "bin")}}}
	env, err := sandboxCommandEnvironment(ctx, map[string]string{"PATH": "/custom/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if env["PATH"] != "/connectors/builtin.dbx/bin:/custom/bin" {
		t.Fatalf("container PATH %q", env["PATH"])
	}
	if strings.Contains(env["PATH"], ctx.Session.ConnectorBinDirs[0]) {
		t.Fatal("host connector directory leaked to container")
	}
}
