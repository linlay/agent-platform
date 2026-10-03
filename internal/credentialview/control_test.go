package credentialview

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogCandidateHistory(t *testing.T) {
	for _, resource := range []string{"agent", "team", "skill", "connector"} {
		raw := `{"action":"apply","args":{"resourceType":"` + resource + `","content":"candidate\\ntext","preservePaths":["runtimeConfig.env.TOKEN"]}}`
		got := CatalogArguments(raw)
		if strings.Contains(got, Hidden) || !strings.Contains(got, "candidate") || !json.Valid([]byte(got)) {
			t.Fatal(got)
		}
		if CatalogArguments(got) != got {
			t.Fatal("normalization is not idempotent")
		}
	}
	if got := CatalogArguments(`{"args":{"resourceType":"unknown","content":"secret"}}`); strings.Contains(got, "secret") {
		t.Fatal(got)
	}
	if got := CatalogArguments(`{"args":`); strings.Contains(got, "args") {
		t.Fatal(got)
	}
}
