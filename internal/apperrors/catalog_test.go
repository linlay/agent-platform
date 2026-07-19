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

func TestInternalRuntimeCodesAreCatalogedWithExistingMetadata(t *testing.T) {
	tests := []Definition{
		{Code: CodePlanningNotCreated, Category: CategoryModel, Scope: ScopeRun, HTTPStatus: 500, Retryable: false, UserSafeMessageKey: string(CodePlanningNotCreated)},
		{Code: CodePlanNotCreated, Category: CategorySystem, Scope: ScopeRun, HTTPStatus: 500, Retryable: false, UserSafeMessageKey: string(CodePlanNotCreated)},
		{Code: CodeMissingToolCallID, Category: CategoryModel, Scope: ScopeModel, HTTPStatus: 500, Retryable: false, UserSafeMessageKey: string(CodeMissingToolCallID)},
		{Code: CodeToolCallsNotAllowed, Category: CategorySystem, Scope: ScopeRun, HTTPStatus: 500, Retryable: false, UserSafeMessageKey: string(CodeToolCallsNotAllowed)},
		{Code: CodeBTWToolLimitReached, Category: CategoryTool, Scope: ScopeTool, HTTPStatus: 500, Retryable: false, UserSafeMessageKey: string(CodeBTWToolLimitReached)},
		{Code: CodeTeamMemberFailed, Category: CategorySystem, Scope: ScopeTask, HTTPStatus: 500, Retryable: false, UserSafeMessageKey: string(CodeTeamMemberFailed)},
	}
	for _, want := range tests {
		got, ok := Lookup(want.Code)
		if !ok || got != want {
			t.Fatalf("definition %q = %#v, %t; want %#v", want.Code, got, ok, want)
		}
	}
}

func TestPayloadContextOverridesRemainStable(t *testing.T) {
	tests := []struct {
		name       string
		code       Code
		category   Category
		scope      Scope
		wantStatus int
		wantRetry  bool
	}{
		{name: "tool budget", code: CodeToolCallsExceeded, category: CategoryTool, scope: ScopeTool, wantStatus: 400},
		{name: "HITL rejection", code: CodeHitlRejected, category: CategorySystem, scope: ScopeTool, wantStatus: 403},
		{name: "HITL timeout", code: CodeHitlTimeout, category: CategoryTimeout, scope: ScopeTool, wantStatus: 504, wantRetry: true},
		{name: "run timeout", code: CodeRunTimeout, category: CategoryTimeout, scope: ScopeRun, wantStatus: 504, wantRetry: true},
		{name: "model budget", code: CodeModelCallsExceeded, category: CategoryModel, scope: ScopeModel, wantStatus: 400},
		{name: "task failure", code: CodeTaskFailed, category: CategorySystem, scope: ScopeTask, wantStatus: 500},
		{name: "plan context", code: CodePlanContextUnavailable, category: CategorySystem, scope: ScopeRun, wantStatus: 503},
		{name: "frontend submit", code: CodeFrontendSubmitInvalidPayload, category: CategoryTool, scope: ScopeFrontendSubmit, wantStatus: 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := Payload(tc.code, "message", WithCategory(tc.category), WithScope(tc.scope))
			if payload["category"] != string(tc.category) || payload["scope"] != string(tc.scope) || payload["status"] != tc.wantStatus || payload["retryable"] != tc.wantRetry {
				t.Fatalf("unexpected payload %#v", payload)
			}
		})
	}
}
