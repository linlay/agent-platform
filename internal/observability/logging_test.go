package observability

import (
	"strings"
	"testing"
)

func TestCredentialLogPatterns(t *testing.T) {
	for _, raw := range []string{`{"apiKey":"secret with spaces"}`, "password='secret with spaces'", "Authorization: Bearer secret-value", "https://example.test/?refresh_token=secret-value&visible=1", `client_secret="secret-value"`, "sk-secret-value"} {
		got := SanitizeLog(raw)
		if strings.Contains(got, "secret with spaces") || strings.Contains(got, "secret-value") {
			t.Fatal(got)
		}
		if SanitizeLog(got) != got {
			t.Fatalf("not idempotent: %q -> %q", got, SanitizeLog(got))
		}
	}
	for _, raw := range []string{"modelKey: example-model", "key: demo", "connectorConfig:\n  connectors: [builtin.dbx]"} {
		if got := SanitizeLog(raw); got != raw {
			t.Fatal(got)
		}
	}
}
