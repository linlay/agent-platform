package tools

import (
	"agent-platform/internal/contracts"
	"strings"
	"testing"
)

func TestConnectorShellBindingsNeverPutCredentialsInArgv(t *testing.T) {
	result := connectorShellPathBindings(map[string]string{"HOME": "/private/home", "CONNECTOR_BIN_DIR": "/private/bin", "API_KEY": "secret", "OAUTH_ACCESS_TOKEN": "token", "DEMO_CONFIG_DIR": "secret-as-config"})
	if result["HOME"] != "/private/home" || result["CONNECTOR_BIN_DIR"] != "/private/bin" {
		t.Fatal(result)
	}
	for _, key := range []string{"API_KEY", "OAUTH_ACCESS_TOKEN", "DEMO_CONFIG_DIR"} {
		if _, ok := result[key]; ok {
			t.Fatal("secret rebound in command line", key)
		}
	}
}

func TestConnectorInterruptedOutcomePreventsAutomaticReplay(t *testing.T) {
	result := connectorInterruptedResult(contracts.ToolExecutionResult{Output: "partial observation", ExitCode: -1}, true)
	if result.Error != "OUTCOME_UNKNOWN" || result.Structured["retryable"] != false || result.Structured["executed"] != nil || !strings.Contains(result.Output, "partial observation") {
		t.Fatal(result)
	}
	ordinary := connectorInterruptedResult(contracts.ToolExecutionResult{Error: "provider_permission_denied", ExitCode: 1}, false)
	if ordinary.Error != "provider_permission_denied" {
		t.Fatal("ordinary error reclassified", ordinary)
	}
}
