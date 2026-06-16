package apperrors

import "testing"

func TestCatalogDefinitionsAreCompleteAndUnique(t *testing.T) {
	seen := map[Code]bool{}
	for _, definition := range Definitions() {
		if definition.Code == "" {
			t.Fatalf("empty code in definition %#v", definition)
		}
		if seen[definition.Code] {
			t.Fatalf("duplicate code %q", definition.Code)
		}
		seen[definition.Code] = true
		if definition.Category == "" || definition.Scope == "" || definition.HTTPStatus == 0 || definition.UserSafeMessageKey == "" {
			t.Fatalf("incomplete definition %#v", definition)
		}
		if got, ok := Lookup(definition.Code); !ok || got != definition {
			t.Fatalf("lookup mismatch for %q: %#v %t", definition.Code, got, ok)
		}
	}
}

func TestPayloadUsesCatalogDefaultsAndOverrides(t *testing.T) {
	payload := Payload(CodeProviderQuotaExhausted, "quota exhausted")
	if payload["category"] != string(CategoryModel) || payload["scope"] != string(ScopeModel) {
		t.Fatalf("unexpected provider payload scope/category: %#v", payload)
	}
	if payload["status"] != 429 || payload["retryable"] != false {
		t.Fatalf("unexpected provider payload metadata: %#v", payload)
	}

	payload = Payload(CodeStreamFailed, "boom", WithScope(ScopeRun), WithStatus(502), WithRetryable(true), WithDiagnostic("upstream", "transit-hub"))
	if payload["status"] != 502 || payload["retryable"] != true {
		t.Fatalf("expected overrides in payload: %#v", payload)
	}
	diagnostics, _ := payload["diagnostics"].(map[string]any)
	if diagnostics["upstream"] != "transit-hub" {
		t.Fatalf("expected diagnostics in payload: %#v", payload)
	}
}
