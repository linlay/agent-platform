package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestOrdinaryToolsCannotReadConnectorSecretsAtFullAccess(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	secret := filepath.Join(state, "users", "owner", "demo", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(secret), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte(`{"API_KEY":"must-never-reach-model"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		context := &contracts.ExecutionContext{Session: contracts.QuerySession{WorkspaceRoot: workspace, AccessLevel: level, ConnectorStateRoot: state}}
		reader := fileToolExecutor(workspace, true)
		result, err := reader.invokeRead(map[string]any{"file_path": secret}, context)
		if err != nil || result.Error != "file_read_path_blocked" || strings.Contains(result.Output, "must-never-reach-model") {
			t.Fatalf("%s file_read leak: %+v %v", level, result, err)
		}
		executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"cat"}, ShellFeaturesEnabled: true, ShellExecutable: "bash", MaxCommandChars: 16000}}}
		result, err = executor.invokeHostBash(t.Context(), map[string]any{"command": "cat '" + secret + "'"}, context)
		if err != nil || result.Error != "bash_access_blocked" || strings.Contains(result.Output, "must-never-reach-model") {
			t.Fatalf("%s bash leak: %+v %v", level, result, err)
		}
	}
}
