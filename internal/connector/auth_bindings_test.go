package connector

import (
	"encoding/json"
	"testing"
)

func TestCredentialBindingValidation(t *testing.T) {
	for _, test := range []struct {
		name, target, key, value string
		header, valid            bool
	}{
		{"HTTP header", "mcp:DocX", "X-API-Key", "${API_KEY}", true, true},
		{"CLI env", "cli", "SERVICE_TOKEN", "${API_KEY}", false, true},
		{"missing field", "cli", "SERVICE_TOKEN", "${MISSING}", false, false},
		{"inline secret", "mcp:DocX", "X-API-Key", "secret", true, false},
		{"shell execution variable", "cli", "BASH_ENV", "${API_KEY}", false, false},
		{"wrong transport", "mcp:DocX", "SERVICE_TOKEN", "${API_KEY}", false, false},
		{"unknown target", "mcp:other", "X-API-Key", "${API_KEY}", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := AuthBinding{}
			if test.header {
				binding.Headers = map[string]string{test.key: test.value}
			} else {
				binding.Env = map[string]string{test.key: test.value}
			}
			pkg := Package{Manifest: Manifest{AuthMode: AuthToken, TokenSchema: json.RawMessage(`{"fields":[{"key":"API_KEY","required":true}]}`), AuthBindings: map[string]AuthBinding{test.target: binding}}, MCP: map[string]map[string]any{"DocX": {"type": "http", "url": "https://example.com/mcp"}}, CLI: map[string]any{}}
			if err := pkg.ValidateAuthBindings(); (err == nil) != test.valid {
				t.Fatalf("validation=%v, want valid=%v", err, test.valid)
			}
		})
	}
}
