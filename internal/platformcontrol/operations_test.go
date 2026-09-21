package platformcontrol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeArgumentsPreservesRunEnvironmentValueAndRedactsSensitiveMetadata(t *testing.T) {
	const value = "plain-value-must-survive"
	const candidate = "candidate-content-must-not-survive"
	const idempotency = "idempotency-key-must-not-survive"
	raw := `{"operation":"run.env.set","params":{"key":"DOCUMENT_ID","value":"` + value + `","content":"` + candidate + `","idempotencyKey":"` + idempotency + `"}}`

	sanitized := SanitizeArguments(raw)
	if !strings.Contains(sanitized, `"value":"`+value+`"`) || strings.Contains(sanitized, `"valueBytes"`) {
		t.Fatalf("sanitized arguments did not preserve run environment value: %s", sanitized)
	}
	for _, forbidden := range []string{candidate, idempotency} {
		if strings.Contains(sanitized, forbidden) {
			t.Fatalf("sanitized arguments contain %q: %s", forbidden, sanitized)
		}
	}
	if !strings.Contains(sanitized, `"content":"[REDACTED]"`) || !strings.Contains(sanitized, `"idempotencyKey":"[REDACTED]"`) {
		t.Fatalf("sanitized arguments do not retain safe shape: %s", sanitized)
	}
}

func TestSanitizeArgumentsKeepsUnknownOperationValueFailClosed(t *testing.T) {
	const value = "unknown-operation-value"
	sanitized := SanitizeArguments(`{"operation":"unknown","params":{"value":"` + value + `"}}`)
	if strings.Contains(sanitized, value) || !strings.Contains(sanitized, `"value":"[REDACTED]"`) {
		t.Fatalf("unknown operation value was not fail-closed: %s", sanitized)
	}
}

func TestOperationRegistryOnlyExposesSetAndUnsetForRunEnvironment(t *testing.T) {
	want := []string{"capabilities.list", "catalog.defaults.get", "catalog.validate", "chat.set_pinned", "run.env.set", "run.env.unset", "runtime.status", "security.explain"}
	if got := OperationNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %#v, want %#v", got, want)
	}
	for _, removed := range []string{"run.env.bind", "run.env.get", "run.env.list", "run.env.bulk"} {
		if _, ok := LookupOperation(removed); ok {
			t.Fatalf("removed operation %q is still registered", removed)
		}
	}
}

func TestEveryOperationDescriptorOwnsValidationAndInvocation(t *testing.T) {
	for _, name := range OperationNames() {
		descriptor, ok := LookupOperation(name)
		if !ok || descriptor.Name != name || descriptor.Validate == nil || descriptor.Invoke == nil {
			t.Fatalf("incomplete descriptor for %q: %#v", name, descriptor)
		}
		if descriptor.RiskClass == "" || len(descriptor.AllowedStages) == 0 {
			t.Fatalf("descriptor metadata is incomplete for %q: %#v", name, descriptor)
		}
	}
}

func TestSetAndUnsetStagesComeFromDescriptor(t *testing.T) {
	for _, name := range []string{"run.env.set", "run.env.unset"} {
		mutation, _ := LookupOperation(name)
		if !mutation.AllowsExecutionPolicy("") || mutation.AllowsExecutionPolicy("read_only") {
			t.Fatalf("mutation descriptor must allow main but reject planning: %#v", mutation)
		}
	}
}

func TestSanitizeCandidateArgumentsIsIdempotentAndPreservesRequestShape(t *testing.T) {
	raw := `{"operation":"catalog.validate","params":{"resourceType":"unknown","resourceKey":"demo","content":"key: demo\nname: 中文文档\n"}}`
	once := SanitizeArguments(raw)
	for i := 0; i < 3; i++ {
		if got := SanitizeArguments(once); got != once {
			t.Fatalf("sanitization changed history: %s -> %s", once, got)
		}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(once), &args); err != nil {
		t.Fatal(err)
	}
	params := args["params"].(map[string]any)
	if len(params) != 3 || params["content"] != "[REDACTED]" {
		t.Fatalf("unexpected request shape: %s", once)
	}
	if err := validateOperationParams("catalog.validate", params); err != nil {
		t.Fatalf("sanitizer injected invalid parameters: %v", err)
	}
	if strings.Contains(once, "中文") {
		t.Fatal("candidate leaked")
	}
}

func TestSanitizeCatalogCandidateByResourceType(t *testing.T) {
	const content = "key: demo\nname: 中文文档\nmodelConfig:\n  modelKey: example-model\n"
	for _, tc := range []struct {
		name         string
		operation    string
		resourceType any
		visible      bool
	}{
		{"agent", "catalog.validate", "agent", true},
		{"normalized-agent", " CATALOG.VALIDATE ", " Agent ", true},
		{"team", "catalog.validate", "team", true},
		{"skill", "catalog.validate", "skill", true},
		{"connector", "catalog.validate", "connector", true},
		{"unknown-type", "catalog.validate", "unknown", false},
		{"missing-type", "catalog.validate", nil, false},
		{"invalid-type", "catalog.validate", 123, false},
		{"unknown-operation", "unknown", "agent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{"resourceKey": "demo", "content": content, "idempotencyKey": "private-key"}
			if tc.resourceType != nil {
				params["resourceType"] = tc.resourceType
			}
			raw, _ := json.Marshal(map[string]any{"operation": tc.operation, "params": params})
			once := SanitizeArguments(string(raw))
			if SanitizeArguments(once) != once {
				t.Fatal("sanitization is not idempotent")
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(once), &got); err != nil {
				t.Fatal(err)
			}
			actual := got["params"].(map[string]any)
			want := "[REDACTED]"
			if tc.visible {
				want = content
			}
			if actual["content"] != want || actual["idempotencyKey"] != "[REDACTED]" || len(actual) != len(params) {
				t.Fatalf("unexpected sanitized params: %#v", actual)
			}
		})
	}
}
