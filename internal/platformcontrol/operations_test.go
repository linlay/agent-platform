package platformcontrol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/runenv"
)

func TestSanitizeArgumentsRemovesEverySensitiveValue(t *testing.T) {
	const secret = "plain-value-must-not-survive"
	const candidate = "candidate-content-must-not-survive"
	const idempotency = "idempotency-key-must-not-survive"
	raw := `{"operation":"run.env.bulk","params":{"changes":[{"operation":"set","key":"TOKEN","value":"` + secret + `"}],"content":"` + candidate + `","idempotencyKey":"` + idempotency + `"}}`

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

func TestMutationApprovalDoesNotModifyInvocationArguments(t *testing.T) {
	policy, err := runenv.ParsePolicy(map[string]any{
		"TOKEN": map[string]any{"mode": "mutable", "approval": "each-change"},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"operation": "run.env.set", "params": map[string]any{
		"key": "TOKEN", "value": "actual-value", "idempotencyKey": "actual-key",
	}}
	before, _ := json.Marshal(args)
	metadata := MutationApproval(args, policy)
	after, _ := json.Marshal(args)
	if !metadata.Required || !reflect.DeepEqual(before, after) {
		t.Fatalf("approval metadata modified invocation: required=%v before=%s after=%s", metadata.Required, before, after)
	}
	if !strings.Contains(metadata.Display, "source=run.dynamic") || strings.Contains(metadata.Display, "actual-value") {
		t.Fatalf("approval display is missing safe source metadata or leaked a value: %q", metadata.Display)
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

func TestOperationStagesComeFromDescriptor(t *testing.T) {
	readOnly, _ := LookupOperation("run.env.list")
	if !readOnly.AllowsExecutionPolicy("") || !readOnly.AllowsExecutionPolicy("read_only") {
		t.Fatalf("read-only descriptor must allow main and planning stages: %#v", readOnly)
	}
	mutation, _ := LookupOperation("run.env.bind")
	if !mutation.AllowsExecutionPolicy("") || mutation.AllowsExecutionPolicy("read_only") {
		t.Fatalf("mutation descriptor must allow main but reject planning: %#v", mutation)
	}
}
