package platformcontrol

import (
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeArgumentsRemovesEverySensitiveValue(t *testing.T) {
	const secret = "plain-value-must-not-survive"
	const candidate = "candidate-content-must-not-survive"
	const idempotency = "idempotency-key-must-not-survive"
	raw := `{"operation":"run.env.set","params":{"key":"TOKEN","value":"` + secret + `","content":"` + candidate + `","idempotencyKey":"` + idempotency + `"}}`

	sanitized := SanitizeArguments(raw)
	for _, forbidden := range []string{secret, candidate, idempotency} {
		if strings.Contains(sanitized, forbidden) {
			t.Fatalf("sanitized arguments contain %q: %s", forbidden, sanitized)
		}
	}
	if !strings.Contains(sanitized, `"value":"[REDACTED]"`) || !strings.Contains(sanitized, `"content":"[REDACTED]"`) || !strings.Contains(sanitized, `"idempotencyKey":"[REDACTED]"`) {
		t.Fatalf("sanitized arguments do not retain safe shape: %s", sanitized)
	}
}

func TestOperationRegistryOnlyExposesSetAndUnsetForRunEnvironment(t *testing.T) {
	want := []string{"capabilities.list", "catalog.defaults.get", "catalog.validate", "run.env.set", "run.env.unset", "runtime.status", "security.explain"}
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
